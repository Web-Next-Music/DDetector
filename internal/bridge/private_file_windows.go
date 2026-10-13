package bridge

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// As in openStorageLock, pin every ancestor without delete sharing and inspect
// the handles themselves: Lstat alone does not reject every kind of reparse point.
func guardPrivateDirectory(dir string) (func(), error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var handles []windows.Handle
	release := func() {
		for _, h := range handles {
			windows.CloseHandle(h)
		}
	}
	for parent := absolute; ; parent = filepath.Dir(parent) {
		name, err := windows.UTF16PtrFromString(parent)
		if err != nil {
			release()
			return nil, err
		}
		h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			release()
			return nil, err
		}
		handles = append(handles, h)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err != nil {
			release()
			return nil, err
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			release()
			return nil, fmt.Errorf("private file directory must be non-reparse")
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return release, nil
}

func securePrivateFile(f *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	// Match the protected current-user-only DACL used by openStorageLock.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func privateFileModeOK(info os.FileInfo, exact bool) bool { return info.Mode().IsRegular() }

func openPrivateFile(path string) (*os.File, func(), error) {
	release, err := guardPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, func(), error) { release(); return nil, nil, err }
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fail(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.WRITE_DAC, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fail(err)
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return fail(err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(h)
		return fail(fmt.Errorf("private file must be regular and non-reparse"))
	}
	f := os.NewFile(uintptr(h), path)
	if err := securePrivateFile(f); err != nil {
		f.Close()
		return fail(err)
	}
	return f, release, nil
}
func replacePrivateFile(old, next string) error {
	source, err := windows.UTF16PtrFromString(old)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(next)
	if err != nil {
		return err
	}
	// renameStorageNoReplace uses WRITE_THROUGH too, but deliberately forbids
	// replacement. Private-file updates require replacement of the old snapshot.
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func syncPrivateDirectory(string) error {
	// Windows cannot FlushFileBuffers on a directory. The file was synced before
	// closing, and replacement above requests write-through.
	return nil
}

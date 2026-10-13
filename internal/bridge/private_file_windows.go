package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

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

func privateSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	// Match the protected current-user-only DACL used by openStorageLock.
	return windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
}

func createPrivateTemp(dir, pattern string) (*os.File, error) {
	if strings.ContainsAny(pattern, `/\`) {
		return nil, fmt.Errorf("temporary file pattern contains path separator")
	}
	sd, err := privateSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	prefix, suffix := pattern, ""
	if i := strings.LastIndexByte(pattern, '*'); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	for attempts := 0; attempts < 100; attempts++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, prefix+hex.EncodeToString(random[:])+suffix)
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		// Install the protected DACL at creation; retain this handle throughout
		// writing so no reopening race or inherited-public-ACL window exists.
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == windows.ERROR_FILE_EXISTS || err == windows.ERROR_ALREADY_EXISTS {
			continue
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(h), path), nil
	}
	return nil, fmt.Errorf("private temporary file name collisions: %w", os.ErrExist)
}

func securePrivateFile(f *os.File) error {
	sd, err := privateSecurityDescriptor()
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

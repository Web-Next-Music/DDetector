package bridge

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows synthesizes Unix permission bits. Inspect the object's security
// descriptor instead; never repair an existing public ACL during validation.
func storagePrivate(path string, info os.FileInfo, exact bool) bool {
	release, err := guardPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return false
	}
	defer release()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var stat windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &stat) != nil || stat.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	return err == nil && storageDescriptorPrivate(sd)
}

func storageDescriptorPrivate(sd *windows.SECURITY_DESCRIPTOR) bool {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return false
	}
	acl, _, err := sd.DACL()
	// A missing/null DACL grants everyone full access. An empty DACL grants none.
	if err != nil || acl == nil {
		return false
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil {
			return false
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if ace.Mask != 0 && !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
				return false
			}
		default:
			// Unknown/object/callback ACEs require semantics we cannot safely assume.
			return false
		}
	}
	return true
}

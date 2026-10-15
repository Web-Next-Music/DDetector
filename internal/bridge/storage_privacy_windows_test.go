package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStoragePrivacyWindowsDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "DDetector")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, public := range []bool{false, true} {
		sddl := "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")"
		if public {
			sddl += "(A;OICI;FR;;;WD)"
		}
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
		if err := ensureMigrationParent(dir); (err != nil) != public {
			t.Fatalf("migration public=%v: %v", public, err)
		}
		if err := checkCensorDir(dir, false); (err != nil) != public {
			t.Fatalf("censor public=%v: %v", public, err)
		}
	}
}

func TestStorageDescriptorPrivate(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for _, tc := range []struct {
		name, sddl string
		want       bool
	}{
		{"user", "O:" + sid + "D:P(A;;FA;;;" + sid + ")", true},
		{"profile inheritance", "O:" + sid + "D:(A;ID;FA;;;" + sid + ")(A;ID;FA;;;SY)(A;ID;FA;;;BA)", true},
		{"everyone", "O:" + sid + "D:P(A;;FR;;;WD)", false},
		{"users write", "O:" + sid + "D:P(A;;FW;;;BU)", false},
		{"public owner", "O:WDD:P(A;;FA;;;" + sid + ")", false},
		{"null DACL", "O:" + sid + "D:NO_ACCESS_CONTROL", false},
		{"empty DACL", "O:" + sid + "D:P", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if got := storageDescriptorPrivate(sd); got != tc.want {
				t.Fatalf("privacy = %v, want %v", got, tc.want)
			}
		})
	}
}

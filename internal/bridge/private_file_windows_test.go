package bridge

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateTempWindowsACLAndAccess(t *testing.T) {
	f, err := createPrivateTemp(t.TempDir(), ".session-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	// Inspect before securePrivateFile: creation itself must protect the file.
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	if sd.String() != expected.String() {
		t.Fatalf("creation DACL = %s; want %s", sd.String(), expected.String())
	}
	// SetSecurityInfo exercises WRITE_DAC on the original creation handle.
	if err := securePrivateFile(f); err != nil {
		t.Fatalf("creation handle lacks WRITE_DAC: %v", err)
	}
	if _, err := io.WriteString(f, "private"); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "private" {
		t.Fatalf("creation handle read = %q, %v", data, err)
	}
}

func TestPrivateFileWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(path, 100, true); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		expected, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
		if err != nil {
			t.Fatal(err)
		}
		if sd.String() != expected.String() {
			t.Fatalf("DACL = %s; want %s", sd.String(), expected.String())
		}
	}
	check()
	if err := writePrivateFile(path, ".session-", 100, func(w io.Writer) error { _, err := io.WriteString(w, "new"); return err }); err != nil {
		t.Fatal(err)
	}
	check()
	for _, value := range []string{"new", "replacement"} {
		if err := writePrivateFile(path, ".session-", 100, func(w io.Writer) error { _, err := io.WriteString(w, value); return err }); err != nil {
			t.Fatal(err)
		}
		data, err := readPrivateFile(path, 100, true)
		if err != nil || string(data) != value {
			t.Fatalf("saved file read = %q, %v; want %q", data, err, value)
		}
		check()
	}
}

func TestPrivateFileWindowsRejectsReparseAncestor(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, "session.json")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "redirect")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	redirected := filepath.Join(link, "session.json")
	if _, err := readPrivateFile(redirected, 100, true); err == nil {
		t.Fatal("read through reparse ancestor")
	}
	if err := writePrivateFile(redirected, ".session-", 100, func(w io.Writer) error { return nil }); err == nil {
		t.Fatal("wrote through reparse ancestor")
	}
}

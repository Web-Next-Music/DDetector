//go:build !windows

package bridge

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFileUnixPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	if err := writePrivateFile(path, ".session-", 100, func(io.Writer) error { return nil }); err == nil {
		t.Fatal("accepted public directory")
	}
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0644, 0640, 0660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		for _, exact := range []bool{false, true} {
			if _, err := readPrivateFile(path, 100, exact); err == nil {
				t.Fatalf("accepted %o", mode)
			}
		}
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(path, 100, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(path, 100, true); err == nil {
		t.Fatal("accepted non-exact mode")
	}
}

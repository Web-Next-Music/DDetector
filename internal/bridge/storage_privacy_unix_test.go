//go:build !windows

package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoragePrivacyUnixModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0400, 0700, 0644, 0660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, exact := range []bool{false, true} {
			want := mode&0077 == 0 && (!exact || mode == 0600)
			if got := storagePrivate(path, info, exact); got != want {
				t.Fatalf("mode %o exact %v: got %v", mode, exact, got)
			}
		}
	}
}

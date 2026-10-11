package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageLockRejectsReparseAncestor(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "redirect")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if f, err := openStorageLock(filepath.Join(link, "writer.lock")); err == nil {
		f.Close()
		t.Fatal("opened lock through reparse ancestor")
	}
	if _, err := os.Stat(filepath.Join(target, "writer.lock")); !os.IsNotExist(err) {
		t.Fatalf("created lock through rejected ancestor: %v", err)
	}
}

func TestStorageLockAllowsRenameWhileOwned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "writer.lock")
	f, err := openStorageLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockStorageFile(f); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "moved.lock")
	if err := os.Rename(path, next); err != nil {
		t.Fatal(err)
	}
	other, err := openStorageLock(next)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := lockStorageFile(other); err == nil {
		t.Fatal("rename lost ownership")
	}
}

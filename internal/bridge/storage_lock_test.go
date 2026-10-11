package bridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStorageLockOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer.lock")
	first, err := openStorageLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = lockStorageFile(first); err != nil {
		t.Fatal(err)
	}
	second, err := openStorageLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = lockStorageFile(second); err == nil {
		t.Fatal("second handle acquired owned lock")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = lockStorageFile(second); err != nil {
		t.Fatalf("close did not release lock: %v", err)
	}
}

func TestStorageLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lock")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if f, err := openStorageLock(link); err == nil {
		f.Close()
		t.Fatal("opened symlink lock")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("target changed: %q %v", data, err)
	}
}

func TestStorageProcessLiveness(t *testing.T) {
	if storageProcessGone(os.Getpid()) {
		t.Fatal("current process reported dead")
	}
	if storageProcessGone(0) || storageProcessGone(-1) {
		t.Fatal("invalid PID authorized stale-lock removal")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStorageExitedProcessHelper$")
	cmd.Env = append(os.Environ(), "DDETECTOR_EXITED_PROCESS_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !storageProcessGone(pid) {
		t.Fatal("exited process not detected")
	}
}
func TestStorageExitedProcessHelper(t *testing.T) {
	if os.Getenv("DDETECTOR_EXITED_PROCESS_HELPER") == "1" {
		os.Exit(0)
	}
}

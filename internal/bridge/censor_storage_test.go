package bridge

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func storageRetryRuntime(t *testing.T, dir string) *censorRuntime {
	t.Helper()
	rt := &censorRuntime{dir: dir, stateVersion: sha256.Sum256(nil), disk: censorDiskState{UserID: "storage-test", Settings: defaultCensorSettings(), Jobs: []CensorJob{}}}
	t.Cleanup(func() {
		if err := rt.releaseStorage(); err != nil {
			t.Error(err)
		}
	})
	return rt
}

func TestCensorStorageRetriesOwnershipWithoutBypass(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "censor", "account")
	owner := storageRetryRuntime(t, dir)
	contender := storageRetryRuntime(t, dir)
	if err := owner.lockStorageLocked(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime.lock")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := contender.persistLocked(); !errors.Is(err, ErrCensorConflict) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if contender.failure != "" || contender.storageFailure == "" || contender.lock != nil {
			t.Fatal("acquisition failure became terminal or acquired ownership")
		}
		if err := contender.commandAdmissionErrorLocked(); err != nil {
			t.Fatalf("retry admission blocked: %v", err)
		}
		if contender.snapshot().Error == "" {
			t.Fatal("storage error not visible")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("contender wrote state: %v", err)
	}
	if err := owner.releaseStorage(); err != nil {
		t.Fatal(err)
	}
	if err := contender.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if contender.snapshot().Error != "" {
		t.Fatal("successful retry retained stale error")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock file replaced: %v", err)
	}
	if err := owner.lockStorageLocked(); !errors.Is(err, ErrCensorConflict) {
		t.Fatalf("new owner not exclusive: %v", err)
	}
}

func TestCensorStorageRetryRejectsChangedState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "censor", "account")
	owner := storageRetryRuntime(t, dir)
	contender := storageRetryRuntime(t, dir)
	if err := owner.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if err := contender.persistLocked(); !errors.Is(err, ErrCensorConflict) {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.releaseStorage(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := contender.persistLocked(); err == nil {
			t.Fatal("stale runtime overwrote newer state")
		}
		if contender.lock != nil || contender.storageFailure == "" {
			t.Fatal("stale runtime acquired ownership or cleared failure")
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("durable state changed: %v", err)
	}
	// Failed version checks must release the acquired OS lock.
	if err := owner.lockStorageLocked(); err != nil {
		t.Fatal(err)
	}
}

func TestCensorStorageRetryRevalidatesDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	parent := filepath.Join(t.TempDir(), "censor")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	rt := storageRetryRuntime(t, filepath.Join(parent, "account"))
	if err := rt.persistLocked(); err == nil {
		t.Fatal("public parent accepted")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, rt.dir); err != nil {
		t.Fatal(err)
	}
	if err := rt.persistLocked(); err == nil {
		t.Fatal("symlink directory accepted on retry")
	}
	if rt.lock != nil || rt.storageFailure == "" {
		t.Fatal("unsafe directory acquired ownership")
	}
	// Remove only the test symlink; no ownership lock is removed.
	if err := os.Remove(rt.dir); err != nil {
		t.Fatal(err)
	}
	if err := rt.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if rt.storageFailure != "" {
		t.Fatal("repaired directory retained stale error")
	}
}

func TestCensorStorageRecoveryPreservesTerminalFailure(t *testing.T) {
	rt := storageRetryRuntime(t, filepath.Join(t.TempDir(), "censor", "account"))
	rt.failure = "Unable to persist censor state; processing is blocked"
	rt.storageFailure = "Censor storage is unavailable or owned by another process"
	if err := rt.lockStorageLocked(); err != nil {
		t.Fatal(err)
	}
	if rt.storageFailure != "" || rt.failure == "" {
		t.Fatal("storage recovery altered terminal failure")
	}
	if err := rt.commandAdmissionErrorLocked(); err == nil {
		t.Fatal("terminal failure no longer blocks commands")
	}
}

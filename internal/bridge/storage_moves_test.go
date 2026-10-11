package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestStorageMoveRollback(t *testing.T) {
	root := t.TempDir()
	moves := []storageMove{{filepath.Join(root, "old-one"), filepath.Join(root, "new-one")}, {filepath.Join(root, "old-two"), filepath.Join(root, "new-two")}}
	for _, move := range moves {
		if err := os.WriteFile(move.old, []byte(move.old), 0600); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("simulated second rename failure")
	err := moveStorageFiles(moves, func(old, next string) error {
		if old == moves[1].old {
			return failure
		}
		return os.Rename(old, next)
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for _, move := range moves {
		data, err := os.ReadFile(move.old)
		if err != nil || string(data) != move.old {
			t.Fatal("source lost", err)
		}
		if _, err := os.Lstat(move.next); !os.IsNotExist(err) {
			t.Fatal("rollback failed", err)
		}
	}
}

func TestStorageRenameCollision(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux atomic rename")
	}
	root := t.TempDir()
	old := filepath.Join(root, "old")
	next := filepath.Join(root, "next")
	os.WriteFile(old, []byte("source"), 0600)
	os.WriteFile(next, []byte("destination"), 0600)
	if err := renameStorageNoReplace(old, next); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	for path, want := range map[string]string{old: "source", next: "destination"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatal("collision lost data", err)
		}
	}
}

func TestSQLiteActiveMigrationFallback(t *testing.T) {
	_, session := managedPathFixture(t)
	legacy := session + ".yandex-123.tracks-cache.sqlite"
	cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	if err := saveTracksCache(legacy, cache); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLiteCache(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ROLLBACK")
	got, err := tracksStoragePath(session, "123")
	if err != nil || got != legacy {
		t.Fatal(got, err)
	}
}

func TestSQLiteAndJSONMigrationTogether(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux atomic rename")
	}
	_, session := managedPathFixture(t)
	legacy := session + ".yandex-123.tracks-cache.sqlite"
	jsonPath := legacy[:len(legacy)-len("sqlite")] + "json"
	cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	for _, path := range []string{legacy, jsonPath} {
		if err := saveTracksCache(path, cache); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tracksStoragePath(session, "123")
	if err != nil || got == legacy {
		t.Fatal(got, err)
	}
	after, err := os.ReadFile(got[:len(got)-len("sqlite")] + "json")
	if err != nil || string(after) != string(before) {
		t.Fatal("JSON snapshot lost", err)
	}
	for _, path := range []string{legacy, jsonPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("source not moved", err)
		}
	}
	if again, err := tracksStoragePath(session, "123"); err != nil || again != got {
		t.Fatal(again, err)
	}
}

func TestStorageMoveCrossDeviceFailure(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	next := filepath.Join(root, "next")
	os.WriteFile(old, []byte("preserved"), 0600)
	if err := moveStorageFiles([]storageMove{{old, next}}, func(string, string) error { return syscall.EXDEV }); !errors.Is(err, syscall.EXDEV) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(old)
	if err != nil || string(data) != "preserved" {
		t.Fatal("source lost", err)
	}
}

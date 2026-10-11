package bridge

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func managedPathFixture(t *testing.T) (string, string) {
	t.Helper()
	config := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", config)
	case "darwin":
		t.Setenv("HOME", config)
		config = filepath.Join(config, "Library", "Application Support")
	default:
		t.Setenv("XDG_CONFIG_HOME", config)
	}
	return config, filepath.Join(config, "ddetector-go", "session.json")
}

func TestManagedStoragePaths(t *testing.T) {
	config, session := managedPathFixture(t)
	preferred := filepath.Join(config, "DDetector", "cache", "tracks", "yandex-123.tracks-cache.sqlite")
	tracks, err := tracksStoragePath(session, "123")
	if err != nil || tracks != preferred {
		t.Fatal(tracks, err)
	}
	censor, err := censorStoragePath(session, "hash")
	if err != nil || censor != filepath.Join(config, "DDetector", "censor", "hash") {
		t.Fatal(censor, err)
	}
	for _, custom := range []string{session, filepath.Join(t.TempDir(), "custom.json")} {
		tracks, err = tracksStoragePath(custom, "123", true)
		if err != nil || tracks != custom+".yandex-123.tracks-cache.sqlite" {
			t.Fatal(tracks, err)
		}
		censor, err = censorStoragePath(custom, "hash", true)
		if err != nil || censor != filepath.Join(filepath.Dir(custom), "censor", "hash") {
			t.Fatal(censor, err)
		}
	}
}

func TestManagedTrackMigration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("atomic rename implementation is Linux-only")
	}
	for _, suffix := range []string{"sqlite", "json"} {
		t.Run(suffix, func(t *testing.T) {
			config, session := managedPathFixture(t)
			legacy := session + ".yandex-123.tracks-cache.sqlite"
			file := strings.TrimSuffix(legacy, "sqlite") + suffix
			cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), Tracks: []Result{NewDetector().Process(Track{ID: 1, Title: "preserved"}, []Lyric{}, false)}}
			if err := saveTracksCache(file, cache); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tracksStoragePath(session, "123")
			preferred := filepath.Join(config, "DDetector", "cache", "tracks", "yandex-123.tracks-cache.sqlite")
			if err != nil || got != preferred {
				t.Fatal(got, err)
			}
			moved := strings.TrimSuffix(preferred, "sqlite") + suffix
			after, err := os.ReadFile(moved)
			if err != nil || string(after) != string(before) {
				t.Fatal("migration changed bytes", err)
			}
			if _, err := os.Lstat(file); !os.IsNotExist(err) {
				t.Fatal("source not relocated", err)
			}
			saved, err := readTracksCache(got, "u", "source")
			if err != nil || len(saved.Tracks) != 1 || saved.Tracks[0].Title != "preserved" {
				t.Fatal(saved, err)
			}
			again, err := tracksStoragePath(session, "123")
			if err != nil || again != got {
				t.Fatal(again, err)
			}
		})
	}
}

func TestManagedCensorMigration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("atomic rename implementation is Linux-only")
	}
	config, session := managedPathFixture(t)
	legacy := filepath.Join(filepath.Dir(session), "censor", "hash")
	for _, name := range []string{"runtime.json", "engine/queue.json", "engine/queue.lock", "native-publication-test/writer.lock", "native-publication-test/backup"} {
		path := filepath.Join(legacy, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := censorStoragePath(session, "hash")
	if err != nil || got != filepath.Join(config, "DDetector", "censor", "hash") {
		t.Fatal(got, err)
	}
	for _, name := range []string{"runtime.json", "engine/queue.json", "native-publication-test/backup"} {
		data, err := os.ReadFile(filepath.Join(got, name))
		if err != nil || string(data) != name {
			t.Fatal(name, err)
		}
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatal("source not relocated", err)
	}
}

func TestStorageMigrationSafeguards(t *testing.T) {
	for _, kind := range []string{"collision", "symlink", "public", "locked", "sidecar"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			legacy := filepath.Join(root, "old", "cache.json")
			next := filepath.Join(root, "DDetector", "cache", "cache.json")
			if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			wantFallback := false
			switch kind {
			case "collision":
				os.MkdirAll(filepath.Dir(next), 0700)
				os.WriteFile(next, []byte("destination"), 0600)
			case "symlink":
				os.Symlink(filepath.Dir(legacy), filepath.Join(root, "DDetector"))
			case "public":
				os.Mkdir(filepath.Join(root, "DDetector"), 0755)
			case "locked":
				f, err := migrationLock(legacy + ".migration.lock")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				wantFallback = true
			case "sidecar":
				sqlite := strings.TrimSuffix(legacy, "json") + "sqlite"
				os.Rename(legacy, sqlite)
				legacy = sqlite
				next = strings.TrimSuffix(next, "json") + "sqlite"
				os.WriteFile(legacy+"-wal", []byte("journal"), 0600)
				wantFallback = true
			}
			got, err := migrateManagedStorage(next, legacy, false)
			if wantFallback {
				if err != nil || got != legacy {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("unsafe migration accepted", got)
			}
			data, err := os.ReadFile(legacy)
			if err != nil || string(data) != "source" {
				t.Fatal("source lost", err)
			}
			if kind == "collision" {
				data, err := os.ReadFile(next)
				if err != nil || string(data) != "destination" {
					t.Fatal("destination lost", err)
				}
			}
		})
	}
}

func TestCensorMigrationHeldOwnershipLock(t *testing.T) {
	for _, name := range []string{"runtime.lock", "engine/queue.lock", "native-publication-test/writer.lock"} {
		t.Run(name, func(t *testing.T) {
			_, session := managedPathFixture(t)
			legacy := filepath.Join(filepath.Dir(session), "censor", "hash")
			path := filepath.Join(legacy, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			f, err := migrationLock(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := censorStoragePath(session, "hash")
			if err != nil || got != legacy {
				t.Fatal(got, err)
			}
			if _, err := os.Lstat(legacy); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedTracksLease(t *testing.T) {
	_, session := managedPathFixture(t)
	path, release, err := acquireTracksStoragePath(session, "123")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := tracksStoragePath(session, "123"); err == nil {
		t.Fatal("another process bypassed the storage lease")
	}
	release()
	got, err := tracksStoragePath(session, "123")
	if err != nil || got != path {
		t.Fatal(got, err)
	}
}

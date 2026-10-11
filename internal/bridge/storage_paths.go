package bridge

import (
	"fmt"
	"os"
	"path/filepath"
)

// Custom session locations retain their existing storage layout. The OAuth
// session itself remains at its historical default, independently of caches.
func managedStorageRoot(sessionPath string) (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return filepath.Dir(sessionPath), nil
	}
	actual, err := filepath.Abs(sessionPath)
	if err != nil {
		return "", err
	}
	legacy, err := filepath.Abs(filepath.Join(config, "ddetector-go", "session.json"))
	if err != nil {
		return "", err
	}
	if actual != legacy {
		return filepath.Dir(sessionPath), nil
	}
	return filepath.Join(config, "DDetector"), nil
}

func tracksStoragePath(sessionPath, yandexID string, customSession ...bool) (string, error) {
	path, release, err := acquireTracksStoragePath(sessionPath, yandexID, customSession...)
	if release != nil {
		defer release()
	}
	return path, err
}

// Hold the stable, legacy-parent gate for the whole acquisition/checkpoint
// operation, not only the rename. Otherwise another process could migrate a
// fallback cache between its read and its next checkpoint write.
func acquireTracksStoragePath(sessionPath, yandexID string, customSession ...bool) (string, func(), error) {
	root, err := managedStorageRoot(sessionPath)
	if err != nil {
		return "", nil, err
	}
	if root == filepath.Dir(sessionPath) || (len(customSession) > 0 && customSession[0]) {
		path, err := resolveTracksStoragePath(sessionPath, yandexID, customSession...)
		return path, func() {}, err
	}
	if err := ensureMigrationParent(filepath.Dir(sessionPath)); err != nil {
		return "", nil, err
	}
	lock, err := migrationLock(sessionPath + ".yandex-" + yandexID + ".storage.lock")
	if err != nil {
		return "", nil, fmt.Errorf("track storage is busy or unsafe: %w", err)
	}
	path, err := resolveTracksStoragePath(sessionPath, yandexID, customSession...)
	if err != nil {
		lock.Close()
		return "", nil, err
	}
	return path, func() { lock.Close() }, nil
}

func resolveTracksStoragePath(sessionPath, yandexID string, customSession ...bool) (string, error) {
	root, err := managedStorageRoot(sessionPath)
	if err != nil {
		return "", err
	}
	legacy := sessionPath + ".yandex-" + yandexID + ".tracks-cache.sqlite"
	if root == filepath.Dir(sessionPath) || (len(customSession) > 0 && customSession[0]) {
		return legacy, nil
	}
	preferred := filepath.Join(root, "cache", "tracks", "yandex-"+yandexID+".tracks-cache.sqlite")
	legacyJSON := legacy[:len(legacy)-len("sqlite")] + "json"
	preferredJSON := preferred[:len(preferred)-len("sqlite")] + "json"
	hasSQLite, err := storageExists(legacy)
	if err != nil {
		return "", err
	}
	hasJSON, err := storageExists(legacyJSON)
	if err != nil {
		return "", err
	}
	if hasSQLite || hasJSON {
		for _, destination := range []string{preferred, preferredJSON, preferred + "-journal", preferred + "-wal", preferred + "-shm"} {
			if exists, err := storageExists(destination); err != nil {
				return "", err
			} else if exists {
				return "", fmt.Errorf("storage migration collision: %s", destination)
			}
		}
	}
	if hasSQLite {
		var companions []storageMove
		if hasJSON {
			companions = append(companions, storageMove{legacyJSON, preferredJSON})
		}
		return migrateManagedStorage(preferred, legacy, false, companions...)
	}
	path, err := migrateManagedStorage(preferredJSON, legacyJSON, false)
	if err != nil {
		return "", err
	}
	if path == legacyJSON {
		return legacy, nil
	}
	return preferred, nil
}

func censorStoragePath(sessionPath, userHash string, customSession ...bool) (string, error) {
	if len(customSession) > 0 && customSession[0] {
		return filepath.Join(filepath.Dir(sessionPath), "censor", userHash), nil
	}
	root, err := managedStorageRoot(sessionPath)
	if err != nil {
		return "", err
	}
	return migrateManagedStorage(filepath.Join(root, "censor", userHash), filepath.Join(filepath.Dir(sessionPath), "censor", userHash), true)
}

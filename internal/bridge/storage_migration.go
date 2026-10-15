package bridge

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Validate every ancestor without following links. Permissions are required on
// the managed subtree, not on shared ancestors such as /home or /tmp.
func migrationParent(path string, create bool) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := migrationParent(parent, create); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && create {
		if err = os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe storage ancestor: %s", path)
	}
	return nil
}

func ensureMigrationParent(path string) error {
	if err := migrationParent(path, true); err != nil {
		return err
	}
	// Check every application-owned ancestor, but allow shared config roots.
	for ancestor := filepath.Dir(path); filepath.Base(ancestor) != "." && ancestor != filepath.Dir(ancestor); ancestor = filepath.Dir(ancestor) {
		if filepath.Base(ancestor) == "DDetector" {
			for p := filepath.Dir(path); ; p = filepath.Dir(p) {
				info, err := os.Lstat(p)
				if err != nil {
					return err
				}
				if !storagePrivate(p, info, false) {
					return fmt.Errorf("storage parent must be private: %s", p)
				}
				if p == ancestor {
					break
				}
			}
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !storagePrivate(path, info, false) {
		return fmt.Errorf("storage parent must be private: %s", path)
	}
	return nil
}

func migrationLock(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil && !validStorageLockInfo(info) {
		return nil, fmt.Errorf("unsafe storage lock: %s", path)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := openStorageLock(path)
	if err != nil {
		return nil, err
	}
	if err = lockStorageFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Failure leaves the source intact. Unsafe paths and collisions are not a
// reason to silently choose one of two divergent histories.
func migrateManagedStorage(preferred, legacy string, directory bool, companions ...storageMove) (string, error) {
	if preferred == legacy {
		return preferred, nil
	}
	info, err := os.Lstat(legacy)
	if os.IsNotExist(err) {
		return preferred, nil
	}
	if err != nil {
		return "", err
	}
	if err := migrationParent(filepath.Dir(legacy), false); err != nil {
		return "", err
	}
	parent, err := os.Lstat(filepath.Dir(legacy))
	if err != nil {
		return "", err
	}
	if !storagePrivate(filepath.Dir(legacy), parent, false) {
		return "", errors.New("legacy storage parent must be private")
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) || !storagePrivate(legacy, info, false) {
		return "", fmt.Errorf("unsafe legacy storage: %s", legacy)
	}
	if err := migrationParent(filepath.Dir(preferred), false); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := os.Lstat(preferred); err == nil {
		return "", fmt.Errorf("storage migration collision: %s and %s", legacy, preferred)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	for _, move := range companions {
		info, err := os.Lstat(move.old)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || !storagePrivate(move.old, info, true) {
			return "", fmt.Errorf("unsafe legacy companion: %s", move.old)
		}
		if exists, err := storageExists(move.next); err != nil {
			return "", err
		} else if exists {
			return "", fmt.Errorf("storage migration collision: %s", move.next)
		}
	}
	fallback := func(err error) (string, error) {
		log.Printf("Storage migration %s -> %s failed; retaining legacy storage: %v", legacy, preferred, err)
		return legacy, nil
	}
	var locks []*os.File
	defer func() {
		for _, f := range locks {
			f.Close()
		}
	}()
	if directory {
		err = filepath.WalkDir(legacy, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			stat, err := entry.Info()
			if err != nil {
				return err
			}
			// Tool-generated work files may be 0644; their private parent
			// directories are the boundary. Preserve bytes and modes rather
			// than deleting interrupted audio work during migration.
			if (!stat.IsDir() && !stat.Mode().IsRegular()) || (stat.IsDir() && !storagePrivate(path, stat, false)) {
				return fmt.Errorf("unsafe censor storage entry: %s", path)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		paths := []string{filepath.Join(legacy, "runtime.lock")}
		err = filepath.WalkDir(legacy, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && (entry.Name() == "queue.lock" || entry.Name() == "writer.lock") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		for _, path := range paths {
			f, err := migrationLock(path)
			if err != nil {
				return fallback(err)
			}
			locks = append(locks, f)
		}
	} else {
		f, err := migrationLock(legacy + ".migration.lock")
		if err != nil {
			return fallback(err)
		}
		locks = append(locks, f)
		for _, move := range companions {
			f, err := migrationLock(move.old + ".migration.lock")
			if err != nil {
				return fallback(err)
			}
			locks = append(locks, f)
		}
		if strings.HasSuffix(legacy, ".sqlite") {
			for _, suffix := range []string{"-journal", "-wal", "-shm"} {
				if _, err := os.Lstat(legacy + suffix); err == nil {
					return fallback(errors.New("SQLite sidecar requires recovery before migration"))
				} else if !os.IsNotExist(err) {
					return "", err
				}
			}
			// Use SQLite's own lock as well: an advisory file lock alone does not
			// protect against an already-open legacy database connection.
			db, err := sql.Open("sqlite", legacy)
			if err != nil {
				return fallback(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if _, err = db.Exec("PRAGMA busy_timeout=0; BEGIN EXCLUSIVE"); err != nil {
				return fallback(err)
			}
			defer db.Exec("ROLLBACK")
		}
	}
	if err := ensureMigrationParent(filepath.Dir(preferred)); err != nil {
		// A symlink or public directory must not be bypassed by fallback.
		if statErr := migrationParent(filepath.Dir(preferred), false); statErr == nil || !os.IsNotExist(statErr) {
			return "", err
		}
		return fallback(err)
	}
	moves := append([]storageMove{{legacy, preferred}}, companions...)
	if err := moveStorageFiles(moves, renameStorageNoReplace); err != nil {
		var rollback *storageRollbackError
		if errors.Is(err, os.ErrExist) || errors.As(err, &rollback) {
			return "", err
		}
		return fallback(err)
	}
	// The move is already committed. A directory-sync failure must not select
	// the now-absent legacy path; report it while keeping the new location.
	seen := map[string]bool{}
	for _, move := range moves {
		for _, path := range []string{filepath.Dir(move.old), filepath.Dir(move.next)} {
			if seen[path] {
				continue
			}
			seen[path] = true
			dir, err := os.Open(path)
			if err == nil {
				err = dir.Sync()
				dir.Close()
			}
			if err != nil {
				log.Printf("Storage migrated, but could not sync directory %s: %v", path, err)
			}
		}
	}
	return preferred, nil
}

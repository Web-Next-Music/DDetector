package bridge

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const censorJobLimit = 8 << 20

var ErrCensorCapacity = errors.New("censor result exceeds storage capacity")

// Encode one job at a time: no serialized full-history buffer is needed.
func encodeCensorState(w io.Writer, state censorDiskState) error {
	uid, err := json.Marshal(state.UserID)
	if err != nil {
		return err
	}
	settings, err := json.Marshal(state.Settings)
	if err != nil {
		return err
	}
	repository, err := json.Marshal(state.RepositoryPath)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(w, "{\"user_id\":"+string(uid)+",\"repository_path\":"+string(repository)+",\"settings\":"+string(settings)+",\"jobs\":["); err != nil {
		return err
	}
	for n, job := range state.Jobs {
		if n > 0 {
			if _, err = io.WriteString(w, ","); err != nil {
				return err
			}
		}
		data, err := json.Marshal(job)
		if err != nil {
			return err
		}
		if _, err = w.Write(data); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "]}\n")
	return err
}

type censorCapacityWriter struct{ remaining int64 }

func (w *censorCapacityWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrCensorCapacity
	}
	w.remaining -= int64(len(p))
	return len(p), nil
}
func censorStateAdmission(state censorDiskState) error {
	return censorAdmissionLimit(state, censorStateLimit)
}
func censorAdmissionLimit(state censorDiskState, limit int64) error {
	return encodeCensorState(&censorCapacityWriter{remaining: limit}, state)
}

func checkCensorDir(dir string, create bool) error {
	if create {
		if e := os.MkdirAll(dir, 0700); e != nil {
			return e
		}
	}
	info, e := os.Lstat(dir)
	if os.IsNotExist(e) && !create {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.IsDir() || !storagePrivate(dir, info, false) {
		return errors.New("censor directory must be private and non-symlink")
	}
	return nil
}

// Runtime state and the engine queue have independent ownership locks. The
// runtime lock also covers review/publication while no engine worker is active.
func (rt *censorRuntime) lockStorageLocked() error {
	if rt.lock != nil {
		return nil
	}
	if e := checkCensorDir(filepath.Dir(rt.dir), true); e != nil {
		return e
	}
	if e := checkCensorDir(rt.dir, true); e != nil {
		return e
	}
	path := filepath.Join(rt.dir, "runtime.lock")
	if info, e := os.Lstat(path); e == nil && !validStorageLockInfo(info) {
		return errors.New("invalid censor runtime lock")
	}
	f, e := openStorageLock(path)
	if e != nil {
		return e
	}
	if e = lockStorageFile(f); e != nil {
		f.Close()
		return ErrCensorConflict
	}
	data, e := readPrivateFile(filepath.Join(rt.dir, "runtime.json"), censorStateLimit, true)
	if e != nil && !os.IsNotExist(e) {
		f.Close()
		return e
	}
	if sha256.Sum256(data) != rt.stateVersion {
		f.Close()
		return errors.New("censor runtime changed in another process; reopen before modifying")
	}
	rt.lock = f
	return nil
}
func (rt *censorRuntime) releaseStorage() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.lock == nil {
		return nil
	}
	e := rt.lock.Close()
	rt.lock = nil
	return e
}

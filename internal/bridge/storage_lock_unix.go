//go:build !windows

package bridge

import (
	"errors"
	"os"
	"syscall"
)

func openStorageLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !validStorageLockInfo(info) {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("storage lock must be a private regular file")
	}
	return f, nil
}
func lockStorageFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func validStorageLockInfo(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600
}

// Permission failures and other unknown states must never authorize stale-lock removal.
func storageProcessGone(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == syscall.ESRCH
}

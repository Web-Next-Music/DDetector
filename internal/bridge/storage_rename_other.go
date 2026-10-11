//go:build !linux && !windows

package bridge

import "errors"

func renameStorageNoReplace(old, next string) error {
	return errors.New("atomic no-replace storage migration is unavailable on this platform")
}

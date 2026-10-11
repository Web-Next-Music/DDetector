package bridge

import "golang.org/x/sys/windows"

func renameStorageNoReplace(old, next string) error {
	source, err := windows.UTF16PtrFromString(old)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(next)
	if err != nil {
		return err
	}
	// No MOVEFILE_REPLACE_EXISTING or COPY_ALLOWED: collisions and cross-volume
	// moves leave the source intact instead of replacing or partially copying it.
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_WRITE_THROUGH)
}

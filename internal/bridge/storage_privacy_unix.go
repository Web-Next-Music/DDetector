//go:build !windows

package bridge

import "os"

func storagePrivate(path string, info os.FileInfo, exact bool) bool {
	return privateFileModeOK(info, exact)
}

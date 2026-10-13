//go:build !windows

package bridge

import (
	"fmt"
	"os"
)

func guardPrivateDirectory(dir string) (func(), error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private file directory must be non-symlink and private (0700)")
	}
	return func() {}, nil
}
func securePrivateFile(f *os.File) error { return f.Chmod(0600) }
func privateFileModeOK(info os.FileInfo, exact bool) bool {
	return info.Mode().Perm()&0077 == 0 && (!exact || info.Mode().Perm() == 0600)
}
func openPrivateFile(path string) (*os.File, func(), error) {
	f, err := os.Open(path)
	return f, func() {}, err
}
func replacePrivateFile(old, next string) error { return os.Rename(old, next) }
func syncPrivateDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

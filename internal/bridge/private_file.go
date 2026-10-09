package bridge

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxTracksCacheBytes int64 = 256 << 20
const maxSessionBytes int64 = 1 << 20

type budgetWriter struct {
	w         io.Writer
	remaining int64
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("private JSON file too large")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// A failed encode or size check never replaces the previous private file.
func writePrivateFile(path, prefix string, limit int64, encode func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private file directory must be non-symlink and private (0700)")
	}
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		err = encode(&budgetWriter{w: f, remaining: limit})
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func readPrivateFile(path string, limit int64, exactMode bool) ([]byte, error) {
	link, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !link.Mode().IsRegular() {
		return nil, fmt.Errorf("private file must be regular and non-symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(link, info) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || (exactMode && info.Mode().Perm() != 0600) {
		return nil, fmt.Errorf("private file must be regular, non-symlink, mode 0600")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("private JSON file too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("private JSON file too large")
	}
	return data, nil
}

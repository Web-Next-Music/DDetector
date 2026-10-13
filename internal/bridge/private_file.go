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
		return fmt.Errorf("create private file directory: %w", err)
	}
	release, err := guardPrivateDirectory(dir)
	if err != nil {
		return fmt.Errorf("guard private file directory: %w", err)
	}
	defer release()
	f, err := createPrivateTemp(dir, prefix)
	if err != nil {
		return fmt.Errorf("create private temporary file: %w", err)
	}
	defer os.Remove(f.Name())
	if err = securePrivateFile(f); err != nil {
		err = fmt.Errorf("secure private temporary file: %w", err)
	} else if err = encode(&budgetWriter{w: f, remaining: limit}); err != nil {
		err = fmt.Errorf("encode private file: %w", err)
	}
	if err == nil {
		if err = f.Sync(); err != nil {
			err = fmt.Errorf("sync private temporary file: %w", err)
		}
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close private temporary file: %w", closeErr)
	}
	if err = replacePrivateFile(f.Name(), path); err != nil {
		return fmt.Errorf("replace private file: %w", err)
	}
	if err := syncPrivateDirectory(dir); err != nil {
		return fmt.Errorf("sync private file directory: %w", err)
	}
	return nil
}

func readPrivateFile(path string, limit int64, exactMode bool) ([]byte, error) {
	link, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !link.Mode().IsRegular() {
		return nil, fmt.Errorf("private file must be regular and non-symlink")
	}
	f, release, err := openPrivateFile(path)
	if err != nil {
		return nil, err
	}
	defer release()
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(link, info) || !info.Mode().IsRegular() || !privateFileModeOK(info, exactMode) {
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

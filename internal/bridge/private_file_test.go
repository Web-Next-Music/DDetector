package bridge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateFileEncodeErrorStage(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("encoder failed")
	err := writePrivateFile(filepath.Join(dir, "session.json"), ".session-", 100, func(io.Writer) error { return failure })
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "encode private file:") {
		t.Fatalf("encode error lost stage or cause: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed encode left %d files", len(entries))
	}
}

func TestPrivateFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	write := func(value string, limit int64) error {
		return writePrivateFile(path, ".session-", limit, func(w io.Writer) error { _, err := io.WriteString(w, value); return err })
	}
	for _, value := range []string{"first", "replacement"} {
		if err := write(value, 100); err != nil {
			t.Fatal(err)
		}
		for _, exact := range []bool{false, true} {
			data, err := readPrivateFile(path, 100, exact)
			if err != nil || string(data) != value {
				t.Fatalf("read = %q, %v", data, err)
			}
		}
	}
	if err := write("oversized", 2); err == nil {
		t.Fatal("accepted oversized write")
	}
	data, err := readPrivateFile(path, 100, true)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("lost previous file: %q, %v", data, err)
	}
	if _, err := readPrivateFile(path, 2, true); err == nil {
		t.Fatal("accepted oversized read")
	}
}

func TestPrivateFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readPrivateFile(link, 100, true); err == nil {
		t.Fatal("accepted symlink")
	}
}

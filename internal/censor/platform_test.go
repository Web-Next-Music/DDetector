package censor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.lock")
	open := func() *os.File {
		t.Helper()
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { file.Close() })
		return file
	}
	first, second := open(), open()
	if err := lockQueueFile(first); err != nil {
		t.Fatal(err)
	}
	if err := lockQueueFile(second); err == nil {
		t.Fatal("second handle acquired exclusive queue lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lockQueueFile(second); err != nil {
		t.Fatalf("lock not released on close: %v", err)
	}
}

func TestBoundCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBoundCommandHelper$")
	cmd.Env = append(os.Environ(), "CENSOR_COMMAND_HELPER=1")
	boundCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("cancelled command did not terminate promptly")
	}
}

func TestBoundCommandHelper(t *testing.T) {
	if os.Getenv("CENSOR_COMMAND_HELPER") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

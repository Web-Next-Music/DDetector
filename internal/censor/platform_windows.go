//go:build windows

package censor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

func boundCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// Resolve the system executable rather than trusting PATH or invoking a shell.
		systemDir, err := windows.GetSystemDirectory()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			kill := exec.CommandContext(ctx, filepath.Join(systemDir, "taskkill.exe"), "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
			kill.WaitDelay = time.Second
			if err = kill.Run(); err == nil {
				return nil
			}
		}
		// Still stop the parent if taskkill is unavailable or fails. Descendant
		// termination is best-effort, unlike containment in a Windows job object.
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second
}

func lockQueueFile(file *os.File) error {
	// Closing the handle releases this exclusive, nonblocking byte-range lock.
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func syncQueueDirectory(string) error {
	// Windows does not support syncing a directory handle via os.File.Sync.
	// persist has already synced and closed the state file before renaming it.
	return nil
}

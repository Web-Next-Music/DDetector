package censor

import (
	"os"
	"path/filepath"
)

func audioWorkRoot(stateDir string) (string, error) {
	if stateDir != "" {
		return filepath.Join(stateDir, "work"), nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "DDetector", "temp", "audio"), nil
}

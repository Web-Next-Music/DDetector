package censor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAudioWorkRoot(t *testing.T) {
	config, err := os.UserConfigDir()
	if err != nil {
		t.Skip(err)
	}
	got, err := audioWorkRoot("")
	if err != nil || got != filepath.Join(config, "DDetector", "temp", "audio") {
		t.Fatal(got, err)
	}
	explicit := t.TempDir()
	got, err = audioWorkRoot(explicit)
	if err != nil || got != filepath.Join(explicit, "work") {
		t.Fatal(got, err)
	}
}

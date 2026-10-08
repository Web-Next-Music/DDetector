package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type User struct {
	ID       string         `json:"id"`
	Email    string         `json:"email,omitempty"`
	Metadata map[string]any `json:"user_metadata,omitempty"`
}
type Session struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresAt int64  `json:"expires_at"`
	ExpiresIn int64  `json:"expires_in"`
	User      User   `json:"user"`
}

func (s *Session) normalize() error {
	if s.Access == "" || s.Refresh == "" || s.User.ID == "" {
		return fmt.Errorf("incomplete auth session")
	}
	if s.ExpiresAt == 0 {
		if s.ExpiresIn <= 0 {
			return fmt.Errorf("missing token expiry")
		}
		s.ExpiresAt = time.Now().Unix() + s.ExpiresIn
	}
	return nil
}
func ReadSession(path string) (*Session, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	link, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || link.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("session file must be regular, non-symlink, mode 0600")
	}
	var s Session
	if err = json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid saved session")
	}
	if err = s.normalize(); err != nil {
		return nil, err
	}
	return &s, nil
}
func SaveSession(path string, s *Session) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("session directory must be private (0700)")
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(s)
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
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type Identity struct {
	Provider string         `json:"provider"`
	Data     map[string]any `json:"identity_data,omitempty"`
}

type User struct {
	Identities []Identity     `json:"identities,omitempty"`
	ID         string         `json:"id"`
	Email      string         `json:"email,omitempty"`
	Metadata   map[string]any `json:"user_metadata,omitempty"`
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
	data, err := readPrivateFile(path, maxSessionBytes, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid saved session")
	}
	if err = s.normalize(); err != nil {
		return nil, err
	}
	return &s, nil
}
func SaveSession(path string, s *Session) error {
	return writePrivateFile(path, ".session-*", maxSessionBytes, func(w io.Writer) error {
		return json.NewEncoder(w).Encode(s)
	})
}

package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Session struct {
	Token        string          `json:"token"`
	APIKey       string          `json:"apiKey,omitempty"`
	EnabledTools map[string]bool `json:"enabledTools,omitempty"`
}

var ErrNotLoggedIn = errors.New("not logged in — run: nan auth login")

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nan"), nil
}

func Load() (*Session, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(d, "session.json"))
	if os.IsNotExist(err) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var s Session
	return &s, json.Unmarshal(data, &s)
}

func Save(s *Session) error {
	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "session.json"), data, 0o600)
}

func Delete() error {
	d, err := dir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(d, "session.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

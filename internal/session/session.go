package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// Path is where the session actually lives, for the About tab to show. It was
// a "~/.config/nan/session.json" typed into the renderer, which is not a path
// on Windows and is not where anything is: a member told to look there finds
// nothing, and the tilde is not something Explorer resolves.
func Path() string {
	d, err := dir()
	if err != nil {
		return filepath.Join(".config", "nan", "session.json")
	}
	return filepath.Join(d, "session.json")
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

// Save writes the session, which holds both the token and the API key, so how
// it is written matters as much as where.
//
// os.WriteFile applies its mode only where it CREATES the file: a session.json
// left behind by an older version, or by a restore that widened it, kept
// whatever mode it already had and the 0o600 here did nothing at all. Writing
// to a temp file created 0600 and renaming it over the top is the only version
// of this that ends with the mode we asked for whatever was there before - and
// it never leaves a half-written session.json behind either, which used to
// read on the next run as not being logged in.
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

	tmp, err := os.CreateTemp(d, ".session-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // does nothing once the rename has taken it

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// On Windows the mode bits are not the mechanism - the file inherits the
	// ACL of the profile directory - so a filesystem that will not take the
	// chmod is no reason to fail the login that is being saved.
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(d, "session.json"))
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

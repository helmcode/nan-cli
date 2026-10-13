// Package session stores the signed-in session on disk.
//
// One file, one member, no database. What makes it worth its own package is
// not the JSON — it is that the file holds a credential, and so how it is
// written matters as much as what is in it.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nxssie/nan-cli/internal/build"
)

// Session is what survives between runs.
//
// The JSON is a contract with every copy of this CLI already installed: the
// file is read by whichever version runs next, so a field is never renamed
// and a new one is always omitempty.
type Session struct {
	// Token is the session the platform issued. It is the credential:
	// anyone holding it is the member until it expires. Empty after the
	// platform refused it, while the fields below are kept.
	Token string `json:"token"`

	// APIKey is the member's inference key, as pasted into Setup. It is a
	// SECOND credential, and it outlives the session: it is kept because the
	// tools it configures need it again, to rewrite a config and to take it
	// back out on sign-out. The file is 0600 for the session's sake and this
	// rides along.
	APIKey string `json:"apiKey,omitempty"`

	// PlatformToken is a nan_pat_ token saved with `nan auth login
	// --api-token`. It is kept apart from APIKey because Setup writes APIKey
	// into every tool it configures, and the inference API refuses platform
	// tokens.
	PlatformToken string `json:"platformToken,omitempty"`

	// EnabledTools is which tools are ticked, so Setup opens showing what
	// is true rather than an empty list. A tool missing from it is ticked.
	EnabledTools map[string]bool `json:"enabledTools,omitempty"`
}

// TokenEnvVar is the environment variable `nan run` and `nan runs` read a
// platform token or an API key from. Nothing writes it to disk.
const TokenEnvVar = "NAN_TOKEN"

// ErrNotLoggedIn is what every command hits on a fresh machine.
var ErrNotLoggedIn = errors.New("not logged in - run: " + build.Name + " auth login")

// SignedIn reports whether there is a session to send.
func (s *Session) SignedIn() bool { return s != nil && s.Token != "" }

// dir is ~/.config/nan on every platform, Windows included, because that is
// where every earlier version put it: moving it would sign everybody out on
// the update and strand the key they pasted.
func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not work out where to keep the session: %w", err)
	}
	return filepath.Join(home, ".config", build.Name), nil
}

// Path is where the session lives, for the About screen and for error
// messages to name. It falls back to a relative path rather than failing,
// because a screen that cannot say where the file is should still render.
func Path() string {
	d, err := dir()
	if err != nil {
		return filepath.Join(".config", build.Name, "session.json")
	}
	return filepath.Join(d, "session.json")
}

// Load reads the session. A missing file is ErrNotLoggedIn rather than an
// I/O error: it is the normal state of a machine nobody has signed in on.
//
// A file without a token is still returned: it is a session the platform
// refused, and the key and the ticked tools in it are still the member's.
// Whoever needs the session checks SignedIn.
func Load() (*Session, error) {
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt file is recoverable by signing in again, and saying so
		// is more use than the parser's offset.
		return nil, fmt.Errorf("the saved session is unreadable (%s) - run: %s auth login", Path(), build.Name)
	}
	return &s, nil
}

// LoadOrEmpty is for the dashboard, which opens on a machine nobody has
// signed in on and offers to fix that, rather than refusing to start.
func LoadOrEmpty() *Session {
	s, err := Load()
	if err != nil {
		return &Session{}
	}
	return s
}

// Save writes the session.
//
// Via a temp file and a rename, for two reasons that both cost a release
// somewhere. os.WriteFile applies its mode only where it CREATES the file,
// so a session.json left behind by an older version — or restored by a
// backup tool that widened it — keeps whatever mode it already had and the
// 0600 does nothing at all. And a write interrupted halfway leaves a
// truncated file, which reads on the next run as not being signed in.
// Creating a new file 0600 and renaming it over the top is the only version
// of this that ends with the mode we asked for whatever was there before,
// and never leaves a half-written one behind.
func Save(s *Session) error {
	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	// G117 flags this for marshalling a field that looks like a secret. It
	// is one, and writing it here is the point: the file is created 0600 and
	// re-tightened on every save by the rename below.
	data, err := json.Marshal(s) //nolint:gosec // G117: deliberate, see the Session doc
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(d, ".session-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once the rename has taken it

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// On Windows the mode bits are not the mechanism — the file inherits
	// the ACL of the profile directory — so a filesystem that will not take
	// the chmod is no reason to fail the sign-in being saved.
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, Path())
}

// Delete removes the session. A file that is already gone is success: that
// is the state `logout` is trying to reach.
func Delete() error {
	err := os.Remove(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

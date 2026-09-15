package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows
	return home
}

// session.json holds the session token and the API key. os.WriteFile applies
// its mode only where it CREATES the file, so a session.json left behind wide
// open - by an older version, by a restore, by a copy out of a backup - kept
// whatever mode it had and went on holding both secrets in the clear.
func TestSavingTightensASessionThatWasWideOpen(t *testing.T) {
	tempHome(t)
	if err := Save(&Session{Token: "a-token"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Save(&Session{Token: "a-token", APIKey: "a-key"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// The mode bits are not the mechanism there - the file inherits the
		// ACL of the profile directory it sits in.
		return
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("session.json is mode %04o, readable by more than the member", mode)
	}
}

// And it still round-trips, temp file and rename included.
func TestSaveAndLoad(t *testing.T) {
	tempHome(t)
	want := &Session{Token: "a-token", APIKey: "a-key", EnabledTools: map[string]bool{"Codex": false}}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != want.Token || got.APIKey != want.APIKey || got.EnabledTools["Codex"] {
		t.Errorf("loaded %+v, want %+v", got, want)
	}

	// Nothing half-written left beside it: the write goes to a temp file in
	// the same directory and is renamed over the top.
	entries, err := os.ReadDir(filepath.Dir(Path()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "session.json" {
			t.Errorf("left %s behind in the config directory", e.Name())
		}
	}
}

func TestLoadWithoutASessionSaysSo(t *testing.T) {
	tempHome(t)
	if _, err := Load(); err != ErrNotLoggedIn {
		t.Errorf("Load() on a clean machine = %v, want %v", err, ErrNotLoggedIn)
	}
}

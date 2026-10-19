package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/session"
)

func withTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func executeKeyPrint(t *testing.T) (string, error) {
	t.Helper()
	var stdout strings.Builder
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{"key", "print"})
	err := rootCmd.Execute()
	return stdout.String(), err
}

func saveSession(t *testing.T, s *session.Session) {
	t.Helper()
	if err := session.Save(s); err != nil {
		t.Fatal(err)
	}
}

func TestKeyPrintWritesOnlyTheKey(t *testing.T) {
	withTestHome(t)
	saveSession(t, &session.Session{Token: "a-token", APIKey: "an-api-key"})

	stdout, err := executeKeyPrint(t)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "an-api-key\n" {
		t.Errorf("stdout = %q, want %q", stdout, "an-api-key\n")
	}
}

func TestKeyPrintFailsWithoutSession(t *testing.T) {
	withTestHome(t)

	stdout, err := executeKeyPrint(t)
	if err == nil {
		t.Fatal("printed a key with no session")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestKeyPrintFailsOnEmptyKey(t *testing.T) {
	withTestHome(t)
	saveSession(t, &session.Session{Token: "a-token"})

	stdout, err := executeKeyPrint(t)
	if err == nil {
		t.Fatal("printed an empty key as if it were one")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestKeyPrintErrorNeverContainsTheKey(t *testing.T) {
	withTestHome(t)
	key := "the-secret-key-value"

	dir := filepath.Dir(session.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A session file the CLI cannot parse is the one error path where a real
	// key is in the file, so it is the one where a message could echo it.
	raw := []byte(`{"apiKey":"` + key + `"`)
	if err := os.WriteFile(session.Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := executeKeyPrint(t)
	if err == nil {
		t.Fatal("expected the unreadable session to fail")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error %q contains the key", err.Error())
	}
}

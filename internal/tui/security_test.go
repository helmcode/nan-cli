// Package tui, and what a member's API key is allowed to touch.
//
// Everything here is one question asked five ways: once the key is in this
// process, where can it end up? On disk with the wrong mode, in an argument
// list, in an error rendered on screen, or left behind in a tool's config
// after the member believes they have signed out. Each of those was true at
// some point, which is why each of them is a test.
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tools' configs are theirs, not ours: by the time `nan` writes a key into
// one it usually exists already, created by the tool itself and commonly 0644.
// os.WriteFile does not change the mode of a file it did not create, so the
// key went into a world-readable file and the 0o600 in the call did nothing.
func TestWritingAKeyTightensAConfigThatWasWideOpen(t *testing.T) {
	home := toolHome(t)
	for _, path := range toolPaths(home) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// As the tool would have left it.
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on a clean run: %v", failed)
	}
	for name, path := range toolPaths(home) {
		content := readFile(t, path)
		if name == "Codex" || name == "Pi" {
			// The two exceptions: their configs carry a reference to
			// `nan key print` instead of the key, and that reference is what
			// says they were configured at all.
			if strings.Contains(content, testKey) {
				t.Errorf("%s carries the literal key", name)
				continue
			}
			want := `args = ["key", "print"]`
			if name == "Pi" {
				want = `key print`
			}
			if !strings.Contains(content, want) {
				t.Errorf("%s was not configured with the key command reference", name)
				continue
			}
		} else {
			if !strings.Contains(content, testKey) {
				t.Errorf("%s was not configured at all", name)
				continue
			}
		}
		assertNotWorldReadable(t, path)
	}
}

// The tools that are already pointing at the cluster are the ones that have
// held a key the longest, and the writers leave them alone: there is nothing
// to change. Their mode is still whatever it was when the key went in, which
// for a config the tool created itself is world-readable.
func TestConfiguringTightensAToolThatNeedsNothingWritten(t *testing.T) {
	home := toolHome(t)
	paths := toolPaths(home)
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on a clean run: %v", failed)
	}
	// As an older version of this CLI would have left them.
	for _, path := range paths {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A second run has nothing to write, and has to fix them anyway.
	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on the second run: %v", failed)
	}
	for name, path := range paths {
		content := readFile(t, path)
		if name == "Codex" || name == "Pi" {
			// The key was never in these files; the command reference is what
			// has to survive the second run untouched.
			if strings.Contains(content, testKey) {
				t.Errorf("%s carries the literal key after the second run", name)
			}
			want := `args = ["key", "print"]`
			if name == "Pi" {
				want = `key print`
			}
			if !strings.Contains(content, want) {
				t.Errorf("%s lost its key command reference on the second run", name)
			}
		} else if !strings.Contains(content, testKey) {
			t.Errorf("%s lost its key on the second run", name)
		}
		assertNotWorldReadable(t, path)
	}
}

// A config this CLI cannot parse is the one case where it must not write: the
// readers used to discard the unmarshal error, read the file as nothing, and
// write it again from scratch - taking every other provider in it, and their
// keys, with them.
func TestAConfigThatDoesNotParseIsLeftExactlyAsItIs(t *testing.T) {
	const theirs = `{"provider": {"anthropic": {"apiKey": "theirs"}},,,`

	for _, c := range []struct {
		name  string
		file  string
		write func(path, key string) error
	}{
		{"Factory AI", "settings.json", writeFactoryConfig},
		{"OpenCode", "opencode.json", writeOpencodeConfig},
		{"Pi", "models.json", writePiConfig},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := tempConfig(t, c.file)
			if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
				t.Fatal(err)
			}

			err := c.write(path, testKey)
			if err == nil {
				t.Fatal("a config that does not parse was written over without a word")
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("the error carries the key: %v", err)
			}
			if got := readFile(t, path); got != theirs {
				t.Errorf("the file was rewritten:\n%s", got)
			}
		})
	}
}

// Signing out deletes session.json, which holds the token and the key. It used
// to stop there, and the key was still in every tool configured from the
// panel - working, billing, and out of reach of anyone who thought signing out
// was the end of it.
func TestSigningOutTakesTheKeyOutOfTheTools(t *testing.T) {
	home := toolHome(t)
	for _, path := range toolPaths(home) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on a clean run: %v", failed)
	}

	removed, failed := RemoveNanFromTools()
	if len(failed) != 0 {
		t.Fatalf("removal failed: %v", failed)
	}
	if len(removed) == 0 {
		t.Fatal("removal found nothing to take out, having just written five")
	}
	for name, path := range toolPaths(home) {
		if strings.Contains(readFile(t, path), testKey) {
			t.Errorf("%s still carries the key after signing out", name)
		}
	}
	// Codex's config never held the key, but the command reference is ours
	// all the same: after signing out it must not survive, or Codex keeps
	// resolving a key that no longer exists.
	codexContent := readFile(t, toolPaths(home)["Codex"])
	for _, ours := range []string{"[model_providers.nan]", "[model_providers.nan.auth]", `["model_providers.nan"]`} {
		if strings.Contains(codexContent, ours) {
			t.Errorf("Codex still carries %s after signing out", ours)
		}
	}
	if strings.Contains(readFile(t, hermesEnvPath(filepath.Join(home, "hermes"))), testKey) {
		t.Error("Hermes still carries the key after signing out")
	}
}

// The reason a failed `hermes config set` prints. The value is the key.
func TestHermesFailuresDoNotPrintTheValue(t *testing.T) {
	err := hermesConfigError([]string{"set", "model.api_key", testKey}, "unknown key\n")
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("the key is in the error the panel renders: %v", err)
	}
	for _, want := range []string{"set", "model.api_key", "unknown key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error no longer says %q: %v", want, err)
		}
	}
}

// ── the shared harness ───────────────────────────────────────────────────────

// A home directory of this test's own, Hermes included, so nothing here
// reaches the machine it is running on.
func toolHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows
	hermesDir := filepath.Join(home, "hermes")
	t.Setenv("HERMES_HOME", hermesDir)
	if err := os.MkdirAll(hermesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	recordHermes(t) // no hermes process is spawned, here or anywhere
	return home
}

func toolPaths(home string) map[string]string {
	return map[string]string{
		"Factory AI": filepath.Join(home, ".factory", "settings.json"),
		"OpenCode":   filepath.Join(home, ".config", "opencode", "opencode.json"),
		"Pi":         filepath.Join(home, ".pi", "agent", "models.json"),
		"Codex":      filepath.Join(home, ".codex", "config.toml"),
	}
}

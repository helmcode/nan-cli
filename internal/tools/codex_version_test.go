package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	catalog "github.com/helmcode/nan-cli/internal/models"
)

func withCodexVersion(t *testing.T, version string) {
	t.Helper()
	original := codexVersion
	codexVersion = func() string { return version }
	t.Cleanup(func() { codexVersion = original })
}

func TestCodexSubAgentSwitchFollowsTheVersion(t *testing.T) {
	cases := map[string]string{
		"codex-cli 0.169.0": "[agents]\nenabled = false\n",
		"codex-cli 0.168.0": "[agents]\nenabled = false\n",
		"codex-cli 1.0.0":   "[agents]\nenabled = false\n",
		"codex-cli 0.167.2": "[features]\nmulti_agent = false\n",
		"codex-cli 0.155.0": "[features]\nmulti_agent = false\n",
		"":                  "[agents]\nenabled = false\n",
		"garbage":           "[agents]\nenabled = false\n",
		"codex-cli x.y.z":   "[agents]\nenabled = false\n",
	}
	for version, want := range cases {
		if use, _ := codexSubAgentSwitches(version); use.off() != want {
			t.Errorf("%q: got %q, want %q", version, use.off(), want)
		}
	}
}

func TestCodexProfileGolden(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	m := catalog.ChatModels()[0]
	got := readFile(t, filepath.Join(home, codexProfileName(m.ID)+".config.toml"))
	want := fmt.Sprintf(`# Written by the NaN CLI. Disconnecting Codex removes it.
model = %q
model_provider = "nan"
model_context_window = %d
model_catalog_json = %q
web_search = "disabled"

[agents]
enabled = false
`, m.ID, m.Context, codexCatalogPath(home))
	if got != want {
		t.Errorf("profile:\ngot:\n%s\nwant:\n%s", got, want)
	}
	assertCodexTOML(t, got)
}

// A Codex older than 0.168 keeps the key it understands in its profiles.
func TestCodexProfilesOnAnOlderCodexKeepMultiAgent(t *testing.T) {
	withCodexVersion(t, "codex-cli 0.159.0")
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	m := catalog.ChatModels()[0]
	body := readFile(t, filepath.Join(filepath.Dir(path), codexProfileName(m.ID)+".config.toml"))
	if !strings.Contains(body, "[features]\nmulti_agent = false") || strings.Contains(body, "[agents]") {
		t.Errorf("profile for Codex 0.159:\n%s", body)
	}
}

// A fake `codex` first on PATH, so the real probe runs without the real tool.
func fakeCodex(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script is not a codex on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { //nolint:gosec // G306: a test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCodexVersionProbeReadsTheVersion(t *testing.T) {
	fakeCodex(t, `echo "codex-cli 0.167.2"`)
	if got := probeCodexVersion(5 * time.Second); got != "codex-cli 0.167.2" {
		t.Errorf("got %q", got)
	}
}

func TestCodexVersionProbeWithNoCodexIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := probeCodexVersion(5 * time.Second); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestCodexVersionProbeThatFailsIsUnknown(t *testing.T) {
	fakeCodex(t, `echo "codex-cli 0.150.0"; exit 3`)
	if got := probeCodexVersion(5 * time.Second); got != "" {
		t.Errorf("a failed codex still gave a version: %q", got)
	}
}

// npm's codex is a wrapper: the process the probe starts is killed at the
// timeout, but the child it left holds the output pipe. The probe has to come
// back anyway - a member waiting on a setup screen for a hung codex is the
// failure this guards against.
func TestCodexVersionProbeThatHangsGivesUpAtTheTimeout(t *testing.T) {
	fakeCodex(t, `sleep 30`)
	start := time.Now()
	got := probeCodexVersion(200 * time.Millisecond)
	if got != "" {
		t.Errorf("got %q", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the probe waited %v on a hung codex", elapsed)
	}
}

// Codex moved between 0.167 and 0.169 on the same machine: each configure
// leaves exactly the switch that Codex reads in the profiles.
func TestCodexProfilesFollowAnUpgradeAndADowngrade(t *testing.T) {
	path := tempConfig(t, "config.toml")
	m := catalog.ChatModels()[0]
	profile := filepath.Join(filepath.Dir(path), codexProfileName(m.ID)+".config.toml")
	for _, s := range []struct{ version, has, hasNot string }{
		{"codex-cli 0.167.0", "multi_agent", "[agents]"},
		{"codex-cli 0.169.0", "[agents]", "multi_agent"},
		{"codex-cli 0.167.0", "multi_agent", "[agents]"},
	} {
		withCodexVersion(t, s.version)
		if err := writeCodexConfig(path, testKey); err != nil {
			t.Fatal(err)
		}
		body := readFile(t, profile)
		if !strings.Contains(body, s.has) || strings.Contains(body, s.hasNot) {
			t.Errorf("%s:\n%s", s.version, body)
		}
	}
}

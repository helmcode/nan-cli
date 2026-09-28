package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nxssie/nan-cli/internal/session"
)

// ── loading ──────────────────────────────────────────────────────────────────

// Every way of not having a usable setting lands on auto, because the theme is
// cosmetic: a file somebody is midway through editing, or a value from a
// newer build, must never be a reason the panel refuses to open.
func TestMissingCorruptOrUnknownThemeLoadsAuto(t *testing.T) {
	if got := loadThemeFrom(tempConfig(t, "settings.json")); got != ThemeAuto {
		t.Errorf("a missing file = %q, want auto", got)
	}

	for _, c := range []struct{ name, body string }{
		{"empty", ""},
		{"whitespace", "   \n"},
		{"corrupt", `{"theme":"dark",`},
		{"unknown value", `{"theme":"solarized"}`},
		{"not a string", `{"theme":5}`},
	} {
		path := tempConfig(t, c.name+".json")
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadThemeFrom(path); got != ThemeAuto {
			t.Errorf("%s: = %q, want auto", c.name, got)
		}
	}
}

// A valid value is read back, whichever of the two explicit modes it names.
func TestKnownThemeLoads(t *testing.T) {
	for _, want := range []ThemeMode{ThemeDark, ThemeLight} {
		path := tempConfig(t, string(want)+".json")
		if err := os.WriteFile(path, []byte(`{"theme":"`+string(want)+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadThemeFrom(path); got != want {
			t.Errorf("loadThemeFrom = %q, want %q", got, want)
		}
	}
}

// ── saving ───────────────────────────────────────────────────────────────────

// settings.json is shared, the same way the tool configs are: saving our key
// must not take somebody else's with it.
func TestSavingTheThemeKeepsWhatItDoesNotOwn(t *testing.T) {
	path := tempConfig(t, "settings.json")
	existing := `{"theme":"dark","editor":"vim","nested":{"keep":true}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadThemeFrom(path); got != ThemeDark {
		t.Fatalf("loadThemeFrom = %q, want dark", got)
	}

	if err := saveThemeTo(path, ThemeLight); err != nil {
		t.Fatal(err)
	}
	if got := loadThemeFrom(path); got != ThemeLight {
		t.Errorf("after saving light, loadThemeFrom = %q", got)
	}

	cfg := readJSON(t, path)
	if cfg["editor"] != "vim" {
		t.Errorf("editor = %v, a foreign key was lost", cfg["editor"])
	}
	nested, ok := cfg["nested"].(map[string]any)
	if !ok || nested["keep"] != true {
		t.Errorf("nested = %v, a foreign key was lost", cfg["nested"])
	}
}

// A file that cannot be parsed is the one case where writing would do real
// damage - it would replace the whole file with the one key we understand - so
// the save gives up instead, leaving the member's bytes exactly as they are.
func TestSavingOverAFileThatDoesNotParseChangesNothing(t *testing.T) {
	path := tempConfig(t, "settings.json")
	broken := `{"theme":"dark","editor":"vim"`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveThemeTo(path, ThemeLight); err == nil {
		t.Fatal("saving over a file that does not parse was reported as success")
	}
	if got := readFile(t, path); got != broken {
		t.Errorf("the file was rewritten:\n%s", got)
	}
}

// A save with no file to read writes one from nothing, which is the first run.
func TestSavingTheThemeCreatesTheFile(t *testing.T) {
	path := tempConfig(t, "settings.json")
	if err := saveThemeTo(path, ThemeDark); err != nil {
		t.Fatal(err)
	}
	if got := loadThemeFrom(path); got != ThemeDark {
		t.Errorf("a freshly written file reads back as %q, want dark", got)
	}
}

// ── cycling and applying ─────────────────────────────────────────────────────

func TestNextThemeCyclesAutoDarkLight(t *testing.T) {
	got := []ThemeMode{ThemeAuto}
	m := ThemeAuto
	for i := 0; i < 3; i++ {
		m = nextTheme(m)
		got = append(got, m)
	}
	want := []ThemeMode{ThemeAuto, ThemeDark, ThemeLight, ThemeAuto}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// applyTheme only ever moves the renderer's background flag, and auto moves
// nothing. lipgloss resolves every AdaptiveColor from that flag on each render,
// which is what lets a mode change re-colour the whole panel without a style
// being rebuilt - and why auto can leave detection alone and still be correct.
func TestApplyThemeForcesTheRendererAndAutoLeavesIt(t *testing.T) {
	applyTheme(ThemeDark)
	if !lipgloss.HasDarkBackground() {
		t.Error("dark did not pin the renderer to a dark background")
	}

	applyTheme(ThemeLight)
	if lipgloss.HasDarkBackground() {
		t.Error("light did not pin the renderer to a light background")
	}

	// Auto is a no-op, so whichever explicit flag is in place survives it.
	lipgloss.SetHasDarkBackground(true)
	applyTheme(ThemeAuto)
	if !lipgloss.HasDarkBackground() {
		t.Error("auto cleared an explicit dark flag, so it is not the no-op it claims")
	}
	lipgloss.SetHasDarkBackground(false)
	applyTheme(ThemeAuto)
	if lipgloss.HasDarkBackground() {
		t.Error("auto set an explicit dark flag, so it is not the no-op it claims")
	}
}

// ── the key ──────────────────────────────────────────────────────────────────

// `t` advances the mode and writes it, so the next run opens in the mode this
// one was left in.
func TestThemeKeyCyclesAndPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	m := newModel(nil, &session.Session{}, ThemeAuto)
	press := func(m model) model {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
		return updated.(model)
	}

	if m.theme != ThemeAuto {
		t.Fatalf("a fresh model starts at %q, want auto", m.theme)
	}
	m = press(m)
	if m.theme != ThemeDark {
		t.Fatalf("first press = %q, want dark", m.theme)
	}
	if got := loadTheme(); got != ThemeDark {
		t.Errorf("the press did not persist: the file holds %q", got)
	}
	if m = press(m); m.theme != ThemeLight {
		t.Errorf("second press = %q, want light", m.theme)
	}
	if m = press(m); m.theme != ThemeAuto {
		t.Errorf("third press = %q, want auto", m.theme)
	}
}

// The theme is the panel's key: not one the help overlay swallows, and not one
// pressed while the guided setup owns the keyboard.
func TestThemeKeyIsGuardedLikeTheOtherGlobalKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	m := newModel(nil, &session.Session{}, ThemeAuto)
	m.showHelp = true
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if got := updated.(model).theme; got != ThemeAuto {
		t.Errorf("t changed the theme to %q with the help overlay open", got)
	}

	m = newModel(nil, &session.Session{}, ThemeAuto)
	m.wizard = wizardEmail
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if got := updated.(model).theme; got != ThemeAuto {
		t.Errorf("t changed the theme to %q during the guided setup", got)
	}
}

// ── the About row ────────────────────────────────────────────────────────────

// Auto's outcome is not written anywhere else, so the row says which side the
// terminal resolved to; the explicit modes just name themselves.
func TestAboutShowsTheThemeAndWhatAutoResolvedTo(t *testing.T) {
	l := newLayout(100, 30)

	if out := (model{theme: ThemeLight}).renderAbout(l); !strings.Contains(out, "Theme:") || !strings.Contains(out, "light") {
		t.Errorf("About does not show a light theme:\n%s", out)
	}

	lipgloss.SetHasDarkBackground(true)
	if out := (model{theme: ThemeAuto}).renderAbout(l); !strings.Contains(out, "auto (terminal looks dark)") {
		t.Errorf("About does not say what auto resolved to:\n%s", out)
	}
	if out := (model{theme: ThemeDark}).renderAbout(l); !strings.Contains(out, "dark") {
		t.Errorf("About does not show a dark theme:\n%s", out)
	}
}

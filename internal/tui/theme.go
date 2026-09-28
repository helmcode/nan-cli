package tui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
)

// ── palette ───────────────────────────────────────────────────────────────────

// The palette is a set of roles, not a paint box. Every token is an AdaptiveColor
// pair: the Light side is the value a light terminal wants, the Dark side the one
// a dark terminal wants, and lipgloss picks between them from the detected
// background.
//
// The roles, in declaration order:
//   - cCyan is the primary accent, used for the current step and highlighted numbers.
//   - cBlue is the deeper accent that grounds the wordmark and selected frames.
//   - cBlueDim is the soft accent fill behind a selected block.
//   - cGray is the readable middle of the text hierarchy, for secondary copy.
//   - cDimGray is the muted tier for hints and inactive rows.
//   - cWhite is the high-contrast tier for the loudest labels and prompts.
//   - cText is the default reading colour for long runs of prose.
//   - cRed is the error tone, kept apart from the accent so failures never read as
//     ordinary emphasis.
var (
	cCyan    = lipgloss.AdaptiveColor{Light: "#6d28d9", Dark: "#a78bfa"}
	cBlue    = lipgloss.AdaptiveColor{Light: "#5b21b6", Dark: "#8b5cf6"}
	cBlueDim = lipgloss.AdaptiveColor{Light: "#ddd6fe", Dark: "#2e1065"}
	cGray    = lipgloss.AdaptiveColor{Light: "#52525b", Dark: "#71717a"}
	cDimGray = lipgloss.AdaptiveColor{Light: "#a1a1aa", Dark: "#52525b"}
	cWhite   = lipgloss.AdaptiveColor{Light: "#18181b", Dark: "#ffffff"}
	cText    = lipgloss.AdaptiveColor{Light: "#374151", Dark: "#cbd5e1"}
	cRed     = lipgloss.AdaptiveColor{Light: "#dc2626", Dark: "#ef4444"}

	// modelColors are the categorical colours for per-model usage rows: the set is
	// fixed so each model keeps a stable colour of its own. Each is a pair now,
	// because the dark one is unreadable on a light background: the Light side is
	// a deeper tone of the same hue, so the five stay distinguishable on white
	// and a model keeps its identity whichever side the terminal resolves to.
	modelColors = []lipgloss.AdaptiveColor{
		{Light: "#7c3aed", Dark: "#8b5cf6"},
		{Light: "#c026d3", Dark: "#a78bfa"},
		{Light: "#059669", Dark: "#10B981"},
		{Light: "#b45309", Dark: "#F59E0B"},
		{Light: "#dc2626", Dark: "#EF4444"},
	}
)

// ── theme mode ────────────────────────────────────────────────────────────────

// ThemeMode is which side of every AdaptiveColor the panel draws. Auto is the
// default and the only one that does not override the terminal: it lets the
// renderer's own background detection decide.
type ThemeMode string

const (
	ThemeAuto  ThemeMode = "auto"
	ThemeDark  ThemeMode = "dark"
	ThemeLight ThemeMode = "light"
)

// nextTheme is the cycle the `t` key walks: auto, then dark, then light, then
// back to auto.
func nextTheme(m ThemeMode) ThemeMode {
	switch m {
	case ThemeDark:
		return ThemeLight
	case ThemeLight:
		return ThemeAuto
	default:
		return ThemeDark
	}
}

// applyTheme pins every AdaptiveColor to one side by setting the renderer's
// background flag, which is the only thing a theme setting can do: each pair is
// resolved per render from that flag, so the next frame is drawn in the new
// colours with no style rebuilt.
//
// Auto is deliberately a no-op. It leaves lipgloss's own termenv detection -
// including the dark fallback it uses when the terminal cannot be asked, which
// is what an SSH session or a multiplexer gets - exactly as it works today.
// That is what "auto" means, and overriding it here would take the choice away
// from the terminal.
func applyTheme(m ThemeMode) {
	switch m {
	case ThemeDark:
		lipgloss.SetHasDarkBackground(true)
	case ThemeLight:
		lipgloss.SetHasDarkBackground(false)
	}
}

// settingsPath is where the theme lives, mirroring session.Path(): the same
// config directory, and the same relative fallback for a home directory that
// cannot be resolved.
func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "nan", "settings.json")
	}
	return filepath.Join(home, ".config", "nan", "settings.json")
}

func loadTheme() ThemeMode { return loadThemeFrom(settingsPath()) }

func saveTheme(m ThemeMode) error { return saveThemeTo(settingsPath(), m) }

// loadThemeFrom reads the saved theme and answers auto for every way of not
// having one: a missing file, an empty one, JSON that does not parse, a key
// that is not a string, or a value this build does not know. The setting is
// cosmetic - the panel works in the terminal's own colours without it - so a
// damaged file is never a reason to fail the TUI, and only a known string is
// read back.
func loadThemeFrom(path string) ThemeMode {
	data, err := os.ReadFile(path)
	if err != nil {
		return ThemeAuto
	}
	var stored struct {
		Theme string `json:"theme"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return ThemeAuto
	}
	switch ThemeMode(stored.Theme) {
	case ThemeDark:
		return ThemeDark
	case ThemeLight:
		return ThemeLight
	}
	return ThemeAuto
}

// saveThemeTo writes the theme key back into the settings file, leaving every
// other key exactly where it was.
//
// It is a read-modify-write for the same reason the tool configs are: this file
// is shared and one day holds a setting this CLI does not know about, and
// writing our single key from scratch would take that setting with it. The
// reader is readJSONConfig, whose whole point is refusing to hand back a file it
// could not parse - so a settings.json somebody is midway through editing is
// left alone rather than flattened, and the error travels out to the caller.
func saveThemeTo(path string, m ThemeMode) error {
	if m != ThemeDark && m != ThemeLight {
		m = ThemeAuto // only ever one of the three constants, whatever came in
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	cfg["theme"] = string(m)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(path, data)
}

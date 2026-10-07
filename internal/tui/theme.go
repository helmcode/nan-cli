package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"

	"github.com/nxssie/nan-cli/internal/session"
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
// config directory, resolved by session.Dir() so settings.json always lands
// beside session.json, and the same relative fallback for a home directory that
// cannot be resolved.
func settingsPath() string {
	d, err := session.Dir()
	if err != nil {
		return filepath.Join(".config", "nan", "settings.json")
	}
	return filepath.Join(d, "settings.json")
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

// readSettingsConfig reads settings.json with the same refusal semantics as
// readJSONConfig - a missing or empty file is an empty map, and a file that
// will not parse is refused rather than guessed at - but it decodes every
// number as a json.Number instead of a float64.
//
// settings.json is shared, and the CLI is not its only writer: a foreign key
// holds whatever its owner put there. json.Unmarshal turns every JSON number
// into a float64, so an integer past 2^53 comes back rounded, and the
// read-modify-write that only meant to set "theme" rewrites somebody else's
// number on the way through. A json.Number keeps the literal digits, so the
// round-trip writes every foreign value back exactly as it was read.
func readSettingsConfig(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// A decoder reads a stream, so on its own it would stop at the first value
	// and silently leave a second one unread; json.Unmarshal rejects that as
	// trailing data. Decoding a second time is how the stream form gets the
	// same strictness back: one value in the file, and the next read is EOF.
	dec := json.NewDecoder(f)
	dec.UseNumber()
	var cfg map[string]any
	if err := dec.Decode(&cfg); err != nil {
		if err == io.EOF {
			// An empty or whitespace-only file is one with no setting in it,
			// not one that cannot be parsed.
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("%s does not parse as JSON, so it was left exactly as it is: %w",
			filepath.Base(path), err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("trailing data after the first JSON value")
		}
		return nil, fmt.Errorf("%s does not parse as JSON, so it was left exactly as it is: %w",
			filepath.Base(path), err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

// saveThemeTo writes the theme key back into the settings file, leaving every
// other key exactly where it was.
//
// It is a read-modify-write for the same reason the tool configs are: this file
// is shared and one day holds a setting this CLI does not know about, and
// writing our single key from scratch would take that setting with it. The
// reader is readSettingsConfig, whose whole point is refusing to hand back a
// file it could not parse - so a settings.json somebody is midway through
// editing is left alone rather than flattened, and the error travels out to the
// caller.
func saveThemeTo(path string, m ThemeMode) error {
	if m != ThemeDark && m != ThemeLight {
		m = ThemeAuto // only ever one of the three constants, whatever came in
	}
	cfg, err := readSettingsConfig(path)
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

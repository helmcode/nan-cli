package tui

import "github.com/charmbracelet/lipgloss"

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
	// fixed so each model keeps a stable colour of its own.
	modelColors = []lipgloss.Color{
		lipgloss.Color("#8b5cf6"),
		lipgloss.Color("#a78bfa"),
		lipgloss.Color("#10B981"),
		lipgloss.Color("#F59E0B"),
		lipgloss.Color("#EF4444"),
	}
)

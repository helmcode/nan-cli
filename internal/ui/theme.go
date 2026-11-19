// Package ui holds everything this CLI paints: the NaN palette, the
// wordmark and the mascot, and the small set of components that the plain
// commands and the dashboard both draw with.
//
// The values come from nan.builders' own tokens: one violet accent for
// fills, a lighter violet for text, and the traffic-light colours kept for
// the state of data. Everything else is neutral, and adapts to a light or a
// dark terminal.
package ui

import (
	"image/color"
	"os"

	"charm.land/lipgloss/v2"
)

// The NaN violet. `#7D39EB` is --color-violet on nan.builders, the single
// accent of the system, and its token file says it is for FILLS: bars, the
// wordmark, the mascot. Text in the accent uses `#9B6BF0`, --color-violet-2,
// which is the one that clears contrast against a dark background, and a
// darker violet on a light one.
const (
	accentHex          = "#7D39EB"
	accentTextHex      = "#9B6BF0"
	accentTextLightHex = "#6D28D9"
	// The edge of the wordmark's letters, darker than the body so the block
	// letters read with depth instead of as a flat slab.
	accentDeepHex = "#3B1578"
)

// The traffic light, for the state of data and nothing decorative.
const (
	statusRedHex    = "#EF4444"
	statusYellowHex = "#F59E0B"
	statusGreenHex  = "#10B981"
)

// Theme is the resolved palette for one terminal.
//
// The accent holds its ground and the neutrals adapt: plenty of people run a
// terminal on white, and a CLI drawn only for black is unreadable for them.
type Theme struct {
	Accent     color.Color // fills, bars, the wordmark, the mascot. Never small text.
	AccentDeep color.Color // the wordmark's edges
	AccentText color.Color // anything accented that is read rather than seen.
	Text       color.Color // primary
	TextDim    color.Color // secondary — descriptions, subtitles
	TextFaint  color.Color // tertiary — the legible floor, nothing below it
	Mono       color.Color // technical values: ids, keys, numbers
	Line       color.Color // hairlines and separators
	Surface    color.Color // panels the terminal paints behind content

	Red    color.Color
	Yellow color.Color
	Green  color.Color
}

// NewTheme resolves the palette for a dark or a light terminal.
//
// The neutrals are the zinc and slate greys nan.builders draws with, and the
// hairlines and surfaces are the deep violet it uses for panels, so the
// accent is never the only thing on screen that says NaN.
func NewTheme(dark bool) Theme {
	pick := lipgloss.LightDark(dark)
	hex := func(light, dark string) color.Color {
		return pick(lipgloss.Color(light), lipgloss.Color(dark))
	}
	return Theme{
		Accent:     lipgloss.Color(accentHex),
		AccentDeep: lipgloss.Color(accentDeepHex),
		AccentText: hex(accentTextLightHex, accentTextHex),
		Text:       hex("#18181B", "#FFFFFF"),
		TextDim:    hex("#52525B", "#A1A1AA"),
		TextFaint:  hex("#71717A", "#71717A"),
		Mono:       hex("#374151", "#CBD5E1"),
		Line:       hex("#DDD6FE", "#2E1065"),
		Surface:    hex("#F5F3FF", "#1A0B33"),
		Red:        lipgloss.Color(statusRedHex),
		Yellow:     lipgloss.Color(statusYellowHex),
		Green:      lipgloss.Color(statusGreenHex),
	}
}

// theme is resolved once, on first use, because asking the terminal about its
// background is a round trip on some emulators and the answer cannot change
// under a running process.
var theme *Theme

// Current is the palette for this terminal.
func Current() Theme {
	if theme == nil {
		t := NewTheme(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
		theme = &t
	}
	return *theme
}

// SetTheme forces a palette. Tests use it to render both backgrounds without
// a terminal attached; nothing else should need it.
func SetTheme(t Theme) { theme = &t }

package cli

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/fang"

	"github.com/nxssie/nan-cli/internal/ui"
)

// ColorScheme is Helmcode's palette, applied to the help and error output
// that fang renders.
//
// Without this, `helmcode --help` is the only screen in the program that is
// not Helmcode's — Charm's default scheme, in Charm's colours, under a
// Helmcode banner. Branding the parts we wrote and leaving the framework's
// default underneath is how a CLI ends up looking assembled rather than
// designed.
func ColorScheme(pick lipgloss.LightDarkFunc) fang.ColorScheme {
	// fang hands us its light/dark decision as a picker rather than as a
	// bool, so the theme is resolved by asking it to choose between black
	// and white and looking at which came back. Reading fang's answer
	// rather than asking the terminal ourselves is what keeps the help and
	// the dashboard from disagreeing about the background.
	t := ui.NewTheme(isDark(pick))

	return fang.ColorScheme{
		Base:           t.Text,
		Title:          t.AccentText,
		Description:    t.TextDim,
		Codeblock:      t.Surface,
		Program:        t.Text,
		DimmedArgument: t.TextFaint,
		Comment:        t.TextFaint,
		Flag:           t.AccentText,
		FlagDefault:    t.TextFaint,
		Command:        t.Text,
		QuotedString:   t.Mono,
		Argument:       t.Mono,
		Help:           t.TextFaint,
		Dash:           t.Line,
		// The error header is the one filled moment here: indigo behind
		// near-black, the same treatment the dashboard gives the open tab.
		ErrorHeader:  [2]color.Color{lipgloss.Color("#0a0a0a"), t.Accent},
		ErrorDetails: t.Red,
	}
}

// isDark asks a picker which way it leans.
func isDark(pick lipgloss.LightDarkFunc) bool {
	r, _, _, _ := pick(lipgloss.Color("#000000"), lipgloss.Color("#ffffff")).RGBA()
	return r > 0x7fff
}

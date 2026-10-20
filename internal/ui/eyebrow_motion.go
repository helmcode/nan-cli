package ui

import (
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// EyebrowTyped draws the eyebrow partway through being typed out: the two
// slashes first, then the letters, with a cursor ahead of them that blinks
// while it types and goes once it is done. For a dashboard that animates
// what it has just put on screen.
//
// progress runs from 0 to 1, driven by a spring; frame counts animation
// ticks, for the blink. Chosen over a sweep of light, letters closing up
// and a decode, side by side in the dashboard: typing is the one that
// belongs to a terminal.
func EyebrowTyped(s string, progress float64, frame int) string {
	if progress >= 1 {
		return Eyebrow(s)
	}
	t := Current()
	accent := lipgloss.NewStyle().Foreground(t.AccentText)
	label := []rune(track(strings.ToUpper(s)))

	// A spring spends its last stretch creeping the final percent to 1,
	// which would leave the last letter missing for most of the settling.
	// Running a fifth ahead finishes the typing as the spring lands.
	p := math.Min(math.Max(progress, 0)*1.2, 1)
	n := int(p * float64(len(label)+2))
	slashes := min(n, 2)
	typed := min(max(n-2, 0), len(label))

	out := accent.Render(strings.Repeat("/", slashes))
	if slashes == 2 {
		out += " " + accent.Render(string(label[:typed]))
	}
	if frame/6%2 == 0 {
		out += lipgloss.NewStyle().Foreground(t.Accent).Render("▌")
	}
	return out
}

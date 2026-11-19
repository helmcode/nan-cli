package pulse

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/helmcode/nan-cli/internal/ui"
)

// The sparkline: thirty days of tokens across the width of the screen. The
// same chart as the Helmcode CLI's, so the two dashboards read alike.
//
// Two rows of eighth blocks, so sixteen steps of height rather than eight,
// which is the difference between a shape and a staircase at this size.
// Coloured by height along the brand ramp, faint to accent, so the busy
// days are the bright ones and the eye finds the peak before the label
// says where it was.
//
// reveal grows it in: every bar is drawn at reveal times its height, and
// the dashboard springs reveal from 0 to 1 when the numbers arrive.

var eighths = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

const sparkRows = 2

// sparkline draws values across width columns, sparkRows high.
func sparkline(values []float64, width int, reveal float64) []string {
	rows := make([]string, sparkRows)
	if len(values) == 0 || width <= 0 {
		for i := range rows {
			rows[i] = strings.Repeat(" ", max(width, 0))
		}
		return rows
	}

	// Each day gets the same whole number of columns, and the line is
	// centred in what is left, so no day is drawn wider than another.
	per := max(width/len(values), 1)
	shown := values
	if len(values)*per > width {
		shown = values[len(values)-width:]
	}
	peak := 0.0
	for _, v := range shown {
		peak = max(peak, v)
	}

	t := ui.Current()
	ramp := lipgloss.Blend1D(9, t.Line, t.Accent, t.AccentText)
	steps := sparkRows * 8

	cells := make([]string, sparkRows)
	for _, v := range shown {
		level := 0
		if peak > 0 {
			level = int(v / peak * reveal * float64(steps))
		}
		if v > 0 && level == 0 && reveal >= 1 {
			level = 1 // a day with any traffic is never drawn as none
		}
		style := lipgloss.NewStyle().Foreground(ramp[min(level*len(ramp)/(steps+1), len(ramp)-1)])
		for r := range sparkRows {
			// Row 0 is the top. It fills only once the bottom row is full.
			floor := (sparkRows - 1 - r) * 8
			fill := min(max(level-floor, 0), 8)
			cells[r] += style.Render(strings.Repeat(string(eighths[fill]), per))
		}
	}
	pad := width - len(shown)*per
	for r := range sparkRows {
		rows[r] = strings.Repeat(" ", pad/2) + cells[r] + strings.Repeat(" ", pad-pad/2)
	}
	return rows
}

// peakDay is the index of the busiest day, or -1.
func peakDay(values []float64) int {
	best := -1
	for i, v := range values {
		if best < 0 || v > values[best] {
			best = i
		}
	}
	return best
}

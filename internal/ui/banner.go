package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// The banner: the mascot, NaN in block letters, and the few words that say
// what this is.
//
// The glyphs of the wordmark are two different things and are coloured as
// such. `█` is the body of the letter, in the accent, because a block letter
// is a fill; `╔ ╗ ╚ ╝ ═ ║` are its edge, in a deeper violet, which is what
// gives the letterform depth on a terminal instead of a flat slab.

// The middle letter sits one row lower than the two N's, which is what makes
// it read as the lowercase 'a' of NaN instead of a third capital.
var wordmark = []string{
	"███╗   ██╗          ███╗   ██╗",
	"████╗  ██║  █████╗  ████╗  ██║",
	"██╔██╗ ██║ ██╔══██╗ ██╔██╗ ██║",
	"██║╚██╗██║ ███████║ ██║╚██╗██║",
	"██║ ╚████║ ██╔══██║ ██║ ╚████║",
	"╚═╝  ╚═══╝ ╚═╝  ╚═╝ ╚═╝  ╚═══╝",
}

const (
	wordmarkWidth = 30
	wordmarkRows  = 6
	sideGap       = 6
	// Between the mascot and the wordmark. Wider than it looks like it needs
	// to be, because the mascot is a solid shape and the letters are not: the
	// same gap reads as crowding.
	mascotGap = 4
)

// BannerRoom is the terminal height from which the mascot is worth its rows.
// Home is a landing screen: one that needs scrolling on a 24-row terminal
// has already failed at the only thing it does, and the mascot is the part
// that goes.
const BannerRoom = 24 + MascotHeight - wordmarkRows + 1

// paint colours one wordmark row.
func paint(row string) string {
	body := lipgloss.NewStyle().Foreground(Current().Accent)
	edge := lipgloss.NewStyle().Foreground(Current().AccentDeep)

	var b strings.Builder
	for _, r := range row {
		switch r {
		case '█':
			b.WriteString(body.Render(string(r)))
		case ' ':
			b.WriteString(" ")
		default:
			b.WriteString(edge.Render(string(r)))
		}
	}
	return b.String()
}

func brandName() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(Current().AccentText).Bold(true)
}

// bannerSide is what goes beside the wordmark, row by row, so the text sits
// on the letters' baseline instead of floating above them.
func bannerSide(version string) []string {
	name := brandName()
	dim := lipgloss.NewStyle().Foreground(Current().TextDim)
	return []string{
		Faint("welcome to"),
		name.Render("nan.builders"),
		Faint("cloud CLI · v" + version),
		"",
		dim.Render("created by ") + name.Render("@Nxssie"),
		dim.Render("maintained by ") + name.Render("Helmcode Team"),
	}
}

// BannerWidth is the columns a full banner needs, the indent aside.
func BannerWidth(version string) int {
	return MascotWidth + mascotGap + BannerWidthCompact(version)
}

// BannerWidthCompact is the same without the mascot, which is what a narrow
// terminal gets.
func BannerWidthCompact(version string) int {
	return wordmarkWidth + sideGap + widest(bannerSide(version))
}

// widest measures the longest of a set of rendered lines, ignoring the escape
// sequences in them.
func widest(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := lipgloss.Width(l); n > w {
			w = n
		}
	}
	return w
}

// Banner draws the mascot, then the wordmark, then the text. The mascot is
// eleven rows and the wordmark six, so the letters are centred against it
// rather than sat on its top edge, which reads as two unrelated drawings.
//
// With withMascot false it is the wordmark and the text alone: on a terminal
// too narrow or too short for all three, the mascot is the part that goes.
func Banner(version, indent string, mood Mood, withMascot bool) string {
	side := bannerSide(version)
	row := func(i int) string {
		line := paint(wordmark[i])
		if side[i] != "" {
			line += strings.Repeat(" ", sideGap) + side[i]
		}
		return line
	}

	var b strings.Builder
	if !withMascot {
		for i := range wordmark {
			b.WriteString(strings.TrimRight(indent+row(i), " ") + "\n")
		}
		return b.String()
	}

	art := Mascot(mood)
	top := (len(art) - len(wordmark)) / 2
	for i := range art {
		line := indent + art[i] + strings.Repeat(" ", mascotGap)
		if w := i - top; w >= 0 && w < len(wordmark) {
			line += row(w)
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return b.String()
}

// PlainBanner is for a terminal too narrow for the wordmark: the name and
// what this is, in two lines.
func PlainBanner(version, indent string) string {
	return indent + brandName().Render("nan.builders") + "\n" +
		indent + Faint("cloud CLI · v"+version) + "\n"
}

// CompactBanner is one line: what a command that prints a result and exits
// can afford to spend on saying whose result it is.
func CompactBanner(version string) string {
	return brandName().Render("nan.builders") + "  " + Faint("cloud CLI · v"+version) + "\n"
}

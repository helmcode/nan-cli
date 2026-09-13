package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The welcome wordmark: NaN in block letters, in the brand's own violet.
//
// The glyphs are two different things and are coloured as such. `█` is the body
// of the letter; `╔ ╗ ╚ ╝ ═ ║` are its edge, and drawing those darker is what
// gives the letterform depth on a terminal instead of a flat slab.
//
// `#7D39EB` is `--color-violet` on nan.builders, the single accent of the
// system, and that token file says it is for FILLS - which a block letter is.
// `#9B6BF0` is `--color-violet-2`, the one that carries text.
const (
	brandViolet     = "#7D39EB"
	brandVioletText = "#9B6BF0"
	brandVioletDeep = "#3B1578"
)

// The middle letter sits one row lower than the two N's, which is what makes it
// read as the lowercase 'a' of NaN instead of a third capital.
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
	sideGap       = 6
)

func styles() (name, dim, text lipgloss.Style) {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(brandVioletText)).Bold(true),
		lipgloss.NewStyle().Foreground(cDimGray),
		lipgloss.NewStyle().Foreground(cGray)
}

// paint colours one wordmark row: the body of the letter in the accent, its
// edge in a deeper violet.
func paint(row string) string {
	body := lipgloss.NewStyle().Foreground(lipgloss.Color(brandViolet))
	edge := lipgloss.NewStyle().Foreground(lipgloss.Color(brandVioletDeep))

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

// What goes beside the wordmark, row by row, so the text sits on the letters'
// baseline instead of floating above them.
func bannerSide() []string {
	name, dim, text := styles()
	return []string{
		dim.Render("welcome to"),
		name.Render("nan.builders"),
		dim.Render("cloud CLI · v" + Version),
		"",
		text.Render("created by ") + name.Render("@Nxssie"),
		text.Render("maintained by ") + name.Render("Helmcode Team"),
	}
}

// BannerWidth is the columns the whole thing needs, indent aside.
var BannerWidth = func() int {
	widest := 0
	for _, line := range bannerSide() {
		if w := lipgloss.Width(line); w > widest {
			widest = w
		}
	}
	return wordmarkWidth + sideGap + widest
}()

// Banner draws the wordmark with the text beside it. Six rows, no frame: it is
// what goes inside a tab that has other things to say.
func Banner(indent string) string {
	side := bannerSide()
	var b strings.Builder
	for i, row := range wordmark {
		line := indent + paint(row)
		if side[i] != "" {
			line += strings.Repeat(" ", sideGap) + side[i]
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// Welcome draws the banner inside corner brackets, the way a command that
// prints once and exits can afford to. The frame is measured from the content
// rather than from a constant, so it keeps hugging it when the text changes.
func Welcome(indent string) string {
	corner := lipgloss.NewStyle().Foreground(lipgloss.Color(brandVioletDeep))

	// The corner piece is two columns and then one of air before the content,
	// which is what `pad` is; the far edge gets the same, so the frame sits the
	// same distance from the text on both sides.
	const pad = 3
	inner := BannerWidth + 2
	edge := func(left, right string) string {
		return indent + corner.Render(left) + strings.Repeat(" ", inner) + corner.Render(right)
	}

	var b strings.Builder
	b.WriteString(edge("┌─", "─┐") + "\n\n")
	b.WriteString(Banner(indent + strings.Repeat(" ", pad)))
	b.WriteString("\n" + edge("└─", "─┘") + "\n")
	return b.String()
}

// WelcomeStacked is the other arrangement: a small label above the wordmark and
// the name under it, the way the Copilot CLI lays its welcome out, instead of
// everything sitting to the right.
func WelcomeStacked(indent string) string {
	corner := lipgloss.NewStyle().Foreground(lipgloss.Color(brandVioletDeep))
	name, dim, text := styles()

	const pad = 3
	inner := wordmarkWidth + 2
	body := indent + strings.Repeat(" ", pad)
	edge := func(left, right string) string {
		return indent + corner.Render(left) + strings.Repeat(" ", inner) + corner.Render(right)
	}

	// Right-aligned under the art, like the reference's "Command-line
	// interface" under the logo.
	under := name.Render("nan.builders") + dim.Render(" · cloud CLI v"+Version)
	gap := wordmarkWidth - lipgloss.Width(under)
	if gap < 0 {
		gap = 0
	}

	var b strings.Builder
	b.WriteString(edge("┌─", "─┐") + "\n\n")
	b.WriteString(body + dim.Render("welcome to") + "\n")
	for _, row := range wordmark {
		b.WriteString(body + paint(row) + "\n")
	}
	b.WriteString(body + strings.Repeat(" ", gap) + under + "\n\n")
	b.WriteString(body + text.Render("created by ") + name.Render("@Nxssie") + "\n")
	b.WriteString(body + text.Render("maintained by ") + name.Render("Helmcode Team") + "\n")
	b.WriteString("\n" + edge("└─", "─┘") + "\n")
	return b.String()
}

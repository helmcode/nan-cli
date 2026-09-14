package tui

import (
	"fmt"
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
	// Between the mascot and the wordmark. Wider than sideGap because the
	// mascot is a solid shape and the letters are not: the same gap reads as
	// crowding.
	mascotGap = 4
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
	return MascotWidth + mascotGap + wordmarkWidth + sideGap + widest
}()

// BannerWidthPlain is the same without the mascot, which is what a narrow
// terminal gets.
var BannerWidthPlain = BannerWidth - MascotWidth - mascotGap

// Banner draws the mascot, then the wordmark, then the text. Eleven rows, no
// frame: it is what goes inside a tab that has other things to say.
//
// The mascot is eleven rows and the wordmark six, so the letters are centred
// against it rather than sat on its top edge - which put the whole of the
// text above the eyes and read as two unrelated drawings.
// MascotCost is the extra rows the mascot adds over the wordmark alone, and
// BannerRoom the terminal height from which it is worth spending them. Home is
// a landing screen: one that needs scrolling on a 24-row terminal has already
// failed at the only thing it does, and the mascot is the part that goes.
const (
	MascotCost = MascotHeight - 6
	BannerRoom = 24 + MascotCost + 1
)

func Banner(indent string, mood mascotMood, withMascot bool) string {
	side := bannerSide()
	if !withMascot {
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

	art := renderMascot(mood)
	top := (len(art) - len(wordmark)) / 2

	var b strings.Builder
	for i := 0; i < len(art); i++ {
		line := indent + art[i] + strings.Repeat(" ", mascotGap)
		if w := i - top; w >= 0 && w < len(wordmark) {
			line += paint(wordmark[w])
			if side[w] != "" {
				line += strings.Repeat(" ", sideGap) + side[w]
			}
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
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
	b.WriteString(Banner(indent+strings.Repeat(" ", pad), moodNormal, true))
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

// ── home ──────────────────────────────────────────────────────────────────────

// renderHome is the first thing the panel shows: the wordmark, and how to move
// around it.
//
// The banner used to live only in About, which is the last tab, so the program
// opened on a table of account fields and you had to go looking for the name of
// what you were running. The keys are along the bottom of every tab too, but a
// one-line footer is where you look when you already know what you are doing,
// not when you have just arrived.
// What a member has to do before the panel is any use to them, in the order
// they have to do it.
//
// The first run of this on a fresh machine landed on Home, which explained
// the arrow keys, and then answered every data tab with `unauthorized`.
// Nothing anywhere said "log in", and the command that does it is a
// subcommand you have to already know about. So Home leads with the step
// that is missing, and stops mentioning it once it is done.
func renderNextStep(l layout, loggedIn, hasKey bool) string {
	if loggedIn && hasKey {
		return ""
	}

	section := lipgloss.NewStyle().Foreground(cGray).Bold(true)
	num := lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	cmd := lipgloss.NewStyle().Foreground(cWhite).Bold(true)
	desc := lipgloss.NewStyle().Foreground(cGray)
	done := lipgloss.NewStyle().Foreground(cDimGray)

	var b strings.Builder
	b.WriteString(l.indent + section.Render("Start here") + "\n\n")

	// Every one of these is a key to press right here. The list used to open
	// with "q, quit, so you have your shell back", because signing in meant
	// leaving the panel for a subcommand and two prompts on stdin - which is
	// where people got stuck, and the reason the panel signs you in itself now.
	steps := []struct {
		what, why string
		done      bool
	}{
		{"s", "sign in - a link goes to your email", loggedIn},
		{"e", "paste your API key, in Setup", hasKey},
		{"space, then c", "pick your tools and apply", false},
	}

	// One column for the commands, measured over all of them, so the reasons
	// line up under each other the way the two lists below this one do.
	column := 0
	for _, s := range steps {
		if w := lipgloss.Width(s.what); w > column {
			column = w
		}
	}
	column += 2

	for i, st := range steps {
		marker := num.Render(fmt.Sprintf("%d.", i+1))
		body := cmd.Width(column).Render(st.what) + desc.Render(st.why)
		if st.done {
			marker = done.Render("✓ ")
			body = done.Width(column).Render(st.what) + done.Render(st.why)
		}
		b.WriteString(l.indent + marker + " " + body + "\n")
	}

	if !loggedIn {
		b.WriteString("\n" + l.indent + done.Render("Profile, Usage and Costs stay empty until you sign in.") + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderHome(l layout, loggedIn, hasKey bool, mood mascotMood) string {
	var b strings.Builder

	if l.w >= BannerWidthPlain+4 {
		b.WriteString(Banner(l.indent, mood, l.w >= BannerWidth+4 && l.h >= BannerRoom) + "\n")
	} else {
		name, dim, _ := styles()
		b.WriteString(l.indent + name.Render("nan.builders") + "\n")
		b.WriteString(l.indent + dim.Render("cloud CLI · v"+Version) + "\n\n")
	}

	b.WriteString(renderNextStep(l, loggedIn, hasKey))

	section := lipgloss.NewStyle().Foreground(cGray).Bold(true)
	key := lipgloss.NewStyle().Foreground(lipgloss.Color(brandVioletText)).Bold(true)
	desc := lipgloss.NewStyle().Foreground(cGray)

	b.WriteString(l.indent + section.Render("Getting around") + "\n\n")

	keys := []struct{ k, d string }{
		{"←/→", "move between tabs"},
		{"↑/↓", "scroll the tab you are on"},
		{"r", "refresh it"},
		{"?", "every shortcut, including the ones for Setup"},
		{"q", "quit"},
	}
	tabs := []struct{ k, d string }{
		{"Usage", "what you have spent, over 24 hours, 30 days and all time"},
		{"Models", "what your key can call, and what you have spent on each"},
		{"Costs", "what that usage would have cost you elsewhere"},
		{"Setup", "your API key, and the tools this configures for you"},
	}

	// One column for both lists, measured over both, so the descriptions line
	// up under each other instead of starting wherever the key ends.
	column := 0
	for _, s := range append(append([]struct{ k, d string }{}, keys...), tabs...) {
		if w := lipgloss.Width(s.k); w > column {
			column = w
		}
	}
	column += 3

	for _, s := range keys {
		b.WriteString(l.indent + key.Width(column).Render(s.k) + desc.Render(s.d) + "\n")
	}

	b.WriteString("\n" + l.indent + section.Render("The tabs") + "\n")
	for _, t := range tabs {
		b.WriteString(l.indent + key.Width(column).Render(t.k) + desc.Render(t.d) + "\n")
	}

	return b.String()
}

package ui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
	ltable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
)

// The pieces every screen is built from.
//
// They exist so the plain commands and the dashboard draw the same thing:
// a key/value row in `helmcode keys list` and one inside the Keys tab are the
// same two lines of code, and a change to how Helmcode looks is a change in
// one place rather than eleven.

// Eyebrow is the brand's section marker: `// SECTION`, upper case, in the
// accent, with the letters held apart.
//
// The brandbook calls the wide tracking on this a signature and asks for
// 0.10em; a terminal has one cell per character and no tracking at all, so
// the spacing is faked with a space between letters. It is the one place
// that trick is worth it — it is what makes a heading read as Helmcode's
// rather than as any CLI's bold text.
func Eyebrow(s string) string {
	t := Current()
	slash := lipgloss.NewStyle().Foreground(t.AccentText)
	label := lipgloss.NewStyle().Foreground(t.AccentText)
	return slash.Render("// ") + label.Render(track(strings.ToUpper(s)))
}

// track puts a space between characters, and two between words, so a word
// break survives the letters being spread out.
func track(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Rule is a hairline. The brandbook wants 0.5px and a terminal's thinnest
// line is one cell, so the weight it cannot give is taken out of the colour
// instead: the line is drawn in the border tone, never in text.
func Rule(width int) string {
	if width < 1 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(Current().Line).Render(strings.Repeat("─", width))
}

// Title is a section heading inside a screen.
func Title(s string) string {
	return lipgloss.NewStyle().Foreground(Current().Text).Bold(true).Render(sanitize(s))
}

// Label is the muted half of a key/value pair.
func Label(s string) string {
	return lipgloss.NewStyle().Foreground(Current().TextDim).Render(sanitize(s))
}

// Value is an ordinary value: a name, a date, a sentence.
func Value(s string) string {
	return lipgloss.NewStyle().Foreground(Current().Text).Render(sanitize(s))
}

// Mono is a technical value — an id, a key prefix, a count. The brandbook
// reserves its mono track for exactly these, and in a terminal everything is
// already monospaced, so what carries the distinction here is the colour.
func Mono(s string) string {
	return lipgloss.NewStyle().Foreground(Current().Mono).Render(sanitize(s))
}

// Faint is the legible floor: hints, empty states, things that are present
// but not being pointed at.
func Faint(s string) string {
	return lipgloss.NewStyle().Foreground(Current().TextFaint).Render(sanitize(s))
}

// Accent is for a figure being pointed at. Text, so it takes the readable
// indigo and not the fill.
func Accent(s string) string {
	return lipgloss.NewStyle().Foreground(Current().AccentText).Render(sanitize(s))
}

// KV renders `label   value`, with the label padded to a shared column so a
// run of them line up.
//
// The trailing trim is for values assembled from optional parts — a figure
// and a delta that turned out to be empty — which otherwise leave plain
// spaces at the end of the line. It cannot damage a tag: a filled badge's
// padding lives INSIDE its colour escape, where this cannot reach it, which
// is exactly right because there the space is the box.
func KV(label, value string, column int) string {
	pad := column - lipgloss.Width(label)
	if pad < 1 {
		pad = 1
	}
	return strings.TrimRight(Label(label)+strings.Repeat(" ", pad)+value, " ")
}

// KVBlock renders a run of label/value rows: a definition list.
//
// A borderless two-column table rather than hand-measured padding. The
// alignment is then the library's problem, which is where it belongs, and a
// value that is too wide wraps under its own column instead of pushing the
// line off the screen.
//
// Both front ends draw this, so it lives here rather than once in the command
// layer and again in the dashboard: the day a label column changes width, it
// should change in both or the two stop looking like one program.
func KVBlock(rows [][2]string, indent string) string {
	if len(rows) == 0 {
		return ""
	}
	label := lipgloss.NewStyle().Foreground(Current().TextDim).PaddingRight(3)

	tbl := ltable.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).
		BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderRow(false).BorderHeader(false).
		StyleFunc(func(_, col int) lipgloss.Style {
			if col == 0 {
				return label
			}
			return lipgloss.NewStyle()
		})

	for _, r := range rows {
		tbl = tbl.Row(r[0], r[1])
	}
	return Indent(TrimLines(tbl.String()), indent) + "\n"
}

// LabelColumn measures the label column a run of KV rows needs, so the caller
// does not have to guess and then be wrong when a label changes.
func LabelColumn(labels ...string) int {
	widest := 0
	for _, l := range labels {
		if w := lipgloss.Width(l); w > widest {
			widest = w
		}
	}
	return widest + 3
}

// Bar is a share of a ceiling, drawn by bubbles/progress.
//
// This was hand-rolled — repeat a block character, repeat a dash — which is
// the same picture without the gradient, without the fractional last cell,
// and with a NaN bug that had to be found and fixed by hand. The library is
// already a dependency for the dashboard; there is no reason for the commands
// to draw their own.
//
// Rendering the track matters as much as the fill: a bar drawn only up to its
// value says how much, but not out of how much, and every bar in this CLI is
// a share of something with a ceiling — a quota, a key limit, a month's
// tokens.
func Bar(fraction float64, width int, alarm bool) string {
	if width < 1 {
		return ""
	}
	// NaN satisfies neither comparison, so without this it reaches the
	// renderer as a very large negative and panics.
	if math.IsNaN(fraction) || fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	t := Current()
	// The brand's two indigos, blended along the bar — and the traffic-light
	// red when the thing being measured is nearly spent, which is the one
	// use the brandbook allows it.
	start, end := t.Accent, t.AccentText
	if alarm {
		start, end = t.Red, t.Yellow
	}

	p := progress.New(
		progress.WithColors(start, end),
		progress.WithoutPercentage(),
		progress.WithFillCharacters('█', '─'),
		progress.WithWidth(width),
	)
	return p.ViewAs(fraction)
}

// levelSteps is how finely the usage scale is cut: one colour per tenth.
const levelSteps = 11

// Level is the colour of a share of an allowance: green while there is
// plenty left, yellow around the middle, red as it runs out. A scale rather
// than an alarm switched on at 90%, so how spent something is can be read
// from the colour alone, and two caps at 20% and 70% no longer look alike.
//
// Traffic-light colours are the one thing the brandbook keeps for severity
// of data, and how much of a cap is gone is exactly that. A share of the
// organisation's traffic is not, and keeps the brand's indigo (Bar).
func Level(fraction float64) color.Color {
	if math.IsNaN(fraction) || fraction < 0 {
		fraction = 0
	}
	t := Current()
	ramp := lipgloss.Blend1D(levelSteps, t.Green, t.Yellow, t.Red)
	return ramp[min(int(math.Round(math.Min(fraction, 1)*float64(levelSteps-1))), levelSteps-1)]
}

// LevelBar is a bar in the usage scale: its fill in Level's colour for how
// full it is, so the length and the colour say the same thing twice.
func LevelBar(fraction float64, width int) string {
	if width < 1 {
		return ""
	}
	if math.IsNaN(fraction) || fraction < 0 {
		fraction = 0
	}
	c := Level(fraction)
	p := progress.New(
		progress.WithColors(c, c),
		progress.WithoutPercentage(),
		progress.WithFillCharacters('█', '─'),
		progress.WithWidth(width),
	)
	return p.ViewAs(math.Min(fraction, 1))
}

// LevelPercent is a share of an allowance, as a figure in its Level colour.
func LevelPercent(fraction float64) string {
	return lipgloss.NewStyle().Foreground(Level(fraction)).Render(Percent(fraction))
}

// TagKind is how loudly a tag is drawn.
type TagKind int

const (
	// TagNeutral is the default, and what most tags should be: the
	// brandbook reserves the accent for what is actually being highlighted,
	// and a screen where every tag shouts highlights nothing.
	TagNeutral TagKind = iota
	TagAccent
	TagFilled
	TagDanger
	TagWarn
	TagOK
)

// Tag is a small upper-case badge: a role, a status, a tier.
func Tag(s string, kind TagKind) string {
	t := Current()
	// Only the filled tag is padded. On every other kind the padding is two
	// stray spaces that push the tag out of the column it sits in; on the
	// filled one it is the box itself.
	style := lipgloss.NewStyle()
	switch kind {
	case TagAccent:
		style = style.Foreground(t.AccentText)
	case TagFilled:
		style = style.Padding(0, 1).Foreground(lipgloss.Color("#0a0a0a")).Background(t.Accent)
	case TagDanger:
		style = style.Foreground(t.Red)
	case TagWarn:
		style = style.Foreground(t.Yellow)
	case TagOK:
		style = style.Foreground(t.Green)
	default:
		style = style.Foreground(t.TextFaint)
	}
	return style.Render(strings.ToUpper(sanitize(s)))
}

// Error is how a failure is printed, everywhere.
func Error(err error) string {
	return lipgloss.NewStyle().Foreground(Current().Red).Render("✗ ") + Value(err.Error())
}

// Success is its counterpart, for a command that changed something.
func Success(msg string) string {
	return lipgloss.NewStyle().Foreground(Current().Green).Render("✓ ") + Value(msg)
}

// Warn is for something that worked but should be looked at.
func Warn(msg string) string {
	return lipgloss.NewStyle().Foreground(Current().Yellow).Render("! ") + Value(msg)
}

// Fingerprint is the most of a secret anyone needs to see: enough to tell two
// keys apart, which is the only question a screen ever asks of one, and not
// enough to use. Nothing in this CLI prints a whole key except the single
// moment it is issued.
func Fingerprint(secret string) string {
	if len(secret) <= 12 {
		return strings.Repeat("·", len(secret))
	}
	return secret[:8] + "…" + secret[len(secret)-4:]
}

// Truncate shortens a string to fit a column, with an ellipsis when it had
// to cut.
//
// Delegated to ansi.Truncate, which understands escape sequences. Walking
// runes and measuring each one — which is what this did — cuts a styled
// string INSIDE its escape sequence: `Truncate(ui.Value("production-api"), 10)`
// returned "\x1b[38;2;255…", a broken sequence and no visible text, which on
// a real terminal swallows whatever is printed after it.
//
// That is not a hypothetical: three of the four callers pass strings this
// package has already styled — the sidebar rows, the status line and the
// footer — and they all truncate exactly when the terminal is narrow.
func Truncate(s string, width int) string {
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(s, width, "…")
}

// Pad right-pads to a display width, for hand-built columns.
func Pad(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// PadLeft is the same for figures, which line up on the right.
func PadLeft(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return strings.Repeat(" ", gap) + s
}

// TrimLines removes the trailing whitespace a table leaves on every row.
//
// lipgloss pads each cell to its column, which is what makes the columns line
// up and also what leaves a run of spaces at the end of every line. Nobody
// sees them on screen and everybody sees them in a diff, in a paste, and in
// `git add` on a file somebody redirected this into.
func TrimLines(block string) string {
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// Indent shifts a whole rendered block, keeping blank lines blank so the
// indent does not become trailing whitespace on an empty row.
func Indent(block, indent string) string {
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

// HelpStyles dresses bubbles/help in the brand.
//
// The keys carry the accent because they are the part you act on; the
// descriptions sit at the muted level, and the separator is the hairline
// tone. Without this the help is the one part of the dashboard rendered in
// somebody else's palette.
func HelpStyles() help.Styles {
	t := Current()
	key := lipgloss.NewStyle().Foreground(t.AccentText)
	desc := lipgloss.NewStyle().Foreground(t.TextFaint)
	sep := lipgloss.NewStyle().Foreground(t.Line)

	return help.Styles{
		Ellipsis:       sep,
		ShortKey:       key,
		ShortDesc:      desc,
		ShortSeparator: sep,
		FullKey:        key,
		FullDesc:       desc,
		FullSeparator:  sep,
	}
}

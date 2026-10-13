package pulse

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/ui"
)

// Costs is what a member's NaN usage would have cost on other providers.

type providerPricing struct {
	model    string
	provider string
	inPer1M  float64 // $ per 1M input tokens
	outPer1M float64 // $ per 1M output tokens
}

// Per 1M tokens, read off each vendor's own pricing page on 2026-09-14, and
// the Anthropic and OpenAI rows again on 2026-09-30 when both had newer
// models out: Opus 5.5 and Sonnet 5.5 for Opus 5 and Sonnet 5, GPT-6.1 Sol and
// GPT-6 Luna for their 5.6 namesakes. Terra stays at 5.6 because OpenAI has
// not shipped a GPT-6 one. Each is the price of the model a member would
// reach for today.
//
// Ten rows, for the spread rather than the count: this multiplies a member's
// usage by each of them, so a table of only mid-range models answers only
// the mid-range question. From Luna at $0.10 in to Astra and Fable at $10, a
// reader can find the row that matches what they would actually have used.
//
// Some rows carry a condition the table cannot express, so each is taken at
// its lowest published rate and the comparison stays conservative: the GPT-6
// and GPT-5.6 rows cost more above a 272k-token prompt, Gemini 3.1 Pro costs
// $4/$18 above a 200k-token prompt, and Gemini 3.8 Flash is on a promotional
// rate that doubles on 2027-01-01. TestGeminiFlashPromoHasNotExpired fails on
// that date so the number is changed rather than forgotten.
var pricingTable = []providerPricing{
	{"Claude Fable 5.1", "Anthropic", 10.00, 50.00},
	{"Claude Opus 5.5", "Anthropic", 4.00, 20.00},
	{"Claude Sonnet 5.5", "Anthropic", 2.00, 10.00},
	{"Claude Haiku 4.5", "Anthropic", 1.00, 5.00},
	{"GPT-6 Astra", "OpenAI", 10.00, 50.00},
	{"GPT-6.1 Sol", "OpenAI", 2.00, 10.00},
	{"GPT-5.6 Terra", "OpenAI", 2.00, 12.00},
	{"GPT-6 Luna", "OpenAI", 0.10, 0.50},
	{"Gemini 3.1 Pro", "Google", 2.00, 12.00},
	{"Gemini 3.8 Flash", "Google", 0.75, 3.75},
}

// geminiFlashPromoEnds is the day Gemini 3.8 Flash stops being half price.
var geminiFlashPromoEnds = time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)

var providerColor = map[string]color.Color{
	"Anthropic": lipgloss.Color("#F97316"),
	"OpenAI":    lipgloss.Color("#10B981"),
	"Google":    lipgloss.Color("#3B82F6"),
}

// calcCost prices a period's tokens. Input and output are summed over the
// models, because that is what a price applies to.
func calcCost(p api.Period, price providerPricing) float64 {
	in, out := p.Sum()
	return in/1_000_000*price.inPer1M + out/1_000_000*price.outPer1M
}

// fmtCost is a dollar amount with thousands separators. Go has no `%,`
// verb: that format once printed `$%!,(float64=1234.5).2f` on every figure
// over a thousand.
func fmtCost(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	dot := len(s) - 3
	whole, dec := s[:dot], s[dot:]
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return "$" + b.String() + dec
}

// costsNote is said under the table: why none of this is a bill.
const costsNote = "Your usage is included in your membership. No per-token charges."

func (m model) renderCosts(w int) string {
	t := ui.Current()
	var b strings.Builder
	b.WriteString("\n" + m.eyebrow("Cost comparison") + "\n")

	// The longest that fits: measured rather than guessed.
	subtitle := ""
	for _, candidate := range []string{
		"Estimated cost based on your actual input/output tokens on other providers.",
		"Estimated cost of your usage on other providers.",
		"Estimated cost elsewhere.",
	} {
		subtitle = candidate
		if lipgloss.Width(candidate) <= w {
			break
		}
	}
	b.WriteString(ui.Faint(subtitle) + "\n\n")

	s := m.snap
	if s.usage == nil {
		return b.String()
	}
	t24, t30, all := s.usage.Last24h, s.usage.Last30d, s.usage.AllTime
	in30, out30 := t30.Sum()
	inAll, outAll := all.Sum()
	showAll := w >= 64 && (inAll != in30 || outAll != out30)
	// Below this the provider word goes: it is the widest part of the label
	// and the least load-bearing, since each provider has its own colour.
	showProvider := w >= 64

	type line struct{ plain, styled, c24, c30, call string }
	lines := make([]line, len(pricingTable))
	nameW, c24W, c30W, callW := len("Provider"), len("24 hours"), len("30 days"), len("All time")
	for i, p := range pricingTable {
		pc := providerColor[p.provider]
		l := line{
			plain:  p.model,
			styled: lipgloss.NewStyle().Foreground(pc).Bold(true).Render(p.model),
			c24:    fmtCost(calcCost(t24, p) * m.reveal),
			c30:    fmtCost(calcCost(t30, p) * m.reveal),
			call:   fmtCost(calcCost(all, p) * m.reveal),
		}
		if showProvider {
			l.plain = p.model + "  " + p.provider
			l.styled = lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(p.model) +
				"  " + lipgloss.NewStyle().Foreground(pc).Render(p.provider)
		}
		nameW, c24W = max(nameW, lipgloss.Width(l.plain)), max(c24W, len(l.c24))
		c30W, callW = max(c30W, len(l.c30)), max(callW, len(l.call))
		lines[i] = l
	}
	nameW += 2

	head := ui.Pad("Provider", nameW) + "  " + ui.PadLeft("24 hours", c24W) + "  " + ui.PadLeft("30 days", c30W)
	width := nameW + 2 + c24W + 2 + c30W
	if showAll {
		head += "  " + ui.PadLeft("All time", callW)
		width += 2 + callW
	}
	b.WriteString("  " + ui.Faint(head) + "\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Line).Render(strings.Repeat("─", width)) + "\n")

	num := lipgloss.NewStyle().Foreground(t.TextDim)
	last := lipgloss.NewStyle().Foreground(t.Text).Bold(true)
	// One line each, and a blank line only where the provider changes: the
	// gaps group the rows by who charges them.
	for i, l := range lines {
		if i > 0 && pricingTable[i].provider != pricingTable[i-1].provider {
			b.WriteString("\n")
		}
		text := l.styled + strings.Repeat(" ", max(nameW-lipgloss.Width(l.plain), 0)) + "  " + num.Render(ui.PadLeft(l.c24, c24W))
		if showAll {
			text += "  " + num.Render(ui.PadLeft(l.c30, c30W)) + "  " + last.Render(ui.PadLeft(l.call, callW))
		} else {
			text += "  " + last.Render(ui.PadLeft(l.c30, c30W))
		}
		b.WriteString("  " + text + "\n")
	}

	// The note, boxed, and wrapped rather than run off a narrow terminal.
	noteW := min(lipgloss.Width("NaN  "+costsNote), max(w-6, 8))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Line).Padding(0, 1).Width(noteW + 4)
	note := lipgloss.NewStyle().Foreground(t.AccentText).Bold(true).Render("NaN") + "  " + ui.Faint(costsNote)
	b.WriteString("\n" + ui.Indent(box.Render(note), "  ") + "\n")
	return b.String()
}

// kindBadge colours a model's kind the same way everywhere it is shown.
func kindBadge(kind string) string {
	c, ok := kindColor[kind]
	if !ok {
		c = ui.Current().TextFaint
	}
	return lipgloss.NewStyle().Foreground(c).Render(kind)
}

var kindColor = map[string]color.Color{
	"text generation":     lipgloss.Color("#3B82F6"),
	"audio speech":        lipgloss.Color("#8B5CF6"),
	"audio transcription": lipgloss.Color("#F59E0B"),
	"embedding":           lipgloss.Color("#10B981"),
	"chat":                lipgloss.Color("#3B82F6"),
	"chat · premium":      lipgloss.Color("#A78BFA"),
	"rerank":              lipgloss.Color("#10B981"),
	"text to speech":      lipgloss.Color("#8B5CF6"),
	"speech to text":      lipgloss.Color("#F59E0B"),
	"image":               lipgloss.Color("#EC4899"),
	"unknown":             lipgloss.Color("#71717A"),
}

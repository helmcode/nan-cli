package pulse

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	catalog "github.com/nxssie/nan-cli/internal/models"
	"github.com/nxssie/nan-cli/internal/ui"
)

// Cards: what enter shows about one thing, over the area it lives in.

type cardKind int

const (
	cardModel cardKind = iota + 1
	cardUsage
	cardHarness
	cardProfile
	cardSignIn
	cardUpdate
)

// link is a way from one card to another.
type link struct {
	label string
	hint  string
	to    target
}

type card struct {
	title string
	body  string
	links []link
}

func buildCard(s snapshot, t target, width int, reveal float64) card {
	switch t.kind {
	case cardModel, cardUsage:
		return modelCard(s, t.id, width, reveal)
	}
	return card{title: "?", body: ui.Faint("Nothing here.")}
}

func kindLabel(k cardKind) string {
	if k == cardModel || k == cardUsage {
		return "Model"
	}
	return ""
}

// figure is a number with what it counts under it.
func figure(value, label string) string {
	t := ui.Current()
	return lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(value) + "\n" +
		lipgloss.NewStyle().Foreground(t.TextFaint).Render(label)
}

// figures sets figures side by side, each in an equal share of the width.
func figures(width int, cells ...string) string {
	if len(cells) == 0 {
		return ""
	}
	w := max(width/len(cells), 12)
	styled := make([]string, len(cells))
	for i, c := range cells {
		styled[i] = cutPad(c, w-2) + "  "
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, styled...)
}

// modelCard is a model: what the catalogue knows of it, and what the member
// spent on it in each window, input and output apart.
func modelCard(s snapshot, id string, width int, reveal float64) card {
	c := card{title: id}
	var b strings.Builder

	if m, ok := catalog.Get(id); ok {
		b.WriteString(kindBadge(kindOf(id)) + "   " + ui.Faint(m.Name) + "\n")
		if m.Context > 0 {
			facts := []string{
				ui.Count(float64(m.Context)) + " tokens of context",
				ui.Count(float64(m.Output)) + " out",
				"takes " + strings.Join(m.Inputs, ", "),
			}
			if m.Reasoning {
				facts = append(facts, "reasons")
			}
			b.WriteString(ui.Faint(strings.Join(facts, " · ")) + "\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString(kindBadge(kindOf(id)) + "\n\n")
	}

	found := false
	for _, p := range s.periods() {
		for _, mu := range p.ByModel {
			if mu.Model != id || mu.Total() == 0 {
				continue
			}
			found = true
			b.WriteString(ui.Faint(p.title) + "\n")
			total := float64(p.TotalTokens)
			share := 0.0
			if total > 0 {
				share = mu.Total() / total
			}
			b.WriteString(ui.Bar(share*reveal, barWidth(width), false) + " " + ui.Value(ui.Percent(share)) +
				ui.Faint(fmt.Sprintf("  %s tokens · %s in, %s out", ui.Tokens(mu.Total()), ui.Tokens(float64(mu.InputTokens)), ui.Tokens(float64(mu.OutputTokens)))) + "\n\n")
		}
	}
	if !found {
		b.WriteString(ui.Faint("No usage on this model yet.") + "\n")
	}
	c.body = b.String()
	return c
}

func barWidth(width int) int { return min(max(width-40, 10), 40) }

// cutPad cuts a block to width, padding every line to it.
func cutPad(block string, width int) string {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	for i, l := range lines {
		lines[i] = ui.Pad(ansi.Truncate(l, width, "…"), width)
	}
	return strings.Join(lines, "\n")
}

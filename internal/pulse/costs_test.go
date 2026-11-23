package pulse

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// costsNoteText returns the copy inside the footer box of the Costs tab, read
// the way a member reads it: borders, colours and line wrapping taken out. The
// box is found by its top and bottom edges, not by any character the table
// above it might also use.
func costsNoteText(t *testing.T, w int) string {
	t.Helper()
	m, _ := demoModel(t, w, 40)
	var words []string
	inBox := false
	for _, line := range strings.Split(ansi.Strip(m.renderCosts(w)), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "╭"):
			inBox = true
		case strings.HasPrefix(trimmed, "╰"):
			inBox = false
		case inBox:
			words = append(words, strings.Fields(strings.Trim(trimmed, "│"))...)
		}
	}
	if len(words) == 0 {
		t.Fatalf("at %d columns the Costs tab has no footer note", w)
	}
	return strings.Join(words, " ")
}

// Membership includes credits and every chat model draws them at its own
// price per 1M tokens, so the footer must not say usage is free at the margin.
// It points to the page that holds the prices instead of quoting any.
func TestCostsNoteDescribesCredits(t *testing.T) {
	for _, w := range []int{50, 80, 120} {
		note := costsNoteText(t, w)
		if strings.Contains(note, "per-token charges") || strings.Contains(note, "included in your membership") {
			t.Errorf("at %d columns the note still has the flat-fee copy: %q", w, note)
		}
		for _, want := range []string{"your credits", "1M", "https://cloud.nan.builders/tokens"} {
			if !strings.Contains(note, want) {
				t.Errorf("at %d columns the note does not mention %q: %q", w, want, note)
			}
		}
		if regexp.MustCompile(`[$€]\s?\d`).MatchString(note) {
			t.Errorf("at %d columns the note hard-codes a price: %q", w, note)
		}
	}
}

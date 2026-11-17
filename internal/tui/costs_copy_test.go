package tui

import (
	"regexp"
	"strings"
	"testing"
)

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// costsNote returns the text inside the footer box of the Costs tab, with the
// borders, colours and line wrapping taken out, so a test can read the copy the
// way a member does whatever width the box was drawn at.
func costsNote(t *testing.T, w int) string {
	t.Helper()
	usage := map[string]any{
		"last24h": map[string]any{"byModel": []any{
			map[string]any{"model": "gemma4", "inputTokens": 1000.0, "outputTokens": 500.0},
		}},
	}
	var words []string
	for _, line := range strings.Split(renderCosts(usage, newLayout(w, 40)), "\n") {
		line = ansiSeq.ReplaceAllString(line, "")
		if !strings.Contains(line, "│") {
			continue
		}
		line = strings.ReplaceAll(line, "│", "")
		words = append(words, strings.Fields(line)...)
	}
	if len(words) == 0 {
		t.Fatalf("at %d columns the Costs tab has no footer note", w)
	}
	return strings.Join(words, " ")
}

// Membership includes credits, and every chat model draws them at its own
// price per 1M tokens. The footer used to say the opposite: "No per-token
// charges". It now says where usage is paid from and where the prices live,
// without quoting a price that would go stale.
func TestCostsNoteDescribesCredits(t *testing.T) {
	for _, w := range []int{50, 80, 120} {
		note := costsNote(t, w)
		for _, stale := range []string{"No per-token charges", "included in your membership. No"} {
			if strings.Contains(note, stale) {
				t.Errorf("at %d columns the note still says %q: %q", w, stale, note)
			}
		}
		for _, want := range []string{"credits", "per 1M tokens", "cloud.nan.builders/tokens"} {
			if !strings.Contains(note, want) {
				t.Errorf("at %d columns the note does not mention %q: %q", w, want, note)
			}
		}
		if regexp.MustCompile(`[$€]\s?\d`).MatchString(note) {
			t.Errorf("at %d columns the note hard-codes a price: %q", w, note)
		}
	}
}

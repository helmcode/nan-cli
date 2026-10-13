package pulse

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Decoding never changes the width of a line - a column that jumps while it
// resolves is worse than no effect - and keeps the outline of the words.
func TestDecodingKeepsTheShapeOfTheName(t *testing.T) {
	name := "ci-runner prod.2"
	for _, p := range []float64{0, 0.3, 0.7} {
		got := decode(name, p, 3)
		if len([]rune(got)) != len([]rune(name)) {
			t.Fatalf("decode at %v changed the length: %q", p, got)
		}
		for i, r := range name {
			if !isLetterOrDigit(r) && rune(got[i]) != r {
				t.Errorf("decode at %v moved the %q at %d: %q", p, r, i, got)
			}
		}
	}
	if got := decode(name, 0, 3); got == name {
		t.Error("nothing was scrambled at the start")
	}
	if got := decode(name, 1, 3); got != name {
		t.Errorf("a finished decode is %q", got)
	}
	// Left to right: what has resolved is the start of the name.
	if got := decode(name, 0.5, 9); got[:8] != name[:8] {
		t.Errorf("the first half has not resolved: %q", got)
	}
}

// The wave runs down the table: a row never resolves before the one above.
func TestTheWaveRunsTopDown(t *testing.T) {
	for _, r := range []float64{0.1, 0.4, 0.8, 1} {
		for i := 1; i < 12; i++ {
			if stagger(r, i) > stagger(r, i-1) {
				t.Fatalf("at %v row %d is ahead of row %d", r, i, i-1)
			}
		}
	}
	if stagger(1, 11) != 1 {
		t.Error("a settled reveal leaves rows undecoded")
	}
}

// Changing area is new content, and it arrives: the reveal starts again.
// Moving the cursor is not, and nothing moves.
func TestAChangeOfAreaAnimatesAndMovingDoesNot(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	next, cmd := m.Update(keyOf("2"))
	m = next.(model)
	if !m.animating || m.reveal != 0 || cmd == nil {
		t.Fatalf("switching area did not start the reveal: animating=%v reveal=%v", m.animating, m.reveal)
	}

	m.reveal, m.animating = 1, false
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m = next.(model); m.animating {
		t.Error("moving the cursor started the reveal")
	}
}

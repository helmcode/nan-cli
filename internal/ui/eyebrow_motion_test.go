package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Typing lands exactly on the eyebrow at rest, cursor gone - a heading that
// settles into something slightly different is one that changes after you
// have read it - and has finished typing a little before the spring lands.
func TestTypingLandsOnTheEyebrow(t *testing.T) {
	SetTheme(NewTheme(true))
	for _, title := range []string{"Keys", "What you can do"} {
		rest := ansi.Strip(Eyebrow(title))
		if got := ansi.Strip(EyebrowTyped(title, 1, 0)); got != rest {
			t.Errorf("typing settles on %q, want %q", got, rest)
		}
		if got := strings.TrimSuffix(ansi.Strip(EyebrowTyped(title, 0.9, 0)), "▌"); got != rest {
			t.Errorf("at 0.9 it reads %q, want it typed", got)
		}
	}
}

// The slashes come first, and the letters follow in order.
func TestTypingGoesInOrder(t *testing.T) {
	SetTheme(NewTheme(true))
	rest := ansi.Strip(Eyebrow("Keys"))
	prev := ""
	for p := 0.0; p <= 1; p += 0.05 {
		got := strings.TrimSuffix(ansi.Strip(EyebrowTyped("Keys", p, 1)), "▌")
		if !strings.HasPrefix(rest, got) {
			t.Fatalf("at %.2f it reads %q, which is not the start of %q", p, got, rest)
		}
		if len(got) < len(prev) {
			t.Fatalf("at %.2f it went back from %q to %q", p, prev, got)
		}
		prev = got
	}
}

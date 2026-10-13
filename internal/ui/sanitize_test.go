package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Names, addresses and error codes are chosen by somebody else. A terminal
// executes what it is sent, so what reaches one must not contain the
// sequences that move the cursor, clear the screen or rename the window.
func TestControlCharactersNeverReachTheTerminal(t *testing.T) {
	SetTheme(NewTheme(true))

	// The realistic shape: a member names a key so that the confirmation an
	// admin is shown gets rewritten after it is drawn.
	hostile := "prod\x1b[2K\rharmless-looking\x1b]0;pwned\x07\x9b31m"

	for name, got := range map[string]string{
		"Value":  Value(hostile),
		"Mono":   Mono(hostile),
		"Faint":  Faint(hostile),
		"Accent": Accent(hostile),
		"Label":  Label(hostile),
		"Title":  Title(hostile),
		"Tag":    Tag(hostile, TagNeutral),
	} {
		for _, forbidden := range []string{"\x1b[2K", "\x1b]0;", "\r", "\x07", "\x9b"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s passed %q through to the terminal:\n%q", name, forbidden, got)
			}
		}
		// The legible part of the name still has to survive, or the
		// sanitising has eaten the thing somebody needs to read. Tag upper
		// cases what it is given, so compare case-insensitively.
		if !strings.Contains(strings.ToLower(got), "harmless-looking") {
			t.Errorf("%s dropped the legible part of the name: %q", name, got)
		}
	}
}

// Errors are printed too, and the one carrying a control-plane string is
// reachable before anybody has signed in.
func TestErrorAndFriendsAreSanitised(t *testing.T) {
	SetTheme(NewTheme(true))
	hostile := "no\x1b[2Jthing"

	for name, got := range map[string]string{
		"Error":   Error(&staticErr{hostile}),
		"Success": Success(hostile),
		"Warn":    Warn(hostile),
	} {
		if strings.Contains(got, "\x1b[2J") {
			t.Errorf("%s passed a screen-clear through: %q", name, got)
		}
	}
}

type staticErr struct{ s string }

func (e *staticErr) Error() string { return e.s }

// A tab is not dangerous, and deleting it would silently join two words.
func TestTabsBecomeSpacesRatherThanNothing(t *testing.T) {
	SetTheme(NewTheme(true))
	if got := Value("a\tb"); !strings.Contains(got, "a b") {
		t.Errorf("a tab should become a space, got %q", got)
	}
}

// A byte that is not valid UTF-8 comes back as the replacement character
// rather than being passed on. That is worth having on purpose as well as by
// accident: malformed UTF-8 reaching lipgloss.Render is the shape of
// GO-2026-5970, the x/text infinite loop.
func TestInvalidUTF8IsReplaced(t *testing.T) {
	if got := sanitize("ok\x9bhere"); strings.ContainsRune(got, 0x9b) {
		t.Errorf("a raw C1 byte survived: %q", got)
	}
}

func TestSanitiseLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{"production-api", "cristian@acme.com", "Acme Industrial", "€1,497", "已完成"} {
		if got := sanitize(s); got != s {
			t.Errorf("sanitize(%q) changed it to %q", s, got)
		}
	}
}

// Truncate used to drop one rune and re-measure the whole remainder, which is
// quadratic: a name the length of the client's 8 MiB response cap took the
// better part of a minute, with no timeout in front of it because the request
// had already finished.
func TestTruncateIsNotQuadratic(t *testing.T) {
	SetTheme(NewTheme(true))
	long := strings.Repeat("a", 200_000)

	done := make(chan string, 1)
	go func() { done <- Truncate(long, 20) }()

	select {
	case got := <-done:
		if got != strings.Repeat("a", 19)+"…" {
			t.Errorf("Truncate gave %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Truncate took more than two seconds on a 200k string - it is quadratic again")
	}
}

func TestBarSurvivesNonsense(t *testing.T) {
	SetTheme(NewTheme(true))
	// NaN satisfies neither `< 0` nor `> 1`, so before the guard this
	// underflowed the cell count and panicked inside strings.Repeat.
	for _, f := range []float64{nan(), -1, 0, 0.5, 1, 2} {
		if got := Bar(f, 10, false); got == "" {
			t.Errorf("Bar(%v) rendered nothing", f)
		}
	}
}

func TestTruncateDoesNotCutInsideAnEscapeSequence(t *testing.T) {
	SetTheme(NewTheme(true))
	styled := Value("production-api") + Faint("  never used")

	for _, width := range []int{1, 2, 5, 10, 16, 20, 25} {
		got := Truncate(styled, width)

		// Every escape opened must be closed: an odd count means a cut
		// landed in the middle of one.
		if strings.Count(got, "\x1b") != strings.Count(got, "m") {
			t.Errorf("width %d left a broken escape sequence: %q", width, got)
		}
		// And it must still be the width that was asked for.
		if w := ansi.StringWidth(got); w > width {
			t.Errorf("width %d produced %d columns: %q", width, w, got)
		}
		// The visible text has to survive down to the ellipsis.
		if visible := ansi.Strip(got); visible == "" {
			t.Errorf("width %d rendered nothing visible: %q", width, got)
		}
	}
}

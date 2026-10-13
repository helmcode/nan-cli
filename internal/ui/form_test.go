package ui

import (
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

// A placeholder that does not render is worse than no placeholder: what
// reaches the screen is the first character of it, sitting under the cursor,
// which reads as a stray keystroke rather than as a hint — and the prompt
// stops saying what it wants.
//
// It happens whenever the form is not given a width: huh leaves the field's
// text input at zero, and bubbles' text input draws one character at zero.
// Every form in this program was built without one.
func TestAPlaceholderIsDrawnInFull(t *testing.T) {
	SetTheme(NewTheme(true))

	form := NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Sign in to Helmcode").
			Placeholder("you@example.com"),
	)).WithShowHelp(false)
	form.Init()

	got := ansi.Strip(form.View())
	if !strings.Contains(got, "you@example.com") {
		t.Errorf("the placeholder is not drawn in full:\n%s", got)
	}
}

// And the form is not stretched across the whole terminal. A one-line
// question does not get better for being a hundred columns wide.
func TestAFormIsNotAsWideAsTheTerminal(t *testing.T) {
	SetTheme(NewTheme(true))
	t.Setenv("COLUMNS", "200")

	form := NewForm(huh.NewGroup(
		huh.NewInput().Title("Sign in to Helmcode"),
	)).WithShowHelp(false)
	form.Init()

	for _, line := range strings.Split(ansi.Strip(form.View()), "\n") {
		if w := len([]rune(line)); w > formWidth+4 {
			t.Errorf("a form line is %d columns wide in a 200-column terminal: %q", w, line)
		}
	}
}

// In a terminal too narrow for the cap, the form follows the terminal rather
// than overflowing it.
func TestAFormFitsANarrowTerminal(t *testing.T) {
	SetTheme(NewTheme(true))
	t.Setenv("COLUMNS", "44")

	if got := FormWidth(); got > 40 {
		t.Errorf("FormWidth is %d in a 44-column terminal", got)
	}
}

// A description too long for one line must not cost the field.
//
// huh sizes a group before it knows its width, measuring every description
// as one line, and draws it wrapped: a description that wraps pushes the
// input out of the bottom of its box, leaving a question with no field under
// it and no cursor. NewForm fits the form once built, which re-measures it.
func TestALongDescriptionDoesNotCostTheField(t *testing.T) {
	SetTheme(NewTheme(true))
	t.Setenv("COLUMNS", "50")

	form := NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Sign in to Helmcode").
			Description("Your email address - a link goes there. Helmcode has no password, " +
				"and this sentence is deliberately far too long for one line.").
			Placeholder("you@example.com"),
	))
	form.Init()

	got := ansi.Strip(form.View())
	if !strings.Contains(got, "you@example.com") {
		t.Errorf("the input is not drawn under a description that wraps:\n%s", got)
	}
}

// The text follows the terminal: a wide one gets long lines, a narrow one
// short lines, and a resize while the form is up rewraps it.
func TestAFormRewrapsWhenTheTerminalResizes(t *testing.T) {
	SetTheme(NewTheme(true))
	t.Setenv("COLUMNS", "120")

	desc := strings.TrimSpace(strings.Repeat("words to wrap ", 12))
	form := NewForm(huh.NewGroup(
		huh.NewInput().Title("Your key").Description(desc).Placeholder("sk-hke_"),
	))
	form.Init()

	lines := func() int { return strings.Count(ansi.Strip(form.View()), "\n") + 1 }
	widest := func() int {
		w := 0
		for _, l := range strings.Split(ansi.Strip(form.View()), "\n") {
			w = max(w, ansi.StringWidth(strings.TrimRight(l, " ")))
		}
		return w
	}

	wide, wideLines := widest(), lines()
	if wide <= 60 {
		t.Errorf("in a 120-column terminal the widest line is %d: the form is not using the width", wide)
	}

	FitForm(form, 50, 40)
	if w := widest(); w > 50 {
		t.Errorf("after narrowing to 50 columns a line is %d wide", w)
	}
	if lines() <= wideLines {
		t.Errorf("narrowing did not rewrap: %d lines, the same as at 120", lines())
	}
	if got := ansi.Strip(form.View()); !strings.Contains(got, "sk-hke_") {
		t.Errorf("after narrowing, the input is gone:\n%s", got)
	}
}

// A MultiSelect with a description and no height draws its question and
// none of its answers. Setup's tool picker shipped that way: "Which tools
// should use Helmcode?" over an empty space, with nothing to tick. And the
// height it needs changes with the width, so it has to hold at every width,
// including after a resize.
func TestAPickerShowsEveryOptionAtEveryWidth(t *testing.T) {
	SetTheme(NewTheme(true))
	t.Setenv("COLUMNS", "120")

	names := []string{"pi", "Crush", "OpenCode"}
	options := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		options = append(options, huh.NewOption(n, n))
	}
	form := NewForm(huh.NewGroup(NewPicker(
		"Which tools should use Helmcode?",
		"Only installed tools are listed. Unticking one removes the key from its config.",
		options,
	)))
	form.Init()

	for _, w := range []int{120, 44, 80} {
		FitForm(form, w, 40)
		got := ansi.Strip(form.View())
		for _, n := range names {
			if !strings.Contains(got, n) {
				t.Errorf("at %d columns the picker does not show %q:\n%s", w, n, got)
			}
		}
		// Nor a gap under the last option: the height is exact.
		if trimmed := strings.TrimRight(got, " \n"); !strings.HasSuffix(trimmed, "OpenCode") {
			t.Errorf("at %d columns something follows the last option:\n%s", w, got)
		}
	}
}

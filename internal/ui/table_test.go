package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// The width a table is given is a CEILING. These are about what happens on
// either side of it, because both failures are invisible in a unit test that
// only checks the words are present: a table stretched to fill a wide
// terminal still contains every cell, and so does one wrapped over three
// lines per row.

func keysTable(width int) Table {
	return Table{
		Width: width,
		Columns: []Column{
			{Title: "name"},
			{Title: "key"},
			{Title: "last used"},
			{Title: "created", DropBelow: 96},
			{Title: "by", Max: 28, DropBelow: 78},
		},
		Rows: [][]string{
			{"production-api", "hke_9f3a…7c21", "just now", "2026-05-27", "cristian@acme.com"},
			{"ci-runner", "hke_2b8d…441e", "2 days ago", "2026-05-27", "cristian@acme.com"},
		},
	}
}

func widestLine(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if n := lipgloss.Width(line); n > w {
			w = n
		}
	}
	return w
}

// A table narrower than the terminal is left at its natural size. Passing the
// width to lipgloss as-is makes it an exact width, and it shares the slack
// out between the columns — which leaves the rule stretched across the screen
// with the words huddled at the left of each column.
func TestATableDoesNotStretchToFillTheTerminal(t *testing.T) {
	SetTheme(NewTheme(true))

	narrow := Table{
		Width:   200,
		Columns: []Column{{Title: "email"}, {Title: "role"}},
		Rows:    [][]string{{"dev@acme.com", "MEMBER"}},
	}

	if w := widestLine(narrow.Render()); w > 40 {
		t.Errorf("a two-column table given 200 columns rendered %d wide:\n%s", w, narrow.Render())
	}
}

// Nothing is dropped while the table still fits. The old version compared
// each column's DropBelow against the terminal width on its own, so a table
// that fitted comfortably in 80 columns still lost two of them.
func TestColumnsSurviveWhileTheTableFits(t *testing.T) {
	SetTheme(NewTheme(true))

	out := keysTable(80).Render()
	for _, want := range []string{"CREATED", "BY"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s was dropped at 80 columns even though the table fits:\n%s", want, out)
		}
	}
	if w := widestLine(out); w > 80 {
		t.Errorf("the table is %d wide with a ceiling of 80", w)
	}
}

// When it does not fit, columns go one at a time, most droppable first, and
// it stops as soon as the rest fit. Dropping every candidate at once throws
// away columns that would have fitted once the first one went.
func TestColumnsGoOneAtATimeMostDroppableFirst(t *testing.T) {
	SetTheme(NewTheme(true))

	out := keysTable(70).Render()
	if strings.Contains(out, "CREATED") {
		t.Errorf("the most droppable column survived at 70 columns:\n%s", out)
	}
	if !strings.Contains(out, "BY") {
		t.Errorf("a column was dropped that was not needed to make it fit:\n%s", out)
	}
	if w := widestLine(out); w > 70 {
		t.Errorf("the table is %d wide with a ceiling of 70", w)
	}

	// Tighter still, and the second one goes too.
	if out := keysTable(50).Render(); strings.Contains(out, "BY") {
		t.Errorf("both droppable columns should be gone at 50 columns:\n%s", out)
	}
}

// A column with no DropBelow is never dropped, however tight it gets — it is
// the caller saying the table is meaningless without it. At a width like this
// the remaining columns are squeezed and their headers truncated, which is
// the right answer: unreadable beats absent when the column is load-bearing.
func TestColumnsWithoutDropBelowAreNeverDropped(t *testing.T) {
	SetTheme(NewTheme(true))

	out := keysTable(30).Render()
	// Prefixes, because at 30 columns lipgloss truncates the headers
	// themselves ("LAST USED" arrives as "LAST US…").
	for _, want := range []string{"NAME", "KEY", "LAST"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s was dropped, but it has no DropBelow:\n%s", want, out)
		}
	}
	// And the two that ARE droppable went, rather than everything being
	// squeezed together.
	for _, gone := range []string{"CREATED", "cristian@acme.com"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s survived at 30 columns:\n%s", gone, out)
		}
	}
}

// No rows means no table. Headers over nothing read as a failure to load, and
// the caller has a better sentence for "there are none of these yet".
func TestAnEmptyTableRendersNothing(t *testing.T) {
	SetTheme(NewTheme(true))

	empty := Table{Columns: []Column{{Title: "email"}}}
	if got := empty.Render(); got != "" {
		t.Errorf("an empty table rendered %q", got)
	}
	if got := TableOrEmpty(empty, "no keys yet"); !strings.Contains(got, "no keys yet") {
		t.Errorf("TableOrEmpty dropped its sentence: %q", got)
	}
}

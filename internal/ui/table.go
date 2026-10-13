package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	ltable "charm.land/lipgloss/v2/table"
)

// Tables, rendered by lipgloss/table.
//
// This used to be hand-rolled — measure every cell, pad to the widest, join
// with two spaces — which is a worse version of something the library already
// does. It also only knew how to TRUNCATE a cell that did not fit, where
// lipgloss distributes the width across the columns and wraps.
//
// What is kept is the shape the brandbook asks for, which is not the
// library's default: no grid. Vertical rules between every column and a box
// around the whole thing is the look this design explicitly is not. One
// hairline under the header, air between the columns, and nothing else — the
// separator between things here is space, and a line only inside a component.

// Align says which way a column's cells sit in their width.
type Align int

const (
	// Left is the default: names, addresses, anything read as words.
	Left Align = iota
	// Right is for figures, which line up on their last digit.
	Right
)

// Column is one column of a Table.
type Column struct {
	Title string
	Align Align
	// Max caps the column's width; 0 lets it take what it needs. Use it on
	// the column that would otherwise eat the terminal — a key name, an
	// email — so the ones after it stay on screen.
	Max int
	// DropBelow marks the column as one that may be dropped when the table
	// will not fit, and says how readily: the HIGHER the number, the sooner
	// it goes. Zero keeps it always.
	//
	// Read it as "below this width I am not worth my columns", which is also
	// how the numbers are chosen — but it is an ORDER, not a threshold. A
	// column is only ever dropped because the table overruns the terminal,
	// and they go one at a time until what is left fits, so a table that
	// happens to be narrow keeps everything however tight the terminal is.
	//
	// Dropping beats wrapping. Given 60 columns and five columns to fit,
	// lipgloss will make them all fit — by wrapping every cell over three
	// lines, which is technically correct and unreadable. Losing the column
	// that matters least keeps the ones that matter legible, and a narrow
	// terminal is exactly when legibility is scarce.
	DropBelow int
}

// Table lays out rows under headers.
//
// Cells are pre-rendered strings, styling included, because the caller knows
// which figure is worth an accent and this does not.
type Table struct {
	Columns []Column
	Rows    [][]string
	// Indent goes before every line.
	Indent string
	// Width caps the whole table. Zero lets it size to its content.
	Width int
}

// Render returns the table, or an empty string when there are no rows: an
// empty table drawn with its headers looks like a failure to load, and
// callers have a better sentence for "there are none of these yet".
func (t Table) Render() string {
	if len(t.Rows) == 0 {
		return ""
	}

	// The natural size first. Width is a CEILING, not a target — lipgloss
	// treats it as an exact width and shares the slack out between the
	// columns, which on three short columns in a hundred-column terminal
	// leaves the header and its rule stretched across the screen with the
	// words huddled at the left of each third.
	rendered := t.build().String()
	if t.Width <= 0 || lipgloss.Width(rendered) <= t.Width {
		return Indent(TrimLines(rendered), t.Indent) + "\n"
	}

	// It does not fit. Drop the columns that earn their place least, one at
	// a time, and stop as soon as the rest fit — dropping every candidate at
	// once throws away columns that would have fitted once the first one
	// went.
	t = t.dropColumns()

	tbl := t.build()
	rendered = tbl.String()
	if lipgloss.Width(rendered) > t.Width {
		// Still over, with nothing left worth dropping: let lipgloss squeeze
		// what remains. Wrapped cells are bad; cells off the right-hand edge
		// of the terminal are worse.
		rendered = tbl.Width(t.Width).String()
	}
	return Indent(TrimLines(rendered), t.Indent) + "\n"
}

// build makes the lipgloss table for the columns as they currently stand.
//
// What is kept from the library's defaults is almost nothing: the grid — a
// box round the whole thing and a rule between every column — is the look
// this design explicitly is not. One hairline under the header, air between
// the columns, and nothing else.
func (t Table) build() *ltable.Table {
	headers := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		headers = append(headers, strings.ToUpper(c.Title))
	}

	return ltable.New().
		Headers(headers...).
		Rows(t.Rows...).
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(Current().Line)).
		BorderTop(false).BorderBottom(false).
		BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderRow(false).
		BorderHeader(true).
		StyleFunc(t.styleFunc())
}

// dropColumns removes columns until the table fits, least valuable first.
//
// `DropBelow` orders them: the column willing to go at the widest terminal is
// the one worth least, so it goes first. It stops the moment the rest fit,
// which is what keeps "created by" on screen at ninety columns and only loses
// it at sixty.
func (t Table) dropColumns() Table {
	for len(t.Columns) > 1 {
		// The column willing to go at the widest terminal is the one worth
		// least, so it goes first.
		worst, widest := -1, 0
		for i, c := range t.Columns {
			if c.DropBelow > widest {
				worst, widest = i, c.DropBelow
			}
		}
		if worst < 0 {
			return t // nothing left that is allowed to go
		}

		t = t.without(worst)
		if lipgloss.Width(t.build().String()) <= t.Width {
			return t
		}
	}
	return t
}

// without returns the table with column i, and the matching cell of every
// row, taken out.
func (t Table) without(i int) Table {
	columns := make([]Column, 0, len(t.Columns)-1)
	columns = append(columns, t.Columns[:i]...)
	columns = append(columns, t.Columns[i+1:]...)

	rows := make([][]string, 0, len(t.Rows))
	for _, row := range t.Rows {
		if i >= len(row) {
			rows = append(rows, row)
			continue
		}
		trimmed := make([]string, 0, len(row)-1)
		trimmed = append(trimmed, row[:i]...)
		trimmed = append(trimmed, row[i+1:]...)
		rows = append(rows, trimmed)
	}

	t.Columns, t.Rows = columns, rows
	return t
}

// styleFunc carries the per-column alignment and the header's colour.
//
// It is also where the width cap lives: lipgloss applies MaxWidth per cell,
// which truncates that cell rather than letting one long value push every
// other column off the screen.
func (t Table) styleFunc() ltable.StyleFunc {
	theme := Current()
	header := lipgloss.NewStyle().Foreground(theme.TextFaint).Padding(0, 1)
	cell := lipgloss.NewStyle().Padding(0, 1)

	return func(row, col int) lipgloss.Style {
		s := cell
		if row == ltable.HeaderRow {
			s = header
		}
		if col < len(t.Columns) {
			if t.Columns[col].Align == Right {
				s = s.Align(lipgloss.Right)
			}
			if limit := t.Columns[col].Max; limit > 0 {
				s = s.MaxWidth(limit + 2) // +2 for the cell's own padding
			}
		}
		return s
	}
}

// TableOrEmpty renders a table, or the given sentence when it has no rows.
//
// The empty case is a sentence rather than bare headers on purpose: headers
// with nothing under them read as a failure to load, and "no API keys yet" is
// both true and actionable. An empty `empty` means draw nothing at all, for a
// section that simply does not apply.
func TableOrEmpty(t Table, empty string) string {
	if rendered := t.Render(); rendered != "" {
		return rendered
	}
	if empty == "" {
		return ""
	}
	return t.Indent + Faint(empty) + "\n"
}

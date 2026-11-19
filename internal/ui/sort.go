package ui

// Sorting, as a piece of state rather than a piece of machinery.
//
// What a table needs to be sortable is smaller than it looks: which column
// is active, and which way. The comparison itself belongs to whoever owns
// the data, because only they still have it as numbers and timestamps. By
// the time a row reaches a table it is strings, and sorting strings puts
// "900M" above "4.12B" and 2026-01-02 above 2025-12-31.
//
// So this holds the state, renders the marker, and stays out of the way.
// Both dashboards embed it, which is what stops them disagreeing about what
// pressing 2 does.

// Sort is which column a table is ordered by, and in which direction.
//
// The zero value sorts by the first column, ascending: the name column, A to
// Z. A figures column, once picked, opens descending instead, because the
// interesting end of a list of figures is the top.
type Sort struct {
	col int
	// desc rather than asc, so the zero value is column zero ascending -
	// the name column, A to Z - which is what a table that has never been
	// sorted should show.
	desc bool
	cols int
}

// NewSort returns the ordering for a table of n columns, starting on the
// column the caller considers the natural one.
func NewSort(columns, start int) Sort {
	s := Sort{cols: columns}
	if start > 0 && start < columns {
		// Set directly rather than through By: By treats "the column you are
		// already on" as a request to reverse, so initialising through it
		// would hand back a table sorted the wrong way round.
		s.col = start
		s.desc = true
	}
	return s
}

// Column is the index currently sorted on.
func (s Sort) Column() int { return s.col }

// Ascending reports the direction.
func (s Sort) Ascending() bool { return !s.desc }

// Columns is how many columns this ordering knows about.
func (s Sort) Columns() int { return s.cols }

// By sorts on a column. Asking for the column already active flips the
// direction instead, which is what a second press of the same key means
// everywhere else.
func (s Sort) By(col int) Sort {
	if s.cols <= 0 {
		return s
	}
	if col < 0 || col >= s.cols {
		return s
	}
	if col == s.col {
		s.desc = !s.desc
		return s
	}
	s.col = col
	// A new column starts descending, except the first one: column zero is
	// the name, and a name list reads A to Z.
	s.desc = col != 0
	return s
}

// Next moves to the next column, wrapping. Direction resets with the column.
func (s Sort) Next() Sort {
	if s.cols <= 0 {
		return s
	}
	return s.By((s.col + 1) % s.cols)
}

// Prev moves to the previous column, wrapping.
func (s Sort) Prev() Sort {
	if s.cols <= 0 {
		return s
	}
	return s.By((s.col - 1 + s.cols) % s.cols)
}

// Flip reverses the direction without changing the column.
func (s Sort) Flip() Sort {
	s.desc = !s.desc
	return s
}

// Resize keeps the ordering valid when a table changes shape — a column
// dropped on a narrow terminal, say. An index that no longer exists falls
// back to the first column rather than sorting on nothing.
func (s Sort) Resize(columns int) Sort {
	s.cols = columns
	if s.col >= columns {
		s.col = 0
		s.desc = false
	}
	return s
}

// Marker is the arrow for a column: the direction on the active one, and
// nothing on the rest.
//
// Pointing the way the values run rather than the way a file manager's
// arrows point: descending is largest first, so the arrow points down.
func (s Sort) Marker(col int) string {
	if col != s.col {
		return ""
	}
	if s.desc {
		return "▼"
	}
	return "▲"
}

// Title is a column heading with its marker, for a table that renders its
// own headers.
func (s Sort) Title(col int, title string) string {
	if m := s.Marker(col); m != "" {
		return title + " " + m
	}
	return title
}

// Less wraps an ascending comparison with the current direction, for handing
// to sort.SliceStable:
//
//	sort.SliceStable(keys, s.Less(func(a, b int) bool { ... }))
//
// Callers write one ascending comparison per column and never think about
// the direction again. Stable, so rows that tie keep the order they had and
// the list does not reshuffle under the cursor on every keypress.
func (s Sort) Less(less func(a, b int) bool) func(a, b int) bool {
	if !s.desc {
		return less
	}
	return func(a, b int) bool { return less(b, a) }
}

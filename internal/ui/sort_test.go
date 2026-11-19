package ui

import (
	"sort"
	"testing"
)

func TestTheSameKeyTwiceFlipsTheDirection(t *testing.T) {
	s := NewSort(4, 0)
	if !s.Ascending() {
		t.Error("the name column should start A to Z")
	}

	s = s.By(0)
	if s.Ascending() {
		t.Error("asking for the active column again did not flip the direction")
	}
	s = s.By(0)
	if !s.Ascending() {
		t.Error("a third press did not flip it back")
	}
}

// A figures column opens on its biggest row; a name column opens at A. Both
// are what somebody means by "sort by this", and they are opposite.
func TestANewColumnPicksTheUsefulDirection(t *testing.T) {
	s := NewSort(4, 0)

	s = s.By(2)
	if s.Column() != 2 {
		t.Fatalf("sorting by column 2 left the sort on %d", s.Column())
	}
	if s.Ascending() {
		t.Error("a figures column should open largest first")
	}

	if s = s.By(0); !s.Ascending() {
		t.Error("the name column should open A to Z")
	}
}

func TestNextWrapsAndOutOfRangeIsIgnored(t *testing.T) {
	s := NewSort(3, 0)
	got := []int{}
	for i := 0; i < 4; i++ {
		s = s.Next()
		got = append(got, s.Column())
	}
	want := []int{1, 2, 0, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Next() walked %v, want %v", got, want)
		}
	}

	before := NewSort(3, 1)
	if after := before.By(9); after.Column() != before.Column() || after.Ascending() != before.Ascending() {
		t.Error("a column that does not exist changed the ordering")
	}
	if after := before.By(-1); after.Column() != before.Column() {
		t.Error("a negative column changed the ordering")
	}
}

// Columns get dropped when the terminal is narrow. An ordering left pointing
// at a column that is no longer there would sort on nothing.
func TestResizeRescuesAnOrderingThatLostItsColumn(t *testing.T) {
	s := NewSort(5, 4)
	if s.Column() != 4 {
		t.Fatalf("sort starts on %d", s.Column())
	}

	s = s.Resize(3)
	if s.Column() != 0 {
		t.Errorf("after losing column 4 the sort is on %d, want the first column", s.Column())
	}
	if s.Columns() != 3 {
		t.Errorf("Resize did not record the new width: %d", s.Columns())
	}

	// A column that survives is left alone.
	keep := NewSort(5, 1).Resize(3)
	if keep.Column() != 1 {
		t.Errorf("a surviving column was moved to %d", keep.Column())
	}
}

// The marker is on the active column only, and points the way the values
// run: descending is largest first, so the arrow points down.
func TestTheMarkerIsOnOneColumn(t *testing.T) {
	s := NewSort(3, 1) // column 1, descending

	if got := s.Marker(1); got != "▼" {
		t.Errorf("the active descending column is marked %q", got)
	}
	if got := s.Marker(0); got != "" {
		t.Errorf("an inactive column is marked %q", got)
	}
	if got := s.Flip().Marker(1); got != "▲" {
		t.Errorf("ascending is marked %q", got)
	}
	if got := s.Title(1, "Tokens"); got != "Tokens ▼" {
		t.Errorf("Title = %q", got)
	}
	if got := s.Title(0, "Name"); got != "Name" {
		t.Errorf("an inactive title should be bare, got %q", got)
	}
}

// Less carries the direction so callers write one ascending comparison per
// column. Sorting the rendered strings instead is the bug this exists to
// avoid: "900M" sorts above "4.12B".
func TestLessCarriesTheDirection(t *testing.T) {
	tokens := []float64{4.12e9, 900e6, 41.2e9}

	asc := NewSort(2, 0) // column 0 ascending
	got := append([]float64(nil), tokens...)
	sort.SliceStable(got, asc.Less(func(a, b int) bool { return got[a] < got[b] }))
	if got[0] != 900e6 || got[2] != 41.2e9 {
		t.Errorf("ascending gave %v", got)
	}

	desc := asc.Flip()
	got = append([]float64(nil), tokens...)
	sort.SliceStable(got, desc.Less(func(a, b int) bool { return got[a] < got[b] }))
	if got[0] != 41.2e9 || got[2] != 900e6 {
		t.Errorf("descending gave %v", got)
	}
}

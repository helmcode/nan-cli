package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Six faces, one head: the moods differ only in the eyes, so the silhouette
// of every face matches the normal one pixel for pixel outside the window.
func TestEveryFaceIsTheSameHead(t *testing.T) {
	normal := mascotFaces[MoodNormal]
	for mood, face := range mascotFaces {
		if len(face) != len(normal) {
			t.Fatalf("mood %d is %d rows, the head is %d", mood, len(face), len(normal))
		}
		for y := range face {
			if len(face[y]) != MascotWidth {
				t.Fatalf("mood %d row %d is %d wide", mood, y, len(face[y]))
			}
			for x := range face[y] {
				if (face[y][x] == '#') != (normal[y][x] == '#') {
					t.Errorf("mood %d changes the head at %d,%d", mood, x, y)
				}
			}
		}
	}
}

// Two pixels a cell: 21 rows of pixels are 11 lines on screen.
func TestTheMascotIsHalfAsTallOnScreen(t *testing.T) {
	SetTheme(NewTheme(true))
	for mood := range mascotFaces {
		rows := Mascot(mood)
		if len(rows) != MascotHeight {
			t.Errorf("mood %d draws %d lines, want %d", mood, len(rows), MascotHeight)
		}
		for _, r := range rows {
			if w := ansi.StringWidth(r); w != MascotWidth {
				t.Errorf("mood %d draws a line %d wide", mood, w)
			}
		}
	}
}

// The banner fits the width it says it needs, and carries the names.
func TestTheBannerFitsAndCarriesTheNames(t *testing.T) {
	SetTheme(NewTheme(true))
	for _, withMascot := range []bool{true, false} {
		out := Banner("1.2.3", "", MoodNormal, withMascot)
		want := BannerWidthCompact("1.2.3")
		if withMascot {
			want = BannerWidth("1.2.3")
		}
		for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := ansi.StringWidth(l); w > want {
				t.Errorf("a banner line is %d wide, BannerWidth says %d", w, want)
			}
		}
		plain := ansi.Strip(out)
		for _, name := range []string{"nan.builders", "v1.2.3", "@Nxssie", "Helmcode Team"} {
			if !strings.Contains(plain, name) {
				t.Errorf("the banner does not carry %q", name)
			}
		}
	}
}

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The mascot, drawn as pixels rather than as characters.
//
// A terminal cell is about twice as tall as it is wide, so a glyph like `█` or
// `▀` is two square pixels stacked. Printing `▀` with a foreground AND a
// background colour paints both of them independently, which doubles the
// vertical resolution and is the difference between a picture of the hexagon
// and a pile of box-drawing characters arranged to suggest one. The first four
// attempts at this were the second thing.
//
// 18 by 21 pixels, so 18 columns by 11 rows on screen.

type mascotMood int

const (
	moodNormal mascotMood = iota
	moodAsleep
	moodThinking
	moodHappy
	moodSad
	moodSurprised
)

const (
	// The face window, darker than any background the terminal is likely to
	// have, so the head reads as a solid object with a hole in it.
	mascotDark = "#1A0B33"
	mascotEye  = "#EDE7FF"
)

// MascotWidth and MascotHeight are what it occupies on screen, in cells.
const (
	MascotWidth  = 18
	MascotHeight = 11
)

// mood picks the face from what the panel is actually doing. The expressions
// are not decoration: they answer "what is going on" from across the room,
// which is the question this whole tab kept failing to answer in words.
func (m model) mood() mascotMood {
	switch {
	case m.configuring, m.loading:
		return moodThinking
	case m.err != nil || strings.HasPrefix(m.setupMsg, "error"):
		return moodSad
	case m.loginStage != loginOff:
		return moodSurprised
	case m.sess.Token == "":
		return moodAsleep
	case m.sess.APIKey != "":
		return moodHappy
	}
	return moodNormal
}

// renderMascot paints a grid two pixels to a cell.
func renderMascot(mood mascotMood) []string {
	grid, ok := mascotFaces[mood]
	if !ok {
		grid = mascotFaces[moodNormal]
	}

	colour := map[byte]string{
		'#': brandViolet,
		':': mascotDark,
		'O': mascotEye,
	}

	out := make([]string, 0, (len(grid)+1)/2)
	for y := 0; y < len(grid); y += 2 {
		top := grid[y]
		bottom := ""
		if y+1 < len(grid) {
			bottom = grid[y+1]
		}
		var row strings.Builder
		for x := 0; x < len(top); x++ {
			t, tok := colour[top[x]]
			b := ""
			bok := false
			if x < len(bottom) {
				b, bok = colour[bottom[x]]
			}
			style := lipgloss.NewStyle()
			switch {
			case tok && bok:
				// Upper half in the foreground, lower half in the background:
				// two pixels, one cell.
				row.WriteString(style.Foreground(lipgloss.Color(t)).
					Background(lipgloss.Color(b)).Render("▀"))
			case tok:
				row.WriteString(style.Foreground(lipgloss.Color(t)).Render("▀"))
			case bok:
				row.WriteString(style.Foreground(lipgloss.Color(b)).Render("▄"))
			default:
				row.WriteString(" ")
			}
		}
		out = append(out, row.String())
	}
	return out
}

// The six faces, as pixel grids.
//
// The silhouette is not drawn by eye: it is measured off the brand's own
// hexagon - public/brand/nan-isotipo-hex.svg and the Helmcode symbol are
// the same shape, pointy-top, with vertical left and right sides and 1.148
// times taller than wide. Off the path: the diagonal opens 1.87 of
// half-width per row over 20% of the height, the corner radius takes
// another 9%, and the vertical sides the middle 40%. Drawn the other way
// up, which is how the first four attempts went, it reads as a blob.
//
// `#` body   `:` the dark face window   `O` eye   `.` nothing
var mascotFaces = map[mascotMood][]string{
	moodNormal: {
		"........##........",
		"......######......",
		"....##########....",
		"...#####::#####...",
		".#####::::::#####.",
		"####::::::::::####",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::O:::::O:::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###::O:::::O:::###",
		"####::::::::::####",
		"######::::::######",
		".#######::#######.",
		"...############...",
		"....##########....",
		"......######......",
		"........##........",
	},
	moodAsleep: {
		"........##........",
		"......######......",
		"....##########....",
		"...#####::#####...",
		".#####::::::#####.",
		"####::::::::::####",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"####::::::::::####",
		"######::::::######",
		".#######::#######.",
		"...############...",
		"....##########....",
		"......######......",
		"........##........",
	},
	moodThinking: {
		"........##........",
		"......######......",
		"....##########....",
		"...#####::#####...",
		".#####::::::#####.",
		"####::::::::::####",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::O:::::O:::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###::O:::::O:::###",
		"###::::::::::::###",
		"####::::::::::####",
		"######::::::######",
		".#######::#######.",
		"...############...",
		"....##########....",
		"......######......",
		"........##........",
	},
	moodHappy: {
		"........##........",
		"......######......",
		"....##########....",
		"...#####::#####...",
		".#####::::::#####.",
		"####::::::::::####",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::O:::::O:::###",
		"###:O:O:::O:O::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"####::::::::::####",
		"######::::::######",
		".#######::#######.",
		"...############...",
		"....##########....",
		"......######......",
		"........##........",
	},
	moodSad: {
		"........##........",
		"......######......",
		"....##########....",
		"...#####::#####...",
		".#####::::::#####.",
		"####::::::::::####",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"###:O:O:::O:O::###",
		"###::O:::::O:::###",
		"###::::::::::::###",
		"###::::::::::::###",
		"####::::::::::####",
		"######::::::######",
		".#######::#######.",
		"...############...",
		"....##########....",
		"......######......",
		"........##........",
	},
	moodSurprised: {
		"........##........",
		"......######......",
		"....##########....",
		"...#####::#####...",
		".#####::::::#####.",
		"####::::::::::####",
		"###::::::::::::###",
		"###::::::::::::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"###:OOO:::OOO::###",
		"####::::::::::####",
		"######::::::######",
		".#######::#######.",
		"...############...",
		"....##########....",
		"......######......",
		"........##........",
	},
}

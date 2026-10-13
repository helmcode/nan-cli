package pulse

import "math"

// Motion: what moves, and why it is allowed to.
//
// Everything here is driven by one number, reveal, which a harmonica spring
// takes from 0 to 1 whenever something new comes on screen: the first load,
// a change of area, a card opening. Figures count up to their value, bars
// grow to their length, the sparkline rises, and a table's rows decode from
// the top down - each name starting as noise and resolving left to right.
//
// It is short (the spring settles in well under a second) and it only runs
// when what is on screen changed. That is the line between motion that says
// "this is new" and motion that is just in the way: nothing moves while you
// are reading, and nothing moves twice for the same thing.

// noise is what an undecoded character is drawn as. Lower case and digits:
// the shape of a name or a key, so the column does not jump when it lands.
const noise = "abcdefghijklmnopqrstuvwxyz0123456789"

// decode draws s with the first progress fraction of it real and the rest
// noise. Spaces and punctuation stay put, so the words' outline is there
// from the first frame and only the letters resolve. frame reshuffles the
// noise, so it shimmers rather than sitting still.
func decode(s string, progress float64, frame int) string {
	if progress >= 1 {
		return s
	}
	runes := []rune(s)
	cut := int(math.Floor(math.Max(progress, 0) * float64(len(runes))))
	out := make([]rune, len(runes))
	for i, r := range runes {
		switch {
		case i < cut:
			out[i] = r
		case !isLetterOrDigit(r):
			out[i] = r
		default:
			// A cheap mix of position, frame and letter: different every
			// frame, the same for the same inputs, so a test can pin it.
			x := (i*7919 + frame*104729 + int(r)*31) * 2654435761
			x ^= x >> 15
			out[i] = rune(noise[(x&0x7fffffff)%len(noise)])
		}
	}
	return string(out)
}

func isLetterOrDigit(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// stagger is row i's own progress when the whole table is at reveal: each
// row starts a little after the one above, so the decoding runs down the
// table as a wave rather than every row resolving at once.
//
// The delay stops growing after the first few rows, so a long table still
// lands in the same short time, and a settled reveal resolves every row:
// the wave is how things arrive, never a state they can be left in.
func stagger(reveal float64, i int) float64 {
	const lead, step, rows = 1.6, 0.09, 6
	if reveal >= 1 {
		return 1
	}
	return math.Min(math.Max(reveal*lead-float64(min(i, rows))*step, 0), 1)
}

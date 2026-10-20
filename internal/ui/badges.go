package ui

import (
	"strings"
)

// Turning the platform's numbers into what is drawn: a change, a share of a
// limit, a count against one. Here rather than in the commands and again in
// the dashboard, because the same figure in two different colours is the
// same class of bug as two different violets.

// Delta renders the backend's pre-formatted change string — "+12.4%", "-3%"
// — which arrives already computed. Only the sign is interpreted, and only
// to colour it: a delta with no direction is not pointed at.
//
// No change at all renders as nothing. The backend sends "0" for that, and a
// lone zero sitting after a figure reads as a second, unexplained figure —
// "Active keys  4  0" is a worse line than "Active keys  4".
func Delta(s string) string {
	switch strings.TrimSpace(s) {
	// The platform sends a dash, em or en, when there is no earlier period
	// to compare with. That is "no delta", and printing the character
	// would put the one glyph this CLI never prints on its front page.
	case "", "0", "0%", "+0", "+0%", "-0", "-0%", "-", string(rune(0x2014)), string(rune(0x2013)):
		return ""
	}
	if s[0] == '+' {
		return Accent(s)
	}
	return Faint(s)
}

// Share is a fraction that refuses to divide by zero. A limit of zero means
// "no limit known", and drawing that as a full bar would be alarming and
// wrong in the same breath.
func Share(used, limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	if used < 0 {
		return 0
	}
	return used / limit
}

// CountOf is "3/5", or just "3" when there is no ceiling to compare against.
func CountOf(used, limit int) string {
	if limit <= 0 {
		return Count(float64(used))
	}
	return Count(float64(used)) + "/" + Count(float64(limit))
}

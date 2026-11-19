package ui

import "strings"

// Everything the control plane says eventually reaches a terminal, and a
// terminal executes what it is sent.
//
// A key name, a member's address, an organisation name and an error code are
// all strings somebody else chose. Nothing between the JSON decoder and the
// screen strips control characters on its own: Lip Gloss only wraps a string
// in colour, and colorprofile forwards anything that is not an SGR sequence
// untouched when the destination IS a terminal — so the one place the
// sanitising would happen for free is the pipe, where it does not matter.
//
// The attack that follows is not theoretical and does not need a compromised
// server. A member of the organisation names a key
//
//	prod\r\x1b[2K… something reassuring
//
// and the next admin to run `helmcode members remove` is shown a confirmation
// that has been rewritten after it was drawn, for a different thing than the
// one they are about to destroy. Cursor movement, screen clears and the
// window-title sequence are all available the same way.
//
// So it is stripped here, in the leaf text primitives, which is the last
// point every path goes through: the commands, the dashboard, the prompts and
// the error messages. It is deliberately NOT applied to Truncate or Pad,
// which are handed strings this package has already styled and whose escapes
// are ours.
func sanitize(s string) string {
	// The common case is a name with nothing wrong with it, so check before
	// building anything.
	if !hasControl(s) {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			// A tab is not dangerous and turning it into nothing would
			// silently join two words.
			return ' '
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// C0, DEL and C1. C1 matters because 0x9b is CSI in its own
			// right on terminals that honour eight-bit controls.
			return -1
		}
		return r
	}, s)
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

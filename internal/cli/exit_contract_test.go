package cli

import "testing"

// The exit codes are a contract with every script that branches on them, so
// the numbers are pinned here as numbers. Every other test compares against
// the constants, and a constant changed by mistake would move the code and
// the expectation together.
func TestExitCodesAreTheDocumentedNumbers(t *testing.T) {
	for name, c := range map[string]struct{ got, want int }{
		"succeeded":         {exitSucceeded, 0},
		"failed":            {exitFailed, 1},
		"timed out":         {exitTimedOut, 2},
		"cancelled":         {exitCancelled, 3},
		"config":            {exitConfig, 4},
		"awaiting approval": {exitAwaitingApproval, 10},
		"usage":             {exitUsage, 64},
		"auth":              {exitAuth, 65},
		"data (context)":    {exitDataErr, 65},
		"unavailable":       {exitUnavailable, 69},
		"detached (Ctrl-C)": {exitDetached, 75},
	} {
		if c.got != c.want {
			t.Errorf("%s exits %d; scripts expect %d", name, c.got, c.want)
		}
	}
}

package cmd

import (
	"testing"

	"github.com/nxssie/nan-cli/internal/tui"
)

// `nan --version` and `nan -v` print one line and exit 0, without opening
// the dashboard.
func TestVersionPrintsOneLine(t *testing.T) {
	swap(t, &openTUI, func() error {
		t.Error("--version opened the dashboard")
		return nil
	})

	for _, flag := range []string{"--version", "-v"} {
		code, out := execute(t, flag)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", flag, code, out)
		}
		if got, want := out, "nan "+tui.Version+"\n"; got != want {
			t.Errorf("%s printed %q, want %q", flag, got, want)
		}
	}
}

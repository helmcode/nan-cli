package cmd

import (
	"bytes"
	"testing"

	"github.com/nxssie/nan-cli/internal/tui"
)

// `nan --version` and `nan -v` print one line and exit 0, without opening
// the dashboard.
func TestVersionPrintsOneLine(t *testing.T) {
	old := openTUI
	defer func() { openTUI = old }()
	openTUI = func() error {
		t.Error("--version opened the dashboard")
		return nil
	}

	for _, flag := range []string{"--version", "-v"} {
		var out bytes.Buffer
		rootCmd.SetArgs([]string{flag})
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if got, want := out.String(), "nan "+tui.Version+"\n"; got != want {
			t.Errorf("%s printed %q, want %q", flag, got, want)
		}
		_ = rootCmd.Flags().Set("version", "false")
	}
}

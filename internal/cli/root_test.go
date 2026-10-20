package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/session"
)

// rootCmd is the tree the package's tests drive. The run commands are
// declared once at package level, so one root is enough for all of them.
var rootCmd = NewRootCmd()

// `nan --version` and `nan -v` print one line a script can parse first, and
// exit 0 without opening the dashboard.
func TestVersionPrintsOneLine(t *testing.T) {
	old := openDashboard
	defer func() { openDashboard = old }()
	openDashboard = func(context.Context, *api.Client, *session.Session) error {
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
		first, _, _ := strings.Cut(out.String(), "\n")
		if want := "nan " + build.Version; first != want {
			t.Errorf("%s printed %q first, want %q", flag, first, want)
		}
		_ = rootCmd.Flags().Set("version", "false")
	}
}

// exitCodeFor is how main ends a failed command: the code, and what fang
// prints, which is nothing for a silent exit.
func exitCodeFor(cmd *cobra.Command, err error) (int, string) {
	if Silent(err) {
		return ExitCode(cmd, err), ""
	}
	return ExitCode(cmd, err), err.Error()
}

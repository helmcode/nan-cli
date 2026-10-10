package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/nxssie/nan-cli/internal/tui"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "nan",
	Short:   "nan.builders cloud CLI",
	Version: tui.Version,
	// A sign-in link that did not work is not a usage mistake: printing the
	// flag list under it buries the one line that says what happened. Execute()
	// below prints the error itself, so cobra printing it too showed every
	// failure twice.
	SilenceUsage:  true,
	SilenceErrors: true,
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return openTUI()
	},
}

// openTUI is the dashboard. A variable so a test can tell whether a command
// line reached it.
var openTUI = tui.Run

func init() {
	// SilenceUsage covers runtime failures, but a mistyped flag IS a usage
	// mistake and the flag list is the answer to it.
	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		c.Println(c.UsageString())
		return err
	})

	// One line a script can parse (`nan --version | cut -d' ' -f2`). The
	// framed wordmark this used to print filled the screen like the dashboard
	// does, and read as the dashboard having opened.
	rootCmd.SetVersionTemplate("nan {{.Version}}\n")
}

func Execute() {
	cmd, err := rootCmd.ExecuteC()
	if err == nil {
		return
	}
	code, msg := exitCodeFor(cmd, err)
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(code)
}

// exitCodeFor is how a failed command ends. `nan run` and `nan runs` have a
// documented table, and a script reads their codes: there anything that is
// not already an ExitError is cobra refusing the arguments - an unknown
// flag, a missing id, two flags that exclude each other - which is a usage
// error. Every other command keeps exiting 1.
func exitCodeFor(cmd *cobra.Command, err error) (int, string) {
	var exit *ExitError
	if errors.As(err, &exit) {
		if exit.Err == nil {
			return exit.Code, ""
		}
		return exit.Code, exit.Err.Error()
	}
	if cmd != nil && cmd.Annotations[sysexitsAnnotation] != "" {
		return exitUsage, err.Error()
	}
	return 1, err.Error()
}

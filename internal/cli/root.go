// Package cli is the command tree: what `nan` does when it is given words,
// as opposed to the dashboard it opens when it is given none.
package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/pulse"
)

// openDashboard is the dashboard. A variable so a test can tell whether a
// command line reached it.
var openDashboard = pulse.Run

// NewRootCmd builds the command tree.
//
// The account and setup commands are built here, fresh each time. The run
// commands (run, runs, automations, approvals, secrets) are declared once at
// package level with their flags, as they always were, and hung off each new
// root: their tests drive them through those declarations.
func NewRootCmd() *cobra.Command {
	var asJSON bool
	var noColor bool
	// dashboard is set when the run opened the dashboard, which says there
	// is an update in its own header.
	var dashboard bool
	var a *app

	root := &cobra.Command{
		Use:   build.Name,
		Short: "nan.builders cloud CLI",
		Long: "nan.builders from the terminal: your account, your usage, the coding tools\n" +
			"on this machine, and agent runs in your workspaces.\n\n" +
			"Run it with no arguments for the dashboard; run a subcommand to get one\n" +
			"answer and your shell back.\n\n" +
			"Examples:\n" +
			"  nan                        open the dashboard\n" +
			"  nan auth login             sign in with a link sent to your email\n" +
			"  nan setup                  point the coding tools here at NaN\n" +
			"  nan run \"review PR 42\"     run an agent in your workspace\n" +
			"  nan runs ls                your recent runs\n",

		// A failure is not a usage mistake. Printing the flag list under "your
		// session has expired" buries the one line that says what happened.
		SilenceUsage:  true,
		SilenceErrors: true,

		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			a = newApp(asJSON, noColor)
			return nil
		},

		RunE: func(cmd *cobra.Command, _ []string) error {
			// The dashboard is a live view, and a live view written to a
			// pipe is unreadable noise: anything that is not a terminal
			// gets the help instead, which says what can be asked for.
			if !a.p.Interactive() {
				return cmd.Help()
			}
			dashboard = true
			return openDashboard(cmd.Context(), a.client, a.session)
		},

		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			noticeUpdate(cmd.Context(), a, dashboard)
		},
	}

	// A mistyped flag IS a usage mistake, and the flag list is the answer to
	// it - which is the half SilenceUsage above would otherwise take away.
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		c.Println(c.UsageString())
		return err
	})

	root.PersistentFlags().BoolVar(&asJSON, "json", false,
		"print the platform's JSON instead of a rendering of it")
	root.PersistentFlags().BoolVar(&noColor, "no-color", false,
		"print without styling (NO_COLOR in the environment does the same)")

	// One line a script can parse (`nan --version | cut -d' ' -f2`), then the
	// toolchain. See build.Line for why this is not the banner.
	root.Version = build.Version
	root.SetVersionTemplate(build.Line() + "\n" + build.Detail() + "\n")

	// Lazily, and not only in PersistentPreRunE: cobra runs just the CLOSEST
	// PersistentPreRunE up the chain.
	get := func() *app {
		if a == nil {
			a = newApp(asJSON, noColor)
		}
		return a
	}
	root.AddCommand(
		authCmd,
		meCmd,
		metricsCmd,
		newSetupCmd(get),
		runCmd,
		runsCmd,
		automationsCmd,
		approvalsCmd,
		secretsCmd,
		notificationsCmd,
	)
	return root
}

// ExitCode is how a failed command ends. `nan run` and `nan runs` have a
// documented table, and a script reads their codes: there anything that is
// not already an ExitError is cobra refusing the arguments - an unknown
// flag, a missing id, two flags that exclude each other - which is a usage
// error. Every other command exits 1.
func ExitCode(cmd *cobra.Command, err error) int {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	if cmd != nil && cmd.Annotations[sysexitsAnnotation] != "" {
		return exitUsage
	}
	return 1
}

// Silent reports whether an error has nothing to print: `--detach` and a
// Ctrl-C that left a run going end with a code and no message.
func Silent(err error) bool {
	var exit *ExitError
	return errors.As(err, &exit) && exit.Err == nil
}

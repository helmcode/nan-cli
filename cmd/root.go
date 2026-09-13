package cmd

import (
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
		return tui.Run()
	},
}

func init() {
	// SilenceUsage covers runtime failures, but a mistyped flag IS a usage
	// mistake and the flag list is the answer to it.
	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		c.Println(c.UsageString())
		return err
	})

	// The same wordmark the About tab draws, so `--version` and the panel are
	// recognisably the same program. This one is framed and stacked, which a
	// command that prints once and exits can afford and a tab cannot.
	rootCmd.SetVersionTemplate("\n" + tui.WelcomeStacked("  ") + "\n")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

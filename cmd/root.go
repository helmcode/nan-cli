package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/nxssie/nan-cli/internal/tui"
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

	const (
		violet = "\033[38;2;167;139;250m"
		dim    = "\033[38;2;113;113;122m"
		bold   = "\033[1m"
		reset  = "\033[0m"
	)
	rootCmd.SetVersionTemplate(
		"\n  " + bold + violet + "nan" + reset +
			"  " + dim + "v{{.Version}}" + reset + "\n" +
			"  " + dim + "nan.builders cloud CLI" + reset + "\n" +
			"  " + dim + "by @Nxssie" + reset + "\n\n",
	)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

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
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Run()
	},
}

func init() {
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

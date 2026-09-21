package cmd

import (
	"fmt"

	"github.com/nxssie/nan-cli/internal/session"
	"github.com/spf13/cobra"
)

var keyCmd = &cobra.Command{
	Use:   "key",
	Short: "Manage your API key",
}

var keyPrintCmd = &cobra.Command{
	Use:   "print",
	Short: "Write your API key to stdout for another tool to read",
	Args:  cobra.NoArgs,
	RunE:  runKeyPrint,
}

func init() {
	rootCmd.AddCommand(keyCmd)
	keyCmd.AddCommand(keyPrintCmd)
}

func runKeyPrint(cmd *cobra.Command, args []string) error {
	sess, err := session.Load()
	if err != nil {
		return err
	}
	// Tool configs read this command's stdout, so an empty key printed as an
	// empty line would look like a working credential to them.
	if sess.APIKey == "" {
		return fmt.Errorf("no api key saved")
	}
	fmt.Fprintln(cmd.OutOrStdout(), sess.APIKey)
	return nil
}

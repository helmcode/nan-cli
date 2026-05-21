package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/session"
)

var meCmd = &cobra.Command{
	Use:   "me",
	Short: "Show your profile",
	RunE:  runMe,
}

func init() {
	rootCmd.AddCommand(meCmd)
}

func runMe(cmd *cobra.Command, args []string) error {
	sess, err := session.Load()
	if err != nil {
		return err
	}

	client := api.New(sess.Token)
	me, err := client.GetMe()
	if err != nil {
		return err
	}

	out, _ := json.MarshalIndent(me, "", "  ")
	fmt.Println(string(out))
	return nil
}

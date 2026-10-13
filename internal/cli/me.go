package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var meCmd = &cobra.Command{
	Use:   "me",
	Short: "Show your profile",
	RunE:  runMe,
}

func runMe(cmd *cobra.Command, _ []string) error {
	sess, err := requireSession("nan me")
	if err != nil {
		return err
	}

	client := newAPIClient(sess.Token)
	me, err := client.Me(cmd.Context())
	if err != nil {
		return err
	}

	out, _ := json.MarshalIndent(me, "", "  ")
	fmt.Println(string(out))
	return nil
}

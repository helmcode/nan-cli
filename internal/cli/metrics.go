package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Metrics and usage information",
}

var metricsUsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show usage metrics",
	RunE:  runMetricsUsage,
}

func init() {
	metricsCmd.AddCommand(metricsUsageCmd)
}

func runMetricsUsage(cmd *cobra.Command, _ []string) error {
	sess, err := requireSession("nan metrics usage")
	if err != nil {
		return err
	}

	client := newAPIClient(sess.Token)
	// The typed answer, not the platform's JSON as it came: like `nan me`,
	// this prints what was chosen to show, and a field the platform adds
	// for itself tomorrow does not reach the member's terminal or logs.
	usage, err := client.Usage(cmd.Context())
	if err != nil {
		return err
	}

	out, _ := json.MarshalIndent(usage, "", "  ")
	fmt.Println(string(out))
	return nil
}

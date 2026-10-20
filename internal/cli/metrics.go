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
	usage, err := client.UsageRaw(cmd.Context())
	if err != nil {
		return err
	}

	out, _ := json.MarshalIndent(usage, "", "  ")
	fmt.Println(string(out))
	return nil
}

package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/session"
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
	rootCmd.AddCommand(metricsCmd)
	metricsCmd.AddCommand(metricsUsageCmd)
}

func runMetricsUsage(cmd *cobra.Command, args []string) error {
	sess, err := session.Load()
	if err != nil {
		return err
	}

	client := api.New(sess.Token)
	usage, err := client.GetMetricsUsage()
	if err != nil {
		return err
	}

	out, _ := json.MarshalIndent(usage, "", "  ")
	fmt.Println(string(out))
	return nil
}

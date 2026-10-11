package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
)

var (
	approvalsState string
	approvalsLimit int
	approvalsJSON  bool
)

const approveInPortal = "there is no approve command in the CLI: approve in the portal at " + runs.ApprovalsURL +
	" (approving needs a browser sign-in, so an agent or a CI job that can read this machine's session or tokens can never approve its own actions). " +
	"Read the proposal with: nan runs proposal <run-id>; reject it with: nan runs reject <run-id>"

var approvalsCmd = &cobra.Command{
	Use:   "approvals",
	Short: "See the proposals waiting for your approval",
	Long: `See the automation runs whose proposed actions wait for your approval.

Read a proposal with nan approvals show <run-id> (the same as nan runs
proposal), and reject it with nan runs reject <run-id>. Approving happens
only in the portal, at ` + runs.ApprovalsURL + `: it needs you
signed in through the browser, so that nothing that can read this machine's
CLI session or tokens (an agent in a workspace, a CI job) can approve the
actions an agent proposed.

Authentication is the same as nan run (see nan run --help); a platform token
needs the runs scope.

Docs: https://nan.builders/docs/runs`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && (args[0] == "approve" || args[0] == "accept") {
			return usageErrorf("%s", approveInPortal)
		}
		if len(args) > 0 {
			return usageErrorf("unknown command %q for \"nan approvals\"; try: nan approvals --help", runs.Truncate(runs.SanitizeLine(args[0]), 64))
		}
		return cmd.Help()
	},
}

var approvalsLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List approvals, pending ones by default",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		state, err := approvalsStateParam(approvalsState)
		if err != nil {
			return err
		}
		if approvalsLimit < 1 || approvalsLimit > 100 {
			return usageErrorf("--limit must be between 1 and 100")
		}
		return withRunsEnv(func(env *runsEnv) error { return doApprovalsLs(cmd.Context(), env, state) })
	},
}

var approvalsShowCmd = &cobra.Command{
	Use:         "show <run-id>",
	Short:       "Print a run's proposal (the same as nan runs proposal)",
	Args:        oneRunID,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error {
			return doRunsProposal(cmd.Context(), env, args[0], proposalDiff, proposalJSON)
		})
	},
}

func init() {
	rootCmd.AddCommand(approvalsCmd)
	approvalsCmd.AddCommand(approvalsLsCmd, approvalsShowCmd)
	approvalsCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	approvalsLsCmd.Flags().StringVar(&approvalsState, "state", api.ApprovalPending, "Only approvals in this state: pending, approved, rejected, expired, cancelled, auto, or all")
	approvalsLsCmd.Flags().IntVar(&approvalsLimit, "limit", 20, "How many approvals to list (1-100)")
	approvalsLsCmd.Flags().BoolVar(&approvalsJSON, "json", false, "Print the API's list object as JSON")

	approvalsShowCmd.Flags().BoolVar(&proposalDiff, "diff", false, "Also print the full diff, when one is stored")
	approvalsShowCmd.Flags().BoolVar(&proposalJSON, "json", false, "Print the approval object (proposal included) as JSON")
}

// approvalsStateParam is the query value for --state: "all" is no filter.
func approvalsStateParam(state string) (string, error) {
	if state == "all" {
		return "", nil
	}
	for _, s := range api.ApprovalStates {
		if s == state {
			return state, nil
		}
	}
	return "", usageErrorf("--state must be one of %s, or all", strings.Join(api.ApprovalStates, ", "))
}

func doApprovalsLs(ctx context.Context, env *runsEnv, state string) error {
	list, err := env.client.ListApprovals(ctx, api.ListApprovalsParams{State: state, Limit: approvalsLimit})
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return exitf(exitAuth, "automations are not enabled for your account")
		}
		return apiExit(err, env.cred)
	}
	if approvalsJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		if state == api.ApprovalPending {
			fmt.Fprintln(env.stderr, "Nothing is waiting for your approval.")
		} else {
			fmt.Fprintln(env.stderr, "No approvals match.")
		}
		return nil
	}
	for i := range list.Data {
		env.nameAutomation(ctx, list.Data[i].Automation)
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tSTATE\tAUTOMATION\tWAITING FOR APPROVAL\tEXPIRES")
	for _, a := range list.Data {
		expires := "-"
		if a.ExpiresAt != nil && a.State == api.ApprovalPending {
			expires = localTime(*a.ExpiresAt)
		}
		automation := runs.SanitizeLine(a.Automation.Label())
		if automation == "" {
			automation = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			runs.SanitizeLine(a.RunID),
			runs.SanitizeLine(a.State),
			runs.Truncate(automation, 40),
			runs.Truncate(waitingKinds(a), 60),
			expires)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if list.NextCursor != nil {
		fmt.Fprintf(env.stderr, "showing the %d most recent; raise --limit (up to 100) for more\n", len(list.Data))
	}
	if state == api.ApprovalPending {
		fmt.Fprintf(env.stderr, "\nreview one: nan runs proposal <run-id>\napprove:    in the portal, %s (approving needs a browser sign-in)\nreject:     nan runs reject <run-id>\n", runs.ApprovalsURL)
	}
	return nil
}

// waitingKinds is the kinds of the actions an approval covers: those from
// the first one that needs approval on, without repeats.
func waitingKinds(a api.Approval) string {
	if a.Proposal == nil || a.ApprovalFrom == nil || *a.ApprovalFrom < 0 {
		return "-"
	}
	var kinds []string
	seen := map[string]bool{}
	for i, act := range a.Proposal.Actions {
		if i < *a.ApprovalFrom || seen[act.Kind] {
			continue
		}
		seen[act.Kind] = true
		kinds = append(kinds, runs.SanitizeLine(act.Kind))
	}
	if len(kinds) == 0 {
		return "-"
	}
	return strings.Join(kinds, ", ")
}

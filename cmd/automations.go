package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
)

// maxInputBytes is the most input an automation run takes (the platform's
// MaxInputPayloadBytes), checked here so a mistake costs a clear message.
const maxInputBytes = 48 * 1024

var (
	autoLsJSON        bool
	autoShowJSON      bool
	autoRunInput      string
	autoRunDetach     bool
	autoRunJSON       bool
	autoRunIdemKey    string
	autoDeliveryLimit int
	autoDeliveryJSON  bool
)

// automationVerbHints answer the verbs a member may reach for that are not
// commands, with the one that is.
var automationVerbHints = map[string]string{
	"new": "create", "add": "create", "update": "edit", "set": "edit",
	"rotate": "triggers rotate",
}

var automationsCmd = &cobra.Command{
	Use:   "automations",
	Short: "List, inspect and run your automations",
	Long: `Create, change, list, inspect and run your automations, manage their
triggers (webhooks, schedules), and read their webhook delivery log.

An automation is a saved agent task: a workspace, an agent, instructions,
the secrets it receives, and its triggers (webhooks, schedules). Its runs
can propose actions on your repository (a push, a pull request, a comment)
that apply automatically or wait for your approval, as you configured.

Approving a proposal stays portal-only, signed in through the browser at
` + runs.PortalURL + `: a credential an agent or a script can hold
must never approve what an agent proposed.

Authentication of ls, show, run and deliveries is the same as nan run (see
nan run --help): a platform token needs the automations:read scope to list
and inspect, and automations:run to start a run. Workspace names are looked
up with workspaces:read; without it the workspace's id is shown instead.

Creating, changing and deleting automations and triggers:
` + writeCredentialHelp + `

Docs: https://nan.builders/docs/runs`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			shown := runs.Truncate(runs.SanitizeLine(args[0]), 64)
			if hint, ok := automationVerbHints[args[0]]; ok {
				return usageErrorf("there is no nan automations %s; try: nan automations %s", shown, hint)
			}
			return usageErrorf("unknown command %q for \"nan automations\"; try: nan automations --help", shown)
		}
		return cmd.Help()
	},
}

var automationsLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List your automations",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error { return doAutomationsLs(cmd.Context(), env) })
	},
}

var automationsShowCmd = &cobra.Command{
	Use:         "show <id|name>",
	Short:       "Show one automation: its settings, secrets (names only) and triggers",
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error { return doAutomationsShow(cmd.Context(), env, args[0]) })
	},
}

var automationsRunCmd = &cobra.Command{
	Use:   "run <id|name> [--input @FILE | --input - | --input JSON]",
	Short: "Start a run of an automation and stream it",
	Long: `Start a run of an automation now, with its saved instructions, secrets and
approval settings, and stream what it does.

--input passes a JSON object to the run: @FILE reads it from a file, -
from stdin, or give the JSON itself. The agent receives it as untrusted data
next to the automation's instructions, which it never replaces. Up to 48 KiB.
Do not put secrets in it: the agent reads it; assign secrets to the automation\ninstead (nan automations edit, or the portal).

When the run proposes actions that need your approval, it stops in
awaiting_approval and this command exits 10: read the proposal with nan runs
proposal <id>, approve it in the portal, or reject it with nan runs reject
<id>.

Exit codes are those of nan run (see nan run --help), plus 10 for awaiting
approval. A platform token needs the automations:run scope.

Docs: https://nan.builders/docs/runs`,
	Example: `  nan automations run "PR review" --input '{"pr": 42}'
  nan automations run nightly-triage --detach
  gh api repos/acme/api/pulls/42 | nan automations run "PR review" --input -`,
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if autoRunIdemKey != "" && !idempotencyPattern.MatchString(autoRunIdemKey) {
			return usageErrorf("--idempotency-key must be 1-128 of letters, digits and . _ : -")
		}
		input, err := readAutomationInput(promptInput{r: os.Stdin, tty: isTerminal(os.Stdin), notice: os.Stderr}, autoRunInput)
		if err != nil {
			return err
		}
		return withRunsEnv(func(env *runsEnv) error {
			return doAutomationsRun(cmd.Context(), env, args[0], input)
		})
	},
}

var automationsDeliveriesCmd = &cobra.Command{
	Use:   "deliveries <id|name>",
	Short: "Show the webhook deliveries an automation received, newest first",
	Long: `Show the webhook deliveries an automation received in the last 30 days,
newest first, and what became of each one. No payload or headers are kept.

Outcomes: run_created, ignored (an event or actor filter did not match),
duplicate, loop_suppressed, skipped_active (a run was already going),
rate_limited, queue_full, too_large, not_accepted (your membership does not
include runs), bad_signature, secrets_unavailable.

Docs: https://nan.builders/docs/runs`,
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if autoDeliveryLimit < 1 || autoDeliveryLimit > 100 {
			return usageErrorf("--limit must be between 1 and 100")
		}
		return withRunsEnv(func(env *runsEnv) error { return doAutomationsDeliveries(cmd.Context(), env, args[0]) })
	},
}

func init() {
	rootCmd.AddCommand(automationsCmd)
	automationsCmd.AddCommand(automationsLsCmd, automationsShowCmd, automationsRunCmd, automationsDeliveriesCmd)
	automationsCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	automationsLsCmd.Flags().BoolVar(&autoLsJSON, "json", false, "Print the API's list object as JSON")
	automationsShowCmd.Flags().BoolVar(&autoShowJSON, "json", false, "Print the automation object as JSON")

	f := automationsRunCmd.Flags()
	f.StringVar(&autoRunInput, "input", "", `JSON object for the run: @FILE, "-" for stdin, or the JSON itself`)
	f.BoolVar(&autoRunDetach, "detach", false, "Start the run, print its id and return without streaming")
	f.BoolVar(&autoRunJSON, "json", false, "Print events as JSON lines, then the run (with --detach: the run)")
	f.StringVar(&autoRunIdemKey, "idempotency-key", "", "Retry-safe key: the same key returns the run already created")

	automationsDeliveriesCmd.Flags().IntVar(&autoDeliveryLimit, "limit", 50, "How many deliveries to list (1-100)")
	automationsDeliveriesCmd.Flags().BoolVar(&autoDeliveryJSON, "json", false, "Print the deliveries as JSON")
}

// oneAutomation is cobra.ExactArgs(1) with a message that says what is
// missing.
func oneAutomation(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing automation id or name; see: nan automations ls")
	case len(args) > 1:
		return usageErrorf("one automation at a time (quote a name with spaces)")
	case strings.TrimSpace(args[0]) == "":
		return usageErrorf("empty automation name; see: nan automations ls")
	}
	return nil
}

// resolveAutomation turns an id or a name into the automation's id. An id is
// used as it is; a name is looked up among the member's automations.
func resolveAutomation(ctx context.Context, env *runsEnv, ref string) (string, error) {
	if runIDPattern.MatchString(ref) {
		return ref, nil
	}
	list, err := env.client.ListAutomations(ctx)
	if err != nil {
		return "", automationExit(err, ref, env.cred)
	}
	var folded []string
	for _, a := range list.Data {
		if a.Name == ref {
			return a.ID, nil
		}
		if strings.EqualFold(a.Name, ref) {
			folded = append(folded, a.Name)
		}
	}
	shown := runs.Truncate(runs.SanitizeLine(ref), 64)
	if len(folded) == 1 {
		return "", usageErrorf("no automation named %q; did you mean %q? See: nan automations ls", shown, runs.SanitizeLine(folded[0]))
	}
	return "", usageErrorf("no automation named %q; see: nan automations ls", shown)
}

// automationExit is apiExit with a 404 that names the automation.
func automationExit(err error, ref string, cred credential) *ExitError {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return usageErrorf("automation %q not found (or automations are not enabled for your account)", runs.Truncate(runs.SanitizeLine(ref), 64))
	}
	return apiExit(err, cred)
}

func doAutomationsLs(ctx context.Context, env *runsEnv) error {
	list, err := env.client.ListAutomations(ctx)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return exitf(exitAuth, "automations are not enabled for your account")
		}
		return apiExit(err, env.cred)
	}
	if autoLsJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintf(env.stderr, "No automations yet. Create one with nan automations create -f FILE, or in the portal: %s\n", runs.NewAutomationURL)
		return nil
	}
	for i := range list.Data {
		env.nameWorkspace(ctx, list.Data[i].Workspace)
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tWORKSPACE\tAGENT\tSTATUS\tAPPROVAL")
	for _, a := range list.Data {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			runs.SanitizeLine(a.ID),
			runs.Truncate(runs.SanitizeLine(a.Name), 40),
			automationWhere(a),
			runs.SanitizeLine(a.Agent),
			automationStatus(a),
			runs.Truncate(approvalSummary(a.ApprovalKinds), 40))
	}
	return tw.Flush()
}

// automationWhere is where an automation's runs execute, for a table.
func automationWhere(a api.Automation) string {
	if a.Executor == api.ExecutorEphemeral {
		return "ephemeral " + sizeShort(a.Size)
	}
	return runs.Truncate(runs.SanitizeLine(a.Workspace.Label()), 40)
}

func automationStatus(a api.Automation) string {
	if a.Enabled {
		return "enabled"
	}
	if a.DisabledReason != nil && *a.DisabledReason != "" && *a.DisabledReason != "member" {
		return "disabled (" + strings.ReplaceAll(runs.SanitizeLine(*a.DisabledReason), "_", " ") + ")"
	}
	return "disabled"
}

// approvalSummary says which actions wait for the member. None means every
// action applies automatically, pushes and pull requests included.
func approvalSummary(kinds []string) string {
	if len(kinds) == 0 {
		return "none (automatic)"
	}
	clean := make([]string, len(kinds))
	for i, k := range kinds {
		clean[i] = runs.SanitizeLine(k)
	}
	return strings.Join(clean, ", ")
}

func doAutomationsShow(ctx context.Context, env *runsEnv, ref string) error {
	id, err := resolveAutomation(ctx, env, ref)
	if err != nil {
		return err
	}
	a, err := env.client.GetAutomation(ctx, id)
	if err != nil {
		return automationExit(err, ref, env.cred)
	}
	if autoShowJSON {
		return printJSON(env.stdout, a, true)
	}
	env.nameWorkspace(ctx, a.Workspace)
	triggers, trigErr := env.client.ListTriggers(ctx, id)

	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("id", runs.SanitizeLine(a.ID))
	row("name", runs.SanitizeLine(a.Name))
	row("description", runs.Truncate(runs.SanitizeLine(a.Description), 200))
	row("status", automationStatus(*a))
	if a.Template != "" && a.Template != "custom" {
		row("template", runs.SanitizeLine(a.Template))
	}
	if a.Workspace != nil {
		row("workspace", runs.NameAndID(a.Workspace.Name, a.Workspace.ID))
	}
	if a.Executor == api.ExecutorEphemeral {
		row("executor", "ephemeral: a fresh container per run, size "+sizeLabel(a.Size))
	}
	agent := runs.SanitizeLine(a.Agent)
	if a.Model != nil && *a.Model != "" {
		agent += " · model " + runs.SanitizeLine(*a.Model)
	}
	row("agent", agent)
	row("inference", inferenceLabel(a.Inference))
	if a.Executor != api.ExecutorEphemeral {
		// An ephemeral run works in its fresh checkout: neither applies.
		row("cwd", runs.SanitizeLine(a.Cwd))
		worktree := "auto"
		if a.GitIsolation != nil {
			worktree = map[bool]string{true: "yes", false: "no"}[*a.GitIsolation]
		}
		row("worktree", worktree)
	}
	if a.TimeoutSeconds > 0 {
		row("timeout", (time.Duration(a.TimeoutSeconds) * time.Second).String())
	}
	if a.GitHubRepo != nil {
		row("repository", runs.SanitizeLine(*a.GitHubRepo))
	}
	row("approval", approvalSummary(a.ApprovalKinds))
	if len(a.ApprovalKinds) > 0 && a.ApprovalTTLSeconds > 0 {
		row("approval ttl", (time.Duration(a.ApprovalTTLSeconds) * time.Second).String())
	}
	if a.ConcurrencyPolicy != "" {
		row("concurrency", strings.ReplaceAll(runs.SanitizeLine(a.ConcurrencyPolicy), "_", " "))
	}
	if a.MaxRunsPerHour > 0 {
		row("runs per hour", fmt.Sprintf("up to %d", a.MaxRunsPerHour))
	}
	for _, s := range a.Secrets {
		row("secret", fmt.Sprintf("%s ← %s (%s)", runs.SanitizeLine(s.Env), runs.SanitizeLine(s.Secret), exposureNote(s.Exposure)))
	}
	if trigErr == nil {
		for _, t := range triggers.Data {
			row("trigger", triggerLine(t))
		}
	}
	row("updated", localTime(a.UpdatedAt))
	row("last changed from", updatedByLabel(a.UpdatedBy))
	if err := tw.Flush(); err != nil {
		return err
	}
	if trigErr != nil {
		fmt.Fprintf(env.stderr, "could not read the triggers: %s\n", runs.SanitizeLine(apiExit(trigErr, env.cred).Error()))
	}

	if a.Inference.BYOK() && trigErr == nil && hasWebhook(triggers.Data) {
		fmt.Fprintln(env.stdout, "\nnote: this automation runs on your own key, and a webhook starts it: what a delivery "+
			"carries is untrusted input to an agent that holds your key in its environment")
	}

	printBlock(env, "instructions", a.Instructions)
	fmt.Fprintf(env.stdout, "\nrun it: nan automations run %s\nchange it: nan automations edit %s -f FILE, or in the portal: %s\n",
		runs.SanitizeLine(a.ID), runs.SanitizeLine(a.ID), runs.AutomationURL(a.ID))
	return nil
}

func hasWebhook(ts []api.Trigger) bool {
	for _, t := range ts {
		if t.Kind == "webhook" && t.Enabled {
			return true
		}
	}
	return false
}

func exposureNote(exposure string) string {
	switch exposure {
	case "agent":
		return "the agent"
	case "actions":
		return "actions only, never the agent"
	}
	return runs.SanitizeLine(exposure)
}

func triggerLine(t api.Trigger) string {
	var b strings.Builder
	b.WriteString(runs.SanitizeLine(t.Kind))
	switch t.Kind {
	case "schedule":
		if t.Cron != nil {
			b.WriteString(" " + runs.SanitizeLine(*t.Cron))
		}
		if t.Timezone != nil {
			b.WriteString(" (" + runs.SanitizeLine(*t.Timezone) + ")")
		}
		if t.NextFireAt != nil && t.Enabled {
			b.WriteString(" · next " + localTime(*t.NextFireAt))
		}
	case "webhook":
		if t.SignatureScheme != nil {
			b.WriteString(" " + runs.SanitizeLine(*t.SignatureScheme))
		}
		if t.TokenPrefix != nil {
			b.WriteString(" · " + runs.SanitizeLine(*t.TokenPrefix) + "…")
		}
	}
	if !t.Enabled {
		b.WriteString(" · disabled")
	}
	return b.String()
}

// readAutomationInput reads --input: "" for none, "-" for stdin, @FILE, or
// the JSON itself. It has to be a JSON object; it goes out compacted.
func readAutomationInput(in promptInput, flag string) (json.RawMessage, error) {
	if flag == "" {
		return nil, nil
	}
	var (
		raw []byte
		err error
	)
	switch {
	case flag == "-":
		if in.tty && in.notice != nil {
			fmt.Fprintln(in.notice, "reading the input from stdin; end it with Ctrl-D")
		}
		if in.r == nil {
			return nil, usageErrorf("no stdin to read the input from")
		}
		raw, err = io.ReadAll(io.LimitReader(in.r, 4*maxInputBytes+1))
		if err != nil {
			return nil, usageErrorf("could not read the input from stdin: %v", err)
		}
	case strings.HasPrefix(flag, "@"):
		path := flag[1:]
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil, usageErrorf("could not read the input: %v", unwrapPath(openErr))
		}
		raw, err = io.ReadAll(io.LimitReader(f, 4*maxInputBytes+1))
		f.Close()
		if err != nil {
			return nil, usageErrorf("could not read the input: %v", unwrapPath(err))
		}
	default:
		raw = []byte(flag)
	}
	switch {
	case len(raw) > 4*maxInputBytes:
		return nil, usageErrorf("--input is over %d KiB, the most a run takes", maxInputBytes/1024)
	case !utf8.Valid(raw):
		return nil, usageErrorf("--input is not UTF-8 text")
	case !json.Valid(raw):
		return nil, usageErrorf("--input is not valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, usageErrorf("--input is not valid JSON")
	}
	if compact.Len() == 0 || compact.Bytes()[0] != '{' {
		return nil, usageErrorf("--input must be a JSON object, like {\"pr\": 42}")
	}
	if compact.Len() > maxInputBytes {
		return nil, usageErrorf("--input is over %d KiB, the most a run takes", maxInputBytes/1024)
	}
	return compact.Bytes(), nil
}

func doAutomationsRun(ctx context.Context, env *runsEnv, ref string, input json.RawMessage) error {
	id, err := resolveAutomation(ctx, env, ref)
	if err != nil {
		return err
	}
	run, replayed, err := env.client.RunAutomation(ctx, id, input, autoRunIdemKey)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "workspace_not_running" {
			return exitf(exitUnavailable, "the automation's workspace is not running; start it at %s and try again", runs.PortalURL)
		}
		return automationExit(err, ref, env.cred)
	}
	if !runIDPattern.MatchString(run.ID) {
		return exitf(exitUnavailable, "nan.builders answered with something that is not a run id")
	}
	if replayed {
		fmt.Fprintf(env.stderr, "run %s already exists for this idempotency key (%s)\n", run.ID, runs.SanitizeLine(run.State))
	}
	if autoRunDetach {
		if autoRunJSON {
			if err := printJSON(env.stdout, run, true); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(env.stdout, run.ID)
			fmt.Fprintf(env.stderr, "run %s %s\n", run.ID, strings.ReplaceAll(runs.SanitizeLine(run.State), "_", " "))
			printFollowHints(env.stderr, run.ID)
		}
		return runExit(run)
	}
	if !replayed {
		// The platform names the automation by id; what the member typed
		// (a name, usually) reads better than a UUID they did not.
		name := ""
		if run.Automation != nil {
			name = runs.SanitizeLine(run.Automation.Name)
		}
		if name == "" {
			name = runs.Truncate(runs.SanitizeLine(ref), 64)
		}
		fmt.Fprintf(env.stderr, "run %s of %s queued on %s; Ctrl-C to detach\n", run.ID, name, runs.SanitizeLine(run.Workspace.Name))
	}
	return follow(ctx, env, run.ID, 0, autoRunJSON, true)
}

func doAutomationsDeliveries(ctx context.Context, env *runsEnv, ref string) error {
	id, err := resolveAutomation(ctx, env, ref)
	if err != nil {
		return err
	}
	list, err := env.client.ListDeliveries(ctx, id, autoDeliveryLimit)
	if err != nil {
		return automationExit(err, ref, env.cred)
	}
	if autoDeliveryJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintln(env.stderr, "No webhook deliveries in the last 30 days.")
		return nil
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RECEIVED\tOUTCOME\tRUN\tDELIVERY")
	for _, d := range list.Data {
		run := "-"
		if d.RunID != nil {
			run = runs.SanitizeLine(*d.RunID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			localTime(d.ReceivedAt),
			runs.SanitizeLine(d.Outcome),
			run,
			runs.Truncate(runs.SanitizeLine(d.DeliveryID), 40))
	}
	return tw.Flush()
}

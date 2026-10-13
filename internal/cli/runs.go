package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
)

// sysexitsAnnotation marks the commands whose exit codes are the documented
// table in `nan run --help`. On them a flag mistake exits 64, not 1.
const sysexitsAnnotation = "nan/sysexits"

var sysexits = map[string]string{sysexitsAnnotation: "true"}

var (
	lsWorkspace string
	lsState     string
	lsLimit     int
	lsJSON      bool
	showJSON    bool
	logsFollow  bool
	logsAfter   int64
	logsJSON    bool
	cancelJSON  bool

	proposalDiff bool
	proposalJSON bool
	rejectReason string
	rejectJSON   bool
)

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List, inspect, follow and cancel agent runs",
	Long: `List, inspect, follow and cancel the agent runs started with nan run, from
the portal, or by your automations. These commands authenticate the same
way as nan run: NAN_TOKEN, --token-file PATH, a token saved with nan auth
login --api-token, or your nan auth login session (the full order is in
nan run --help). Automation names are looked up with automations:read;
without it the automation's id is shown instead.

A run of an automation can stop in awaiting_approval: the agent is done and
the actions it proposed (a push, a pull request, a comment) wait for you.
Read the proposal with nan runs proposal <id>, reject it with nan runs
reject <id>, and approve it in the portal. There is no approve command:
approving needs you signed in through the browser, so that an agent or a CI
job that can read this machine's session or tokens can never approve its
own actions.

Docs: https://nan.builders/docs/runs`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && (args[0] == "approve" || args[0] == "accept") {
			id := "<id>"
			if len(args) > 1 && runIDPattern.MatchString(args[1]) {
				id = args[1]
			}
			return usageErrorf("there is no nan runs approve: approve on the run's page in the portal (inbox: %s); approving needs a browser sign-in. Read the proposal and its link with: nan runs proposal %s", runs.ApprovalsURL, id)
		}
		if len(args) > 0 {
			return usageErrorf("unknown command %q for \"nan runs\"; try: nan runs --help", runs.Truncate(runs.SanitizeLine(args[0]), 64))
		}
		return cmd.Help()
	},
}

var runsLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List your runs, newest first",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := checkLsFlags(); err != nil {
			return err
		}
		return withRunsEnv(func(env *runsEnv) error { return doRunsLs(cmd.Context(), env) })
	},
}

var runsShowCmd = &cobra.Command{
	Use:         "show <id>",
	Short:       "Show one run: state, result, prompt",
	Args:        oneRunID,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error { return doRunsShow(cmd.Context(), env, args[0]) })
	},
}

var runsLogsCmd = &cobra.Command{
	Use:   "logs <id>",
	Short: "Print a run's events (-f to follow until it ends)",
	Long: `Print a run's events. Their types: started, message (what the agent
says), tool_call and tool_result, log (agent stdout and stderr), artifact,
error, truncated (output was cut), and finished.

With -f, follow the run until it ends and exit with its code, the same codes
as nan run: 0 succeeded, 1 failed, 2 timed out, 3 cancelled, 4 workspace
not set up for it, 10 awaiting approval. Ctrl-C stops following; the run
goes on.

With --json, print one event per line as a JSON object (seq, ts, type,
data), then, with -f, the finished run. The event types and their data are
described in the docs. --after N starts after event number N, to pick up
where an earlier read stopped.

Docs: https://nan.builders/docs/runs`,
	Args:        oneRunID,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error { return doRunsLogs(cmd.Context(), env, args[0]) })
	},
}

var runsCancelCmd = &cobra.Command{
	Use:         "cancel <id>",
	Short:       "Cancel a queued or running run",
	Args:        oneRunID,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error { return doRunsCancel(cmd.Context(), env, args[0]) })
	},
}

var runsProposalCmd = &cobra.Command{
	Use:   "proposal <id>",
	Short: "Print the actions a run proposed, and where to approve them",
	Long: `Print what an automation run proposed to do - pushes, pull requests,
comments, reviews, labels - as plain text: which actions apply automatically,
which wait for your approval, the files a push changes, and the bodies the
agent wrote. --diff prints the full diff too, when one is stored (only when
your approval covers a push); without one it says so on stderr and exits 0.

Everything here was written by the agent and is shown as text, never run or
rendered. Values of your secrets are shown as [masked].

Approve in the portal (the run's page): approving needs a browser sign-in.
Reject from here with nan runs reject <id>.

Docs: https://nan.builders/docs/runs`,
	Args:        oneRunID,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRunsEnv(func(env *runsEnv) error {
			return doRunsProposal(cmd.Context(), env, args[0], proposalDiff, proposalJSON)
		})
	},
}

var runsRejectCmd = &cobra.Command{
	Use:   "reject <id>",
	Short: "Reject the proposal of a run awaiting approval",
	Long: `Reject the proposal of a run awaiting approval: the run ends cancelled
(approval_rejected) and none of the actions waiting for approval is applied.
Actions that applied automatically before it stay applied.

Rejecting is always safe, so any credential that can read runs can do it.
Approving is not, and happens only in the portal.

Docs: https://nan.builders/docs/runs`,
	Args:        oneRunID,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkRejectReason(rejectReason); err != nil {
			return err
		}
		return withRunsEnv(func(env *runsEnv) error { return doRunsReject(cmd.Context(), env, args[0]) })
	},
}

func init() {
	runsCmd.AddCommand(runsLsCmd, runsShowCmd, runsLogsCmd, runsCancelCmd, runsProposalCmd, runsRejectCmd)
	runsCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	runsLsCmd.Flags().StringVar(&lsWorkspace, "ws", "", "Only runs in this workspace")
	runsLsCmd.Flags().StringVar(&lsState, "state", "", "Only runs in this state: queued, starting, running, awaiting_approval, succeeded, failed, cancelled, timed_out")
	runsLsCmd.Flags().IntVar(&lsLimit, "limit", 20, "How many runs to list (1-100)")
	runsLsCmd.Flags().BoolVar(&lsJSON, "json", false, "Print the API's list object as JSON")

	runsShowCmd.Flags().BoolVar(&showJSON, "json", false, "Print the run object as JSON")

	runsLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow the run until it ends, and exit with its code")
	runsLogsCmd.Flags().Int64Var(&logsAfter, "after", 0, "Start after this event number")
	runsLogsCmd.Flags().BoolVar(&logsJSON, "json", false, "Print events as JSON lines (with -f, then the finished run)")

	runsCancelCmd.Flags().BoolVar(&cancelJSON, "json", false, "Print the run object as JSON")

	runsProposalCmd.Flags().BoolVar(&proposalDiff, "diff", false, "Also print the full diff, when one is stored")
	runsProposalCmd.Flags().BoolVar(&proposalJSON, "json", false, "Print the approval object (proposal included) as JSON")

	runsRejectCmd.Flags().StringVar(&rejectReason, "reason", "", "Why, for your own record (up to 500 characters)")
	runsRejectCmd.Flags().BoolVar(&rejectJSON, "json", false, "Print the run object as JSON")
}

func withRunsEnv(fn func(*runsEnv) error) error {
	env, err := newRunsEnv()
	if err != nil {
		return err
	}
	return runtimeError(fn(env))
}

// runtimeError keeps a failure that happened while running - stdout closed
// under us, say - from reading as a usage error, which is what exitCodeFor
// makes of any plain error from these commands.
func runtimeError(err error) error {
	if err == nil {
		return nil
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return err
	}
	return &ExitError{Code: 1, Err: err}
}

func checkRunID(id string) error {
	if !runIDPattern.MatchString(id) {
		return usageErrorf("%q is not a run id - ids look like 6f1c2a9b-…, see: nan runs ls", runs.Truncate(runs.SanitizeLine(id), 64))
	}
	return nil
}

// oneRunID is cobra.ExactArgs(1) with a message that says what is missing.
func oneRunID(_ *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing run id - see: nan runs ls")
	case len(args) > 1:
		return usageErrorf("one run id at a time")
	}
	return checkRunID(args[0])
}

func notFound(err error, id string, cred credential) *ExitError {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return usageErrorf("run %s not found", id)
	}
	return apiExit(err, cred)
}

var validStates = map[string]bool{
	api.StateQueued: true, api.StateStarting: true, api.StateRunning: true, api.StateAwaitingApproval: true,
	api.StateSucceeded: true, api.StateFailed: true, api.StateCancelled: true, api.StateTimedOut: true,
}

func checkLsFlags() error {
	if lsState != "" && !validStates[lsState] {
		return usageErrorf("--state must be one of queued, starting, running, awaiting_approval, succeeded, failed, cancelled, timed_out")
	}
	if lsLimit < 1 || lsLimit > 100 {
		return usageErrorf("--limit must be between 1 and 100")
	}
	return nil
}

func doRunsLs(ctx context.Context, env *runsEnv) error {
	list, err := env.client.ListRuns(ctx, api.ListRunsParams{Workspace: lsWorkspace, State: lsState, Limit: lsLimit})
	if err != nil {
		return apiExit(err, env.cred)
	}
	if lsJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		if lsWorkspace != "" || lsState != "" {
			_, _ = fmt.Fprintln(env.stderr, "No runs match.")
		} else {
			_, _ = fmt.Fprintln(env.stderr, "No runs yet. Start one with: nan run \"<prompt>\"")
		}
		return nil
	}
	for i := range list.Data {
		env.nameRunWorkspace(ctx, &list.Data[i])
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATE\tWORKSPACE\tAGENT\tCREATED\tPROMPT")
	for _, r := range list.Data {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			runs.SanitizeLine(r.ID),
			runs.SanitizeLine(r.State),
			runs.Truncate(runs.SanitizeLine(refLabel(r.Workspace.Name, r.Workspace.ID)), 40),
			runs.SanitizeLine(r.Agent),
			localTime(r.CreatedAt),
			runs.Truncate(runs.SanitizeLine(r.PromptPreview), 60))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if list.NextCursor != nil {
		if lsLimit < 100 {
			_, _ = fmt.Fprintf(env.stderr, "showing the %d most recent - raise --limit (up to 100) for more\n", len(list.Data))
		} else {
			_, _ = fmt.Fprintf(env.stderr, "showing the %d most recent - narrow it with --ws or --state\n", len(list.Data))
		}
	}
	return nil
}

func localTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return runs.SanitizeLine(ts)
	}
	return t.Local().Format("2006-01-02 15:04")
}

func doRunsShow(ctx context.Context, env *runsEnv, id string) error {
	if err := checkRunID(id); err != nil {
		return err
	}
	run, err := env.client.GetRun(ctx, id)
	if err != nil {
		return notFound(err, id, env.cred)
	}
	if showJSON {
		return printJSON(env.stdout, run, true)
	}
	env.nameRun(ctx, run)

	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			_, _ = fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	state := runs.SanitizeLine(run.State)
	if run.QueuePosition != nil {
		state += fmt.Sprintf(" (position %d)", *run.QueuePosition)
	}
	if run.CancelRequested && !api.Terminal(run.State) {
		state += ", cancel requested"
	}
	row("id", runs.SanitizeLine(run.ID))
	row("state", state)
	if run.ErrorCode != nil {
		row("error", runs.SanitizeLine(*run.ErrorCode))
	}
	if run.ExitCode != nil {
		row("exit code", fmt.Sprint(*run.ExitCode))
	}
	row("workspace", runs.NameAndID(run.Workspace.Name, run.Workspace.ID))
	agent := runs.SanitizeLine(run.Agent)
	if run.Model != nil && *run.Model != "" {
		agent += " · model " + runs.SanitizeLine(*run.Model)
	}
	row("agent", agent)
	row("trigger", runs.SanitizeLine(run.Trigger))
	if run.Phase != "" && run.Phase != "run" {
		row("phase", runs.SanitizeLine(run.Phase)+phaseNote(run.Phase))
	}
	if run.Automation != nil {
		row("automation", runs.NameAndID(run.Automation.Name, run.Automation.ID))
	}
	if run.GitHubRepo != nil {
		row("repository", runs.SanitizeLine(*run.GitHubRepo))
	}
	for _, sec := range run.Secrets {
		row("secret", runs.SanitizeLine(sec.Env)+" ("+exposureNote(sec.Exposure)+")")
	}
	if run.ParentRunID != nil {
		row("parent run", runs.SanitizeLine(*run.ParentRunID))
	}
	if a := run.Approval; a != nil {
		approval := runs.SanitizeLine(a.State)
		if a.State == api.ApprovalPending && a.ExpiresAt != nil {
			approval += ", expires " + localTime(*a.ExpiresAt)
		}
		if a.Proposal != nil {
			approval += fmt.Sprintf(" · %d actions (nan runs proposal %s)", len(a.Proposal.Actions), runs.SanitizeLine(run.ID))
		}
		row("approval", approval)
	}
	for _, apply := range applyRuns(run) {
		row("apply run", runs.SanitizeLine(apply))
	}
	row("cwd", runs.SanitizeLine(run.Cwd))
	worktree := "auto"
	if run.GitIsolation != nil {
		worktree = map[bool]string{true: "yes", false: "no"}[*run.GitIsolation]
	}
	row("worktree", worktree)
	if run.TimeoutSeconds > 0 {
		row("timeout", (time.Duration(run.TimeoutSeconds) * time.Second).String())
	}
	row("created", localTime(run.CreatedAt))
	if run.StartedAt != nil {
		row("started", localTime(*run.StartedAt))
	}
	if run.FinishedAt != nil {
		row("finished", localTime(*run.FinishedAt))
	}
	if res := run.Result; res != nil {
		row("branch", runs.SanitizeLine(res.Branch))
		row("commit", runs.SanitizeLine(res.Commit))
		if res.ChangedFiles > 0 {
			row("changed files", fmt.Sprint(res.ChangedFiles))
		}
		for _, a := range res.Artifacts {
			row("artifact", fmt.Sprintf("%s (%d bytes)", runs.SanitizeLine(a.Path), a.Bytes))
		}
		for i, a := range res.ActionsApplied {
			outcome := "ok"
			if !a.OK {
				outcome = "failed"
			}
			if a.Ref != "" {
				outcome += " · " + runs.Truncate(runs.SanitizeLine(a.Ref), 80)
			}
			row("action", fmt.Sprintf("%d. %s %s", i+1, runs.SanitizeLine(a.Kind), outcome))
		}
	}
	if u := run.Usage; u != nil && (u.TokensIn != nil || u.TokensOut != nil) {
		row("tokens", fmt.Sprintf("%s in, %s out", optInt(u.TokensIn), optInt(u.TokensOut)))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if run.State == api.StateAwaitingApproval {
		_, _ = fmt.Fprintln(env.stdout)
		_, _ = fmt.Fprintln(env.stdout, "awaiting your approval:")
		_, _ = fmt.Fprintln(env.stdout, runs.ApprovalHint(run))
	}

	prompt := run.Prompt
	if prompt == "" {
		prompt = run.PromptPreview
	}
	printBlock(env, "prompt", prompt)
	if run.Result != nil {
		printBlock(env, "summary", run.Result.Summary)
	}
	return nil
}

func optInt(v *int64) string {
	if v == nil {
		return "?"
	}
	return fmt.Sprint(*v)
}

func printBlock(env *runsEnv, title, text string) {
	text = strings.TrimRight(runs.Sanitize(text), "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	_, _ = fmt.Fprintf(env.stdout, "\n%s:\n", title)
	for _, l := range strings.Split(text, "\n") {
		_, _ = fmt.Fprintf(env.stdout, "  %s\n", l)
	}
}

func doRunsLogs(ctx context.Context, env *runsEnv, id string) error {
	if err := checkRunID(id); err != nil {
		return err
	}
	if logsAfter < 0 {
		return usageErrorf("--after cannot be negative")
	}
	if logsFollow {
		return follow(ctx, env, id, logsAfter, logsJSON, false)
	}

	renderer := &runs.Renderer{Out: env.stdout}
	after := logsAfter
	var done bool
	for {
		page, err := env.client.EventsPage(ctx, id, after, 500)
		if err != nil {
			return notFound(err, id, env.cred)
		}
		for _, ev := range page.Data {
			if logsJSON {
				if err := printJSON(env.stdout, ev, false); err != nil {
					return err
				}
			} else {
				renderer.Event(ev)
			}
		}
		done = page.Done
		if page.Done || len(page.Data) == 0 || page.NextAfter <= after {
			break
		}
		after = page.NextAfter
	}
	renderer.Flush()
	if !done {
		_, _ = fmt.Fprintf(env.stderr, "run %s is still going - follow it with: nan runs logs %s -f\n", id, id)
	}
	return nil
}

func doRunsCancel(ctx context.Context, env *runsEnv, id string) error {
	if err := checkRunID(id); err != nil {
		return err
	}
	run, changed, err := env.client.CancelRun(ctx, id)
	if err != nil {
		return notFound(err, id, env.cred)
	}
	if cancelJSON {
		return printJSON(env.stdout, run, true)
	}
	if !changed {
		_, _ = fmt.Fprintf(env.stdout, "run %s had already ended: %s\n", id, runs.SanitizeLine(run.State))
		return nil
	}
	_, _ = fmt.Fprintf(env.stdout, "cancel requested for run %s - it stops within seconds; check with: nan runs show %s\n", id, id)
	return nil
}

// phaseNote says what a phase means, once, where it is shown.
func phaseNote(phase string) string {
	switch phase {
	case "propose":
		return " (the agent proposes actions; they apply after approval or automatically)"
	case "apply":
		return " (applies approved or automatic actions; no agent)"
	}
	return ""
}

// applyRuns lists a run's apply runs, oldest first, without repeats: the
// platform may name them in the run and in its approval.
func applyRuns(run *api.Run) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id *string) {
		if id != nil && *id != "" && !seen[*id] {
			seen[*id] = true
			ids = append(ids, *id)
		}
	}
	if a := run.Approval; a != nil {
		add(a.PrefixApplyRunID)
	}
	for i := range run.ApplyRunIDs {
		add(&run.ApplyRunIDs[i])
	}
	if a := run.Approval; a != nil {
		add(a.ApplyRunID)
	}
	add(run.ApplyRunID)
	return ids
}

func doRunsProposal(ctx context.Context, env *runsEnv, id string, withDiff, asJSON bool) error {
	if err := checkRunID(id); err != nil {
		return err
	}
	run, err := env.client.GetRun(ctx, id)
	if err != nil {
		return notFound(err, id, env.cred)
	}
	if run.Approval == nil {
		if asJSON {
			return usageErrorf("run %s has no proposal", id)
		}
		return usageErrorf("run %s has no proposal: only automation runs that proposed actions have one (state: %s)", id, runs.SanitizeLine(run.State))
	}
	if asJSON {
		return printJSON(env.stdout, run.Approval, true)
	}
	env.nameAutomation(ctx, run.Automation)
	runs.Proposal(env.stdout, run)
	if !withDiff {
		return nil
	}
	if !run.Approval.HasDiff {
		_, _ = fmt.Fprintln(env.stderr, "no diff is stored for this proposal: one is kept only when your approval covers a push")
		return nil
	}
	diff, err := env.client.ProposalDiff(ctx, id)
	if err != nil {
		return notFound(err, id, env.cred)
	}
	_, _ = fmt.Fprintln(env.stdout)
	_, _ = fmt.Fprintln(env.stdout, "diff:")
	runs.Diff(env.stdout, diff)
	return nil
}

// maxRejectReason is the platform's limit, in characters.
const maxRejectReason = 500

func checkRejectReason(reason string) error {
	if utf8.RuneCountInString(reason) > maxRejectReason {
		return usageErrorf("--reason is over %d characters", maxRejectReason)
	}
	if !utf8.ValidString(reason) {
		return usageErrorf("--reason is not UTF-8 text")
	}
	for _, r := range reason {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return usageErrorf("--reason contains a control character")
		}
	}
	return nil
}

func doRunsReject(ctx context.Context, env *runsEnv, id string) error {
	if err := checkRunID(id); err != nil {
		return err
	}
	run, err := env.client.RejectRun(ctx, id, rejectReason)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "not_awaiting_approval" {
			return usageErrorf("run %s is not awaiting approval; see its state with: nan runs show %s", id, id)
		}
		return notFound(err, id, env.cred)
	}
	if rejectJSON {
		return printJSON(env.stdout, run, true)
	}
	_, _ = fmt.Fprintf(env.stdout, "rejected: run %s is %s; none of the actions waiting for approval will be applied (actions that applied automatically before it stay applied)\n", id, strings.ReplaceAll(runs.SanitizeLine(run.State), "_", " "))
	return nil
}

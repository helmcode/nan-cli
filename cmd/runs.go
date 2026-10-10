package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
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
)

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List, inspect, follow and cancel agent runs",
	Long: `List, inspect, follow and cancel the agent runs started with nan run, from
the portal, or by your automations. They authenticate the same way as nan
run: NAN_TOKEN, --token-file PATH, your nan auth login session, or a token
saved with nan auth login --api-token (see: nan run --help).

Docs: https://nan.builders/docs/runs`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageErrorf("unknown command %q for \"nan runs\" — try: nan runs --help", args[0])
		}
		return cmd.Help()
	},
}

var runsLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List your runs, newest first",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
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
not set up for it. Ctrl-C stops following; the run goes on.

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

func init() {
	rootCmd.AddCommand(runsCmd)
	runsCmd.AddCommand(runsLsCmd, runsShowCmd, runsLogsCmd, runsCancelCmd)
	runsCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	runsLsCmd.Flags().StringVar(&lsWorkspace, "ws", "", "Only runs in this workspace")
	runsLsCmd.Flags().StringVar(&lsState, "state", "", "Only runs in this state: queued, starting, running, succeeded, failed, cancelled, timed_out")
	runsLsCmd.Flags().IntVar(&lsLimit, "limit", 20, "How many runs to list (1-100)")
	runsLsCmd.Flags().BoolVar(&lsJSON, "json", false, "Print the API's list object as JSON")

	runsShowCmd.Flags().BoolVar(&showJSON, "json", false, "Print the run object as JSON")

	runsLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow the run until it ends, and exit with its code")
	runsLogsCmd.Flags().Int64Var(&logsAfter, "after", 0, "Start after this event number")
	runsLogsCmd.Flags().BoolVar(&logsJSON, "json", false, "Print events as JSON lines (with -f, then the finished run)")

	runsCancelCmd.Flags().BoolVar(&cancelJSON, "json", false, "Print the run object as JSON")
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
		return usageErrorf("%q is not a run id — ids look like 6f1c2a9b-…, see: nan runs ls", runs.Truncate(runs.SanitizeLine(id), 64))
	}
	return nil
}

// oneRunID is cobra.ExactArgs(1) with a message that says what is missing.
func oneRunID(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing run id — see: nan runs ls")
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
	api.StateQueued: true, api.StateStarting: true, api.StateRunning: true,
	api.StateSucceeded: true, api.StateFailed: true, api.StateCancelled: true, api.StateTimedOut: true,
}

func checkLsFlags() error {
	if lsState != "" && !validStates[lsState] {
		return usageErrorf("--state must be one of queued, starting, running, succeeded, failed, cancelled, timed_out")
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
			fmt.Fprintln(env.stderr, "No runs match.")
		} else {
			fmt.Fprintln(env.stderr, "No runs yet. Start one with: nan run \"<prompt>\"")
		}
		return nil
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tWORKSPACE\tAGENT\tCREATED\tPROMPT")
	for _, r := range list.Data {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			runs.SanitizeLine(r.ID),
			runs.SanitizeLine(r.State),
			runs.SanitizeLine(r.Workspace.Name),
			runs.SanitizeLine(r.Agent),
			localTime(r.CreatedAt),
			runs.Truncate(runs.SanitizeLine(r.PromptPreview), 60))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if list.NextCursor != nil {
		if lsLimit < 100 {
			fmt.Fprintf(env.stderr, "showing the %d most recent — raise --limit (up to 100) for more\n", len(list.Data))
		} else {
			fmt.Fprintf(env.stderr, "showing the %d most recent — narrow it with --ws or --state\n", len(list.Data))
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

	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
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
	row("workspace", runs.SanitizeLine(run.Workspace.Name))
	agent := runs.SanitizeLine(run.Agent)
	if run.Model != nil && *run.Model != "" {
		agent += " · model " + runs.SanitizeLine(*run.Model)
	}
	row("agent", agent)
	row("trigger", runs.SanitizeLine(run.Trigger))
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
	}
	if u := run.Usage; u != nil && (u.TokensIn != nil || u.TokensOut != nil) {
		row("tokens", fmt.Sprintf("%s in, %s out", optInt(u.TokensIn), optInt(u.TokensOut)))
	}
	if err := tw.Flush(); err != nil {
		return err
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
	fmt.Fprintf(env.stdout, "\n%s:\n", title)
	for _, l := range strings.Split(text, "\n") {
		fmt.Fprintf(env.stdout, "  %s\n", l)
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
	done := false
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
		fmt.Fprintf(env.stderr, "run %s is still going — follow it with: nan runs logs %s -f\n", id, id)
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
		fmt.Fprintf(env.stdout, "run %s had already ended: %s\n", id, runs.SanitizeLine(run.State))
		return nil
	}
	fmt.Fprintf(env.stdout, "cancel requested for run %s — it stops within seconds; check with: nan runs show %s\n", id, id)
	return nil
}

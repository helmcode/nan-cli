package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/spf13/cobra"
)

// The limits the platform enforces, checked here too so a mistake costs a
// clear message instead of a round trip and a 400.
const (
	maxPromptBytes = 32768
	minTimeout     = time.Minute
	maxTimeout     = 2 * time.Hour
)

var (
	modelPattern       = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	runIDPattern       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// How long after a Ctrl-C that detached a second one still means "cancel".
var cancelWindow = 2 * time.Second

type runOptions struct {
	workspace      string
	agent          string
	model          string
	cwd            string
	worktree       bool
	noWorktree     bool
	timeout        time.Duration
	timeoutSet     bool
	detach         bool
	json           bool
	idempotencyKey string
	file           string
}

var runOpts runOptions

var runCmd = &cobra.Command{
	Use:   `run [flags] ( "prompt" | -f FILE | - )`,
	Short: "Run an agent in one of your workspaces",
	Long: `Run an agent in one of your workspaces and stream what it does.

The prompt is the argument, the contents of -f FILE, or stdin when the
argument is "-". The run happens in the workspace, not on this machine: it
goes on if this command stops, and its log stays on nan.builders.

Ctrl-C detaches and leaves the run going. A second Ctrl-C within 2 seconds
cancels it. Scripts that may retry should pass --idempotency-key, so a
retry after a lost connection returns the run instead of starting another.

Exit codes:
  0   the run succeeded
  1   the run failed
  2   the run timed out
  3   the run was cancelled (after a double Ctrl-C: cancel requested)
  4   the workspace is not set up for it (agent not installed, no inference
      key, bad configuration)
  64  usage error
  65  not signed in, or not allowed
  69  nan.builders unavailable, or the stream was lost (the run goes on)
  75  detached: the run was accepted and is still going`,
	Example: `  nan run "review PR 42 and write the findings to artifacts/review.md"
  nan run --ws develop --cwd /home/nan/projects/api -f task.md
  git diff | nan run -
  id=$(nan run --detach "upgrade the dependencies")`,
	Args:        cobra.ArbitraryArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := runOpts
		opts.timeoutSet = cmd.Flags().Changed("timeout")
		// The arguments first: a usage mistake is the thing to report, even
		// on a machine that is not signed in.
		req, err := buildRunRequest(promptInput{r: os.Stdin, tty: isTerminal(os.Stdin), notice: os.Stderr}, opts, args)
		if err != nil {
			return err
		}
		env, err := newRunsEnv()
		if err != nil {
			return err
		}
		return runtimeError(startRun(cmd.Context(), env, opts, req))
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	f := runCmd.Flags()
	f.StringVar(&runOpts.workspace, "ws", "", "Workspace to run in (default: your only workspace)")
	f.StringVar(&runOpts.agent, "agent", "pi", "Agent to run: pi or hermes")
	f.StringVar(&runOpts.model, "model", "", "Model for the agent (default: the agent's own)")
	f.StringVar(&runOpts.cwd, "cwd", "", "Directory in the workspace to run in (default: /home/nan)")
	f.BoolVar(&runOpts.worktree, "worktree", false, "Always run in a separate git worktree")
	f.BoolVar(&runOpts.noWorktree, "no-worktree", false, "Run in the directory itself, even inside a git repository")
	f.DurationVar(&runOpts.timeout, "timeout", 30*time.Minute, "Stop the run after this long (1m to 2h)")
	f.BoolVar(&runOpts.detach, "detach", false, "Queue the run, print its id and return without streaming")
	f.BoolVar(&runOpts.json, "json", false, "Print events as JSON lines, then the finished run (with --detach: the run)")
	f.StringVar(&runOpts.idempotencyKey, "idempotency-key", "", "Retry-safe key: the same key and request return the run already created")
	f.StringVarP(&runOpts.file, "file", "f", "", `Read the prompt from FILE ("-" for stdin)`)
	runCmd.MarkFlagsMutuallyExclusive("worktree", "no-worktree")
}

// runsEnv is everything the run commands touch outside themselves, so the
// tests can hand them a mock server, buffers and a fake Ctrl-C.
type runsEnv struct {
	client     *api.RunsClient
	usingKey   bool
	stdout     io.Writer
	stderr     io.Writer
	stdin      io.Reader
	interrupts <-chan os.Signal
}

func newRunsEnv() (*runsEnv, error) {
	sess, err := session.Load()
	if err != nil {
		if errors.Is(err, session.ErrNotLoggedIn) {
			return nil, &ExitError{Code: exitAuth, Err: err}
		}
		return nil, exitf(exitAuth, "could not read your session (%v) — run: nan auth login", err)
	}
	if sess.Token == "" && sess.APIKey == "" {
		return nil, &ExitError{Code: exitAuth, Err: session.ErrNotLoggedIn}
	}
	return &runsEnv{
		client:   api.NewRunsClient(sess.Token, sess.APIKey),
		usingKey: sess.Token == "",
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		stdin:    os.Stdin,
	}, nil
}

func doRun(ctx context.Context, env *runsEnv, opts runOptions, args []string) error {
	req, err := buildRunRequest(promptInput{r: env.stdin}, opts, args)
	if err != nil {
		return err
	}
	return startRun(ctx, env, opts, req)
}

func startRun(ctx context.Context, env *runsEnv, opts runOptions, req *api.CreateRunRequest) error {
	if req.Workspace == "" {
		name, err := defaultWorkspace(ctx, env)
		if err != nil {
			return err
		}
		req.Workspace = name
	}

	run, replayed, err := env.client.CreateRun(ctx, *req, opts.idempotencyKey)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return usageErrorf("workspace %q not found (or runs are not enabled for your account)", runs.SanitizeLine(req.Workspace))
		}
		if errors.As(err, &apiErr) && apiErr.Code == "workspace_not_running" {
			return exitf(exitUnavailable, "workspace %q is not running — start it at https://cloud.nan.builders and try again", runs.SanitizeLine(req.Workspace))
		}
		return apiExit(err, env.usingKey)
	}
	if !runIDPattern.MatchString(run.ID) {
		return exitf(exitUnavailable, "nan.builders answered with something that is not a run id")
	}
	id := run.ID

	if replayed {
		fmt.Fprintf(env.stderr, "run %s already exists for this idempotency key (%s)\n", id, runs.SanitizeLine(run.State))
	}

	if opts.detach {
		if opts.json {
			if err := printJSON(env.stdout, run, true); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(env.stdout, id)
			fmt.Fprintf(env.stderr, "run %s %s on %s\n", id, runs.SanitizeLine(run.State), runs.SanitizeLine(run.Workspace.Name))
			printFollowHints(env.stderr, id)
		}
		return runExit(run)
	}

	if !replayed {
		where := runs.SanitizeLine(run.Workspace.Name)
		if where == "" {
			where = runs.SanitizeLine(req.Workspace)
		}
		fmt.Fprintf(env.stderr, "run %s queued on %s (%s) — Ctrl-C to detach\n", id, where, runs.SanitizeLine(run.Agent))
	}
	return follow(ctx, env, run.ID, 0, opts.json, true)
}

// follow streams a run until it ends and exits with its code. With
// detachable, Ctrl-C stops following instead of killing the process, and a
// second one within cancelWindow cancels the run.
func follow(ctx context.Context, env *runsEnv, id string, after int64, asJSON, detachable bool) error {
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()

	renderer := &runs.Renderer{Out: env.stdout}
	var writeErr error
	lastState := ""
	onFrame := func(f api.Frame) {
		switch {
		case f.Event != nil && asJSON:
			if err := printJSON(env.stdout, f.Event, false); err != nil && writeErr == nil {
				writeErr = err
			}
		case f.Event != nil:
			renderer.Event(*f.Event)
		case f.State != nil && !asJSON:
			// A reconnect can repeat the state the stream was already in.
			if f.State.State == lastState {
				return
			}
			lastState = f.State.State
			renderer.Flush()
			state := runs.SanitizeLine(strings.ReplaceAll(f.State.State, "_", " "))
			if f.State.ErrorCode != nil && *f.State.ErrorCode != "" {
				state += " (" + runs.SanitizeLine(*f.State.ErrorCode) + ")"
			}
			fmt.Fprintf(env.stderr, "── %s\n", state)
		}
	}

	type outcome struct {
		run *api.Run
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		run, err := env.client.Follow(streamCtx, id, after, onFrame)
		done <- outcome{run, err}
	}()

	// Ctrl-C is only taken over once the run exists and is being followed:
	// before that there is nothing to detach from, and a member stuck behind
	// a slow request should be able to leave the ordinary way.
	var interrupts <-chan os.Signal
	release := func() {}
	if detachable {
		interrupts = env.interrupts
		if interrupts == nil {
			ch := make(chan os.Signal, 2)
			signal.Notify(ch, os.Interrupt)
			release = func() { signal.Stop(ch) }
			defer release()
			interrupts = ch
		}
	}

	finish := func(res outcome) error {
		renderer.Flush()
		if res.err != nil {
			return streamExit(res.err, env, id)
		}
		if writeErr != nil {
			return writeErr
		}
		if asJSON {
			if err := printJSON(env.stdout, res.run, false); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(env.stderr, runs.Outcome(res.run))
		}
		return runExit(res.run)
	}

	select {
	case res := <-done:
		return finish(res)

	case <-interrupts:
		stopStream()
		res := <-done
		// The run may have ended in the same instant: then there is nothing
		// to detach from, and its outcome is the answer.
		if res.run != nil {
			return finish(res)
		}
		renderer.Flush()
		safeID := runs.SanitizeLine(id)
		fmt.Fprintf(env.stderr, "\ndetached — run %s goes on in the workspace\n", safeID)
		printFollowHints(env.stderr, safeID)
		fmt.Fprintf(env.stderr, "press Ctrl-C again within %s to cancel it now\n", cancelWindow)

		select {
		case <-interrupts:
		case <-time.After(cancelWindow):
			return &ExitError{Code: exitDetached}
		}
		// From here a further Ctrl-C leaves the ordinary way, rather than
		// sitting unheard behind the cancel request.
		release()
		cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		run, changed, err := env.client.CancelRun(cancelCtx, id)
		if err != nil {
			return apiExit(err, env.usingKey)
		}
		if !changed {
			fmt.Fprintf(env.stderr, "run %s had already ended: %s\n", safeID, runs.SanitizeLine(run.State))
			return runExit(run)
		}
		fmt.Fprintf(env.stderr, "cancel requested for run %s — it stops within seconds; check with: nan runs show %s\n", safeID, safeID)
		return &ExitError{Code: exitCancelled}
	}
}

func streamExit(err error, env *runsEnv, id string) error {
	safeID := runs.SanitizeLine(id)
	if errors.Is(err, api.ErrStreamLost) {
		return exitf(exitUnavailable, "%s — run %s goes on in the workspace; follow it with: nan runs logs %s -f", runs.SanitizeLine(err.Error()), safeID, safeID)
	}
	exit := notFound(err, safeID, env.usingKey)
	if exit.Code == exitUnavailable {
		return exitf(exitUnavailable, "%s (run %s)", runs.SanitizeLine(exit.Error()), safeID)
	}
	return exit
}

func printFollowHints(w io.Writer, id string) {
	fmt.Fprintf(w, "  follow: nan runs logs %s -f\n", id)
	fmt.Fprintf(w, "  cancel: nan runs cancel %s\n", id)
}

func printJSON(w io.Writer, v any, indent bool) error {
	out, err := runs.SafeJSON(v, indent)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// buildRunRequest checks the flags and reads the prompt. Everything here is
// a usage error: nothing has been sent yet.
// promptInput is where a "-" prompt comes from. With tty set, a notice says
// the command is waiting for it, which otherwise looks exactly like a hang.
type promptInput struct {
	r      io.Reader
	tty    bool
	notice io.Writer
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func buildRunRequest(in promptInput, opts runOptions, args []string) (*api.CreateRunRequest, error) {
	if opts.agent != "pi" && opts.agent != "hermes" {
		return nil, usageErrorf("--agent must be pi or hermes, not %q", opts.agent)
	}
	if opts.worktree && opts.noWorktree {
		return nil, usageErrorf("--worktree and --no-worktree contradict each other; pick one")
	}
	if opts.model != "" && !modelPattern.MatchString(opts.model) {
		return nil, usageErrorf("--model %q is not a model name (letters, digits and . _ : / -, up to 128)", opts.model)
	}
	if opts.idempotencyKey != "" && !idempotencyPattern.MatchString(opts.idempotencyKey) {
		return nil, usageErrorf("--idempotency-key must be 1-128 of letters, digits and . _ : -")
	}

	prompt, err := readPrompt(in, opts, args)
	if err != nil {
		return nil, err
	}

	req := &api.CreateRunRequest{
		Workspace: opts.workspace,
		Agent:     opts.agent,
		Prompt:    prompt,
		Trigger:   "cli",
	}
	if opts.cwd != "" {
		cwd := opts.cwd
		req.Cwd = &cwd
	}
	if opts.model != "" {
		model := opts.model
		req.Model = &model
	}
	if opts.worktree || opts.noWorktree {
		iso := opts.worktree
		req.GitIsolation = &iso
	}
	if opts.timeoutSet {
		if opts.timeout < minTimeout || opts.timeout > maxTimeout {
			return nil, usageErrorf("--timeout must be between 1m and 2h, not %s", opts.timeout)
		}
		secs := int(opts.timeout / time.Second)
		req.TimeoutSeconds = &secs
	}
	return req, nil
}

func readPrompt(in promptInput, opts runOptions, args []string) (string, error) {
	const how = `pass it as an argument, with -f FILE, or "-" to read stdin`
	if len(args) > 1 {
		return "", usageErrorf("the prompt is one argument — quote it, or %s", how)
	}
	if opts.file != "" && len(args) == 1 {
		return "", usageErrorf("give the prompt once: as an argument or with -f, not both")
	}

	var (
		raw []byte
		err error
	)
	switch {
	case opts.file == "-" || (len(args) == 1 && args[0] == "-"):
		if in.tty && in.notice != nil {
			fmt.Fprintln(in.notice, "reading the prompt from stdin — end it with Ctrl-D")
		}
		if in.r == nil {
			return "", usageErrorf("no stdin to read the prompt from")
		}
		raw, err = io.ReadAll(io.LimitReader(in.r, maxPromptBytes+1))
		if err != nil {
			return "", usageErrorf("could not read the prompt from stdin: %v", err)
		}
	case opts.file != "":
		f, openErr := os.Open(opts.file)
		if openErr != nil {
			return "", usageErrorf("could not read the prompt: %v", openErr)
		}
		raw, err = io.ReadAll(io.LimitReader(f, maxPromptBytes+1))
		f.Close()
		if err != nil {
			return "", usageErrorf("could not read the prompt: %v", err)
		}
	case len(args) == 1:
		raw = []byte(args[0])
	default:
		return "", usageErrorf("no prompt — %s", how)
	}

	switch {
	case len(raw) > maxPromptBytes:
		return "", usageErrorf("the prompt is over %d bytes, the most a run takes", maxPromptBytes)
	case strings.TrimSpace(string(raw)) == "":
		return "", usageErrorf("the prompt is empty — %s", how)
	case !utf8.Valid(raw):
		return "", usageErrorf("the prompt is not UTF-8 text")
	case strings.ContainsRune(string(raw), 0):
		return "", usageErrorf("the prompt contains a NUL byte, which a run cannot take")
	}
	return string(raw), nil
}

// defaultWorkspace is the member's only workspace. With none, or more than
// one, there is nothing to guess: the member has to say.
func defaultWorkspace(ctx context.Context, env *runsEnv) (string, error) {
	list, err := env.client.ListWorkspaces(ctx)
	if err != nil {
		exit := apiExit(err, env.usingKey)
		if exit.Code == exitAuth {
			return "", exit
		}
		return "", exitf(exitUnavailable, "could not list your workspaces (%v) — name one with --ws", exit.Err)
	}
	switch len(list) {
	case 0:
		return "", usageErrorf("you have no workspaces — create one at https://cloud.nan.builders")
	case 1:
		return list[0].Name, nil
	}
	names := make([]string, len(list))
	for i, w := range list {
		names[i] = w.Name
	}
	return "", usageErrorf("you have %d workspaces; pick one with --ws:\n%s", len(list), joinNames(names))
}

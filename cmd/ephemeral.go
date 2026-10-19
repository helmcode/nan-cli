package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/spf13/cobra"
)

// Ephemeral runs (helmcode/nan-devops AGENT-EPHEMERAL-EXECUTORS.md): an
// agent in a fresh, throwaway container instead of a workspace. It clones
// the GitHub repository the run names, runs the agent with the model the
// run names, streams like any run and is destroyed at the end. Billed per
// started minute by container size, with free minutes every month.

// ephemeralDocs is where ephemeral runs are explained.
const ephemeralDocs = "https://nan.builders/docs/ephemeral-runs"

// ephemeralSettingsURL is where the member changes their ephemeral limits:
// writing them needs a browser sign-in.
const ephemeralSettingsURL = runs.PortalURL + "/settings"

// freeMinutesHelp is how free minutes count, said wherever they are shown
// (owner request).
const freeMinutesHelp = "Free minutes are S-minutes: one M minute uses 2, one L minute 4 (100 free = 100 S, 50 M or 25 L)"

// notChargedNote is what a cost means while billing is off.
const notChargedNote = "not charged yet: billing is not active, usage is metered and shown only"

// ephemeralScope is the platform-token scope that may start ephemeral work.
const ephemeralScope = "runs:ephemeral"

// ephemeralSizes are the container sizes, smallest first, with what each
// one is. Prices are the platform's (nan ephemeral usage shows the current
// ones); the help quotes them too.
var ephemeralSizes = []struct {
	name, spec string
	sMinutes   int // S-minutes one minute of this size uses
}{
	{api.SizeS, "1 vCPU, 4 GiB RAM, 10 GB disk", 1},
	{api.SizeM, "2 vCPU, 8 GiB RAM, 16 GB disk", 2},
	{api.SizeL, "4 vCPU, 12 GiB RAM, 20 GB disk", 4},
}

func sizeSpec(size string) string {
	for _, s := range ephemeralSizes {
		if s.name == size {
			return s.spec
		}
	}
	return ""
}

// sizeLabel is a container size as people read it: "M (2 vCPU, ...)".
func sizeLabel(size *string) string {
	if size == nil || *size == "" {
		return ""
	}
	label := strings.ToUpper(runs.SanitizeLine(*size))
	if spec := sizeSpec(*size); spec != "" {
		label += " (" + spec + ")"
	}
	return label
}

// sizeShort is a container size for a table: "M".
func sizeShort(size *string) string {
	if size == nil || *size == "" {
		return "?"
	}
	return strings.ToUpper(runs.SanitizeLine(*size))
}

// formatEUR prints an amount in micros (millionths of a euro) with two
// decimals, or more when the amount needs them: €0.018, €1.50, €0.00.
func formatEUR(micros int64) string {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	frac := strconv.FormatInt(micros%1_000_000+1_000_000, 10)[1:] // six digits
	frac = strings.TrimRight(frac, "0")
	for len(frac) < 2 {
		frac += "0"
	}
	return sign + "€" + strconv.FormatInt(micros/1_000_000, 10) + "." + frac
}

// formatCents prints an amount in euro cents: €100.00.
func formatCents(cents int) string { return formatEUR(int64(cents) * 10_000) }

// startedMinutes is a billed duration in started minutes.
func startedMinutes(seconds int64) int64 { return (seconds + 59) / 60 }

// runCostLabel is an ephemeral run's metered cost, for show.
func runCostLabel(run *api.Run) string {
	c := run.Cost
	if c == nil {
		if api.Terminal(run.State) {
			return "not metered yet: check again in a minute"
		}
		return "metered when the run ends"
	}
	label := formatEUR(c.EURMicros) + " for " + plural(int(startedMinutes(c.BilledSeconds)), "started minute")
	if run.Size != nil && *run.Size != "" {
		label += " of size " + sizeShort(run.Size)
	}
	return label + ", before your free minutes (nan ephemeral usage)"
}

// runCostShort is an ephemeral run's cost, for a table.
func runCostShort(run *api.Run) string {
	switch {
	case !run.Ephemeral():
		return "-"
	case run.Cost == nil:
		return "pending"
	}
	return formatEUR(run.Cost.EURMicros)
}

// runWhere is where a run executes, for a table.
func runWhere(run *api.Run) string {
	if run.Ephemeral() {
		return "ephemeral " + sizeShort(run.Size)
	}
	return runs.Truncate(runs.SanitizeLine(refLabel(run.Workspace.Name, run.Workspace.ID)), 40)
}

// githubRepoPattern is the platform's owner/name grammar.
var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

func validGitHubRepo(r string) bool {
	if !githubRepoPattern.MatchString(r) {
		return false
	}
	owner, name, _ := strings.Cut(r, "/")
	for _, h := range []string{owner, name} {
		if h == "." || h == ".." {
			return false
		}
	}
	return true
}

// repoFromURL turns a GitHub URL pasted into --repo into owner/name, when
// it is one.
func repoFromURL(v string) (string, bool) {
	for _, p := range []string{"https://github.com/", "http://github.com/", "github.com/", "git@github.com:"} {
		if rest, ok := strings.CutPrefix(v, p); ok {
			rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
			if validGitHubRepo(rest) {
				return rest, true
			}
		}
	}
	return "", false
}

// ephemeralUnavailable is the answer when the member does not have
// ephemeral runs: the platform does not say why, on purpose.
func ephemeralUnavailable() *ExitError {
	return exitf(exitAuth, "ephemeral runs are not available for your account")
}

// ephemeralExit maps the platform's ephemeral refusals onto what to do
// about them. nil: not one of them.
//
//   - 400 invalid_request on executor: the member does not have ephemeral
//     runs (the platform answers exactly as for an unknown executor, so
//     nobody outside can tell the executor exists).
//   - 403 token_scope_denied on executor: a credential that may not start
//     billable work (an sk- key, or a token without runs:ephemeral).
//   - 402 ephemeral_cap_reached: the month's spend plus the worst case of
//     the runs going on could pass the member's cap.
func ephemeralExit(apiErr *api.APIError, cred credential) *ExitError {
	switch {
	case apiErr.Code == "ephemeral_cap_reached" || apiErr.Status == 402:
		return exitf(exitUnavailable, "this run could take your ephemeral spend past your monthly cap: raise the cap in the portal, "+
			"Settings at %s (signed in through the browser), or wait for your ephemeral runs going on to finish. "+
			"See this month's spend with: nan ephemeral usage", ephemeralSettingsURL)
	case apiErr.Code == "token_scope_denied" && apiErr.Param == "executor":
		return exitf(exitAuth, "ephemeral runs cost money, so they need your session (nan auth login) or a platform token "+
			"with the %s scope; API keys (sk-) cannot start them. %s", ephemeralScope, ephemeralScopeHint(cred))
	case apiErr.Status == 400 && apiErr.Param == "executor":
		return ephemeralUnavailable()
	}
	return nil
}

// ephemeralScopeHint is what to do about the credential an ephemeral run
// was refused for.
func ephemeralScopeHint(cred credential) string {
	switch cred.kind {
	case credEnv, credFile, credStoredToken:
		return fmt.Sprintf("%s does not have it: create a token with %s in Settings > Tokens at %s, signed in through the browser "+
			"(existing tokens keep the scopes they were created with)", cred.describe(), ephemeralScope, runs.PortalURL)
	case credStoredKey:
		return cred.describe() + " is an API key: sign in with nan auth login"
	}
	return "Sign in again with: nan auth login"
}

// ─── automation files ──────────────────────────────────────────────────────

// checkAutomationExecutor checks the executor fields of an automation file
// before it is sent, so a mistake costs a clear message instead of a round
// trip: executor is workspace or ephemeral, size is s, m or l and only for
// ephemeral, and (on create, where the file is the whole automation) an
// ephemeral automation has no workspace, a fresh checkout of a github_repo
// and a model. An edit holds only the fields that change: the platform
// checks the result.
func checkAutomationExecutor(body json.RawMessage, create bool) error {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(body, &f); err != nil {
		return nil // not an object: the platform says so
	}
	str := func(k string) (string, bool, bool) {
		raw, ok := f[k]
		if !ok || string(raw) == "null" {
			return "", ok, true
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", true, false
		}
		return s, true, true
	}
	executor, hasExecutor, ok := str("executor")
	if !ok || (hasExecutor && executor != api.ExecutorWorkspace && executor != api.ExecutorEphemeral) {
		return usageErrorf(`executor must be "workspace" (the default: runs in one of your workspaces) or "ephemeral" ` +
			`(runs in a fresh container, no workspace); see: nan run --help`)
	}
	size, hasSize, ok := str("size")
	if !ok || (hasSize && size != "" && size != api.SizeS && size != api.SizeM && size != api.SizeL) {
		return usageErrorf(`size must be "s", "m" or "l" (the container of an ephemeral automation: %s); %s`,
			sizeList(), freeMinutesHelp)
	}
	if hasSize && size != "" && hasExecutor && executor == api.ExecutorWorkspace {
		return usageErrorf(`size is for ephemeral automations only: set executor: ephemeral, or remove size`)
	}
	if !create || executor != api.ExecutorEphemeral {
		return nil
	}
	if hasSize && size != "" && !hasExecutor {
		return usageErrorf(`size is for ephemeral automations only: add executor: ephemeral, or remove size`)
	}
	if ws, has, _ := str("workspace"); has && ws != "" {
		return usageErrorf("an ephemeral automation has no workspace: remove workspace (it runs in a fresh container)")
	}
	if checkout, _, _ := str("checkout"); checkout != "fresh" {
		return usageErrorf(`an ephemeral automation works on a fresh checkout: set checkout: fresh and github_repo: OWNER/NAME`)
	}
	if repo, _, _ := str("github_repo"); repo == "" {
		return usageErrorf(`an ephemeral automation needs github_repo: OWNER/NAME, the repository each run clones`)
	}
	if cwd, has, _ := str("cwd"); has && cwd != "" {
		return usageErrorf("an ephemeral automation works in its fresh checkout: remove cwd")
	}
	model, _, _ := str("model")
	if model == "" {
		var inf struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(f["inference"], &inf)
		model = inf.Model
	}
	if model == "" {
		return usageErrorf("an ephemeral automation must name its model (model: ID): a container has no workspace configuration to default to")
	}
	return nil
}

func sizeList() string {
	parts := make([]string, len(ephemeralSizes))
	for i, s := range ephemeralSizes {
		parts[i] = s.name + " " + s.spec
	}
	return strings.Join(parts, "; ")
}

// ─── nan ephemeral ─────────────────────────────────────────────────────────

var (
	ephemeralUsageJSON    bool
	ephemeralSettingsJSON bool
)

const ephemeralCredentialHelp = `These commands need your session (nan auth login): platform tokens and
API keys cannot read them.`

var ephemeralCmd = &cobra.Command{
	Use:   "ephemeral",
	Short: "Your ephemeral runs: this month's minutes and cost, and your limits",
	Long: `Ephemeral runs execute an agent in a fresh, throwaway container instead of
a workspace: start one with nan run --ephemeral --repo OWNER/NAME --model ID.

Sizes, billed per started minute (a 14 s run is 1 minute):
` + sizeTable("  ") + `
Free minutes every calendar month: Community 100, Inference 300, Premium
1000. ` + freeMinutesHelp + `.
While billing is not active, usage is metered and shown, not charged:
nan ephemeral usage says which.

Your limits, a monthly spend cap (default €100) and how many ephemeral runs
may go on at once (default 50), are changed in the browser only: Settings
at ` + ephemeralSettingsURL + `.

` + ephemeralCredentialHelp + `

Docs: ` + ephemeralDocs,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageErrorf("unknown command %q for \"nan ephemeral\"; try: nan ephemeral --help", runs.Truncate(runs.SanitizeLine(args[0]), 64))
		}
		return cmd.Help()
	},
}

var ephemeralUsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "This month's ephemeral minutes, cost and free minutes left",
	Long: `This calendar month's ephemeral runs: container minutes, cost after your
free minutes, the free minutes left, your monthly cap and the prices.

` + freeMinutesHelp + `.
While billing is not active, usage is metered and shown, not charged.

` + ephemeralCredentialHelp + `

Docs: ` + ephemeralDocs,
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEphemeralEnv(func(env *runsEnv) error { return doEphemeralUsage(cmd.Context(), env) })
	},
}

var ephemeralSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Your ephemeral limits: monthly spend cap and concurrent runs (read only)",
	Long: `Your ephemeral limits: the monthly spend cap (€0 to €1000, default €100)
and how many ephemeral runs may go on at once (1 to 50, default 50).

They are read only here: change them in the browser, Settings at
` + ephemeralSettingsURL + `, signed in through the browser, so that nothing
that can read this machine's CLI session (an agent, a CI job) can raise
what you spend.

` + ephemeralCredentialHelp + `

Docs: ` + ephemeralDocs,
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEphemeralEnv(func(env *runsEnv) error { return doEphemeralSettings(cmd.Context(), env) })
	},
}

func init() {
	rootCmd.AddCommand(ephemeralCmd)
	ephemeralCmd.AddCommand(ephemeralUsageCmd, ephemeralSettingsCmd)
	ephemeralUsageCmd.Flags().BoolVar(&ephemeralUsageJSON, "json", false, "Print the API's usage object as JSON")
	ephemeralSettingsCmd.Flags().BoolVar(&ephemeralSettingsJSON, "json", false, "Print the API's settings object as JSON")
}

// sizeTable is the sizes and their prices, for help text, each line
// starting with indent.
func sizeTable(indent string) string {
	prices := map[string]string{api.SizeS: "€0.003", api.SizeM: "€0.006", api.SizeL: "€0.012"}
	var b strings.Builder
	for _, s := range ephemeralSizes {
		fmt.Fprintf(&b, "%s%-2s %-32s %s per minute\n", indent, s.name, s.spec, prices[s.name])
	}
	return b.String()
}

// ephemeralSession is the credential of nan ephemeral: the session from
// nan auth login, and nothing else (the platform takes no token here).
func ephemeralSession(getenv func(string) string, tokenFile string) (credential, error) {
	const why = "nan ephemeral needs your session: sign in with nan auth login (platform tokens and API keys cannot read ephemeral usage or settings)"
	if tokenFile != "" {
		return credential{}, exitf(exitAuth, "--token-file was given, and %s", why)
	}
	sess, err := session.Load()
	if err != nil && !errors.Is(err, session.ErrNotLoggedIn) {
		return credential{}, exitf(exitAuth, "could not read your session (%v): %s", err, why)
	}
	if sess == nil || sess.Token == "" {
		if getenv(tokenEnvVar) != "" {
			return credential{}, exitf(exitAuth, "%s is set, and %s", tokenEnvVar, why)
		}
		return credential{}, exitf(exitAuth, "%s", why)
	}
	return credential{kind: credSession, session: sess.Token}, nil
}

func withEphemeralEnv(fn func(*runsEnv) error) error {
	cred, err := ephemeralSession(os.Getenv, tokenFileFlag)
	if err != nil {
		return err
	}
	return runtimeError(fn(&runsEnv{client: cred.client(), cred: cred, stdout: writeStdout, stderr: writeStderr}))
}

// ephemeralReadExit maps a refused read of /api/ephemeral.
func ephemeralReadExit(err error, cred credential) *ExitError {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 404:
			return ephemeralUnavailable()
		case 403:
			return exitf(exitAuth, "nan.builders did not take %s for this: sign in with nan auth login", cred.describe())
		}
	}
	return apiExit(err, cred)
}

func doEphemeralUsage(ctx context.Context, env *runsEnv) error {
	u, err := env.client.EphemeralUsage(ctx)
	if err != nil {
		return ephemeralReadExit(err, env.cred)
	}
	if ephemeralUsageJSON {
		return printJSON(env.stdout, u, true)
	}
	priceS := u.PricesEURMicrosPerMinute[api.SizeS]
	sMinutes := func(micros int64) int64 {
		if priceS <= 0 {
			return 0
		}
		return micros / priceS
	}

	month := runs.SanitizeLine(u.Month)
	if u.Plan != "" {
		month += " (" + runs.SanitizeLine(u.Plan) + " plan)"
	}
	fmt.Fprintf(env.stdout, "ephemeral runs, %s\n\n", month)
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "container time\t%s\n", plural(int(u.Minutes), "minute"))
	if priceS > 0 {
		total, used := sMinutes(u.FreeEURMicrosTotal), sMinutes(u.FreeEURMicrosUsed)
		fmt.Fprintf(tw, "free minutes left\t%d S-minutes of %d (= %d M or %d L minutes); %d used\n",
			u.FreeMinutesLeft, total, u.FreeMinutesLeft/2, u.FreeMinutesLeft/4, used)
	}
	cost := formatEUR(u.CostEURMicros) + " after your free minutes"
	if !u.BillingEnabled {
		cost += " (" + notChargedNote + ")"
	}
	fmt.Fprintf(tw, "cost this month\t%s\n", cost)
	fmt.Fprintf(tw, "monthly cap\t%s (change it in the browser, Settings at %s)\n", formatCents(u.MonthlyCapEURCents), ephemeralSettingsURL)
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(env.stdout, "\nprices per started minute (a 14 s run is 1 minute):")
	tw = tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	for _, s := range ephemeralSizes {
		price, ok := u.PricesEURMicrosPerMinute[s.name]
		if !ok {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\ta minute uses %s\n", strings.ToUpper(s.name), s.spec, formatEUR(price),
			plural(s.sMinutes, "free S-minute"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "\n%s.\n", freeMinutesHelp)
	return nil
}

func doEphemeralSettings(ctx context.Context, env *runsEnv) error {
	s, err := env.client.EphemeralSettings(ctx)
	if err != nil {
		return ephemeralReadExit(err, env.cred)
	}
	if ephemeralSettingsJSON {
		return printJSON(env.stdout, s, true)
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "monthly spend cap\t%s (€0 to %s)\n", formatCents(s.MonthlyCapEURCents), formatCents(s.Limits.MonthlyCapEURCentsMax))
	fmt.Fprintf(tw, "concurrent runs\tup to %d at once (1 to %d)\n", s.MaxConcurrent, s.Limits.MaxConcurrentMax)
	billing := "active: ephemeral usage past your free minutes is charged"
	if !s.BillingEnabled {
		billing = "not active yet: usage is metered and shown, not charged"
	}
	fmt.Fprintf(tw, "billing\t%s\n", billing)
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(env.stderr, "change these in the browser: Settings at %s (signed in through the browser; the CLI only reads them)\n", ephemeralSettingsURL)
	return nil
}

// billingNote says, under an ephemeral run's cost, whether that cost is
// charged. Only a session can ask (GET /api/ephemeral/usage); the lookup is
// bounded like a name lookup and never fails the command.
func (env *runsEnv) billingNote(ctx context.Context) {
	known, enabled := false, false
	if env.cred.kind == credSession {
		lctx, cancel := context.WithTimeout(ctx, nameLookupTimeout)
		defer cancel()
		if u, err := env.client.EphemeralUsage(lctx); err == nil {
			known, enabled = true, u.BillingEnabled
		}
	}
	switch {
	case known && enabled:
	case known:
		fmt.Fprintf(env.stdout, "\nnote: %s\n", notChargedNote)
	default:
		fmt.Fprintln(env.stdout, "\nnote: while billing is not active, ephemeral costs are metered and shown, not charged (nan ephemeral usage says which)")
	}
}

// ─── nan run --ephemeral ───────────────────────────────────────────────────

// checkEphemeralFlags checks the flags of an ephemeral run (and that the
// ephemeral-only flags are not given without --ephemeral). It normalises
// --size to the platform's lowercase name.
func checkEphemeralFlags(opts *runOptions) error {
	if !opts.ephemeral {
		switch {
		case opts.repo != "":
			return usageErrorf("--repo is for ephemeral runs: add --ephemeral (a workspace run works in the workspace's own folders; pick one with --cwd)")
		case opts.size != "":
			return usageErrorf("--size is for ephemeral runs: add --ephemeral (a workspace has its own size, set in the portal)")
		}
		return nil
	}
	switch {
	case opts.workspace != "":
		return usageErrorf("an ephemeral run has no workspace: drop --ws (it runs in a fresh container on a clone of --repo)")
	case opts.cwd != "":
		return usageErrorf("an ephemeral run works in its fresh clone of --repo: drop --cwd")
	case opts.worktree || opts.noWorktree:
		return usageErrorf("an ephemeral run works on a fresh clone, never a git worktree: drop --worktree/--no-worktree")
	case opts.repo == "":
		return usageErrorf("--ephemeral needs --repo OWNER/NAME: the GitHub repository the container clones")
	}
	if !validGitHubRepo(opts.repo) {
		if repo, ok := repoFromURL(opts.repo); ok {
			return usageErrorf("--repo takes OWNER/NAME, not a URL: did you mean --repo %s?", repo)
		}
		return usageErrorf("--repo %q is not a GitHub repository: give it as OWNER/NAME, like acme/api", runs.Truncate(runs.SanitizeLine(opts.repo), 120))
	}
	if opts.model == "" {
		return usageErrorf("--ephemeral needs --model ID: a container has no workspace configuration to pick a model from")
	}
	if opts.size != "" {
		size := strings.ToLower(opts.size)
		if sizeSpec(size) == "" {
			return usageErrorf("--size must be s, m or l, not %q: %s. %s", runs.Truncate(runs.SanitizeLine(opts.size), 16), sizeList(), freeMinutesHelp)
		}
		opts.size = size
	}
	return nil
}

// runPlace says where a run executes, for the line that announces it:
// "on develop", or "in an ephemeral container (size M) for acme/api".
func runPlace(run *api.Run, req *api.CreateRunRequest) string {
	if run.Ephemeral() || req.Executor == api.ExecutorEphemeral {
		size := run.Size
		if size == nil || *size == "" {
			s := req.Size
			if s == "" {
				s = api.SizeS
			}
			size = &s
		}
		repo := run.GitHubRepo
		if repo == nil {
			repo = req.GitHubRepo
		}
		place := "in an ephemeral container (size " + sizeShort(size) + ")"
		if repo != nil && *repo != "" {
			place += " for " + runs.SanitizeLine(*repo)
		}
		return place
	}
	where := runs.SanitizeLine(run.Workspace.Name)
	if where == "" {
		where = runs.SanitizeLine(req.Workspace)
	}
	return "on " + where
}

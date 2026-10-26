package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/contexts"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"
)

// Agent contexts (helmcode/nan-devops AGENT-CONTEXT.md §6): a versioned file
// tree a member keeps on NaN and hands to their runs and automations:
//
//	AGENTS.md            instructions
//	skills/<name>/...    skills (SKILL.md and its files)
//	files/...            reference files
//
// push hashes a local folder, uploads only the blobs the platform lacks
// (one PUT up to 16 MiB, a resumable multipart upload above) and publishes
// the tree as a new version based on the one it read. pull writes a version
// into an empty folder, through os.OpenRoot and only after every path passed
// the same validator the platform uses (internal/contexts, a verbatim copy).

const (
	contextsDocs = "https://nan.builders/docs/contexts"

	scopeContextsRead  = "contexts:read"
	scopeContextsWrite = "contexts:write"

	// exitDataErr is sysexits' EX_DATAERR: the content was refused (a file
	// name, a type, a limit, secret-like content). The same number as
	// exitAuth, which is EX_NOPERM's neighbour in this CLI's table.
	exitDataErr = 65

	// Limits of one version (AGENT-CONTEXT.md §1.4), checked before any
	// upload so a push that cannot be published uploads nothing.
	ctxMaxFiles      = 1000
	ctxMaxSkills     = 30
	ctxMaxTotalBytes = 250 << 20
	ctxMaxAgentsMD   = 32 << 10
	ctxMaxSkillMD    = 64 << 10

	// ctxMaxWait is the longest Retry-After a push waits out by itself.
	ctxMaxWait = 2 * time.Minute
	// ctxRetries is how many times one request is retried after a
	// Retry-After, a part after a failure, or a publish after blob_missing
	// or (with --force) version_conflict.
	ctxRetries = 3
)

// Test seams.
var (
	// ctxSleep waits out a Retry-After.
	ctxSleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	// ctxUploadStateDir is where an unfinished multipart upload is
	// remembered, so the next push resumes it instead of starting over.
	ctxUploadStateDir = func() (string, error) {
		d, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, "nan", "context-uploads"), nil
	}
	// ctxConcurrency is how many blobs a push uploads at once.
	ctxConcurrency = 4
	// ctxSingleMax is the largest blob sent as one PUT.
	ctxSingleMax int64 = api.ContextPartSize
)

var (
	ctxJSON bool

	ctxCreateDescription string
	ctxCreateFrom        string

	ctxPushCreate       bool
	ctxPushForce        bool
	ctxPushAllowSecrets bool
	ctxPushDryRun       bool
	ctxPushSkipInvalid  bool

	ctxPullForce bool

	ctxEditName        string
	ctxEditDescription string

	ctxRmYes bool
)

var ctxVerbHints = map[string]string{
	"list": "ls", "get": "show", "new": "create", "add": "create", "upload": "push", "publish": "push",
	"download": "pull", "clone": "pull", "update": "edit", "rename": "edit NAME --name NEW", "delete": "rm", "remove": "rm",
}

const ctxCredentialHelp = `Authentication, first match wins: --token-file, your session from
nan auth login, NAN_TOKEN, a platform token saved with nan auth login
--api-token. A platform token needs contexts:read to list, show and pull,
and contexts:write to create, push, edit and delete. Workspace keys never
read or write contexts.`

var contextCmd = &cobra.Command{
	Use:     "context",
	Aliases: []string{"contexts"},
	Short:   "Keep instructions, skills and files for your agent runs",
	Long: `Manage your agent contexts: versioned folders of instructions, skills and
reference files that your runs and automations hand to the agent.

A context has this layout:
  AGENTS.md            instructions for the agent (at most 32 KiB)
  skills/<name>/       a skill: SKILL.md (name and description in its
                       frontmatter) plus any files it uses
  files/...            reference files: docs, specs, CSV, PDF, images,
                       Office documents

File names may use letters of any language, digits, spaces and
. _ - ( ) + , @. Dotfiles (.env, .git) are skipped; names with emoji,
symbols, control characters or Windows reserved names (CON, NUL, ...)
are refused, each with its reason.

Every push publishes a new version; runs pin the version they start
with, automations follow the latest. Do not put credentials in a context:
keep them in nan secrets. A push that looks like it carries one is
refused unless you add --allow-secrets.

Exit codes: 0 done, 1 failed (the context changed while pushing), 64
usage, 65 content refused (file names, types, limits, secret-like
content, storage quota) or credential refused, 69 nan.builders
unavailable.

` + ctxCredentialHelp + `

Docs: ` + contextsDocs,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			shown := runs.Truncate(runs.SanitizeLine(args[0]), 64)
			if hint, ok := ctxVerbHints[args[0]]; ok {
				return usageErrorf("there is no nan context %s; try: nan context %s", shown, hint)
			}
			return usageErrorf("unknown command %q for \"nan context\"; try: nan context --help", shown)
		}
		return cmd.Help()
	},
}

var ctxLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List your contexts and the storage they use",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doContextLs(cmd.Context(), env) })
	},
}

var ctxShowCmd = &cobra.Command{
	Use:         "show <NAME|id>",
	Short:       "Show a context and the files of its current version",
	Args:        cobra.ExactArgs(1),
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doContextShow(cmd.Context(), env, args[0]) })
	},
}

var ctxCreateCmd = &cobra.Command{
	Use:   "create NAME [--description TEXT] [--from DIR]",
	Short: "Create a context, empty or from a folder",
	Long: `Create a context. NAME is lowercase letters, digits and dashes (at most
63). With --from DIR, the folder is pushed as its first content (see nan
context push --help for what a push checks).

` + ctxCredentialHelp + `

Docs: ` + contextsDocs,
	Example: `  nan context create acme-api --description "Acme API conventions"
  nan context create acme-api --from ./acme-context`,
	Args:        cobra.ExactArgs(1),
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doContextCreate(cmd.Context(), env, args[0]) })
	},
}

var ctxPushCmd = &cobra.Command{
	Use:   "push <NAME|id> DIR",
	Short: "Publish a folder as the context's next version",
	Long: `Publish DIR as the next version of a context: the folder becomes the
context's whole content (files no longer in DIR are removed from the new
version; older versions are kept while a run uses them).

Before anything is uploaded, push checks the layout and every file name:
dotfiles and dot-folders (.git, .env) are skipped with a notice; every
other refused file is printed with its reason (bad_top_level,
bad_character, case_collision, reserved_name, too_long, too_deep,
too_large, skill_invalid, symlink) and push exits 65 without uploading,
unless --skip-invalid leaves those files out. A CLAUDE.md is refused with
a hint to rename it to AGENTS.md. Names are normalised to NFC (macOS
hands out decomposed names).

Then push hashes every file, asks the platform which ones it does not
already hold, uploads only those (16 MiB in one request, larger files in
16 MiB parts; an interrupted upload resumes on the next push) and
publishes the new version. --dry-run prints what would be added, changed
and removed, and how much would be uploaded, and changes nothing.

If the context changed since push read it (another push, the portal),
push stops with exit 1. --force shows what changed and publishes on top
of it instead. Text that looks like a credential (API keys, tokens,
private keys) is refused with the file and line; keep credentials in nan
secrets, or publish anyway with --allow-secrets (the agent can read and
copy anything in a context).

With --create, a context that does not exist yet is created first.

` + ctxCredentialHelp + `

Docs: ` + contextsDocs,
	Example: `  nan context push acme-api ./acme-context --dry-run
  nan context push acme-api ./acme-context
  nan context push acme-api ./acme-context --create --skip-invalid`,
	Args:        cobra.ExactArgs(2),
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error {
			return doContextPush(cmd.Context(), env, args[0], args[1], pushOptionsFromFlags())
		})
	},
}

var ctxPullCmd = &cobra.Command{
	Use:   "pull <NAME|id> DIR",
	Short: "Download a context's current version into an empty folder",
	Long: `Write the current version of a context into DIR, which must be empty or
not exist yet (--force writes into a folder that has files, replacing
the ones the context has).

Every path the platform sends is checked with the same rules a push
uses, and files are written through a handle rooted at DIR: a path
that is absolute, climbs out with .., differs from another only by case,
or is a Windows reserved name is refused before anything is written.
Every file is checked against its sha256 and size.

` + ctxCredentialHelp + `

Docs: ` + contextsDocs,
	Example:     `  nan context pull acme-api ./acme-context`,
	Args:        cobra.ExactArgs(2),
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doContextPull(cmd.Context(), env, args[0], args[1]) })
	},
}

var ctxEditCmd = &cobra.Command{
	Use:         "edit <NAME|id> [--name NEW] [--description TEXT]",
	Short:       "Rename a context or change its description (no new version)",
	Args:        cobra.ExactArgs(1),
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("name") && !cmd.Flags().Changed("description") {
			return usageErrorf("nothing to change: give --name or --description")
		}
		var patch api.ContextPatch
		if cmd.Flags().Changed("name") {
			if !contexts.ValidContextName(ctxEditName) {
				return usageErrorf("%s", contextNameRule(ctxEditName))
			}
			patch.Name = &ctxEditName
		}
		if cmd.Flags().Changed("description") {
			patch.Description = &ctxEditDescription
		}
		return withWriteEnv(func(env *runsEnv) error { return doContextEdit(cmd.Context(), env, args[0], patch) })
	},
}

var ctxRmCmd = &cobra.Command{
	Use:         "rm <NAME|id> [--yes]",
	Short:       "Delete a context and all its versions",
	Args:        cobra.ExactArgs(1),
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doContextRm(cmd.Context(), env, args[0]) })
	},
}

func init() {
	rootCmd.AddCommand(contextCmd)
	contextCmd.AddCommand(ctxLsCmd, ctxShowCmd, ctxCreateCmd, ctxPushCmd, ctxPullCmd, ctxEditCmd, ctxRmCmd)
	contextCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	for _, c := range []*cobra.Command{ctxLsCmd, ctxShowCmd, ctxCreateCmd, ctxPushCmd, ctxEditCmd} {
		c.Flags().BoolVar(&ctxJSON, "json", false, "Print the answer as JSON")
	}
	ctxCreateCmd.Flags().StringVar(&ctxCreateDescription, "description", "", "What the context is for")
	ctxCreateCmd.Flags().StringVar(&ctxCreateFrom, "from", "", "Push this folder as the first content (`DIR`)")
	for _, c := range []*cobra.Command{ctxCreateCmd, ctxPushCmd} {
		c.Flags().BoolVar(&ctxPushAllowSecrets, "allow-secrets", false, "Publish even if a text file looks like it holds a credential")
		c.Flags().BoolVar(&ctxPushSkipInvalid, "skip-invalid", false, "Leave refused files out instead of stopping")
	}
	f := ctxPushCmd.Flags()
	f.BoolVar(&ctxPushCreate, "create", false, "Create the context if it does not exist")
	f.BoolVar(&ctxPushForce, "force", false, "Publish on top of a version someone else published meanwhile")
	f.BoolVar(&ctxPushDryRun, "dry-run", false, "Print what would change and be uploaded; change nothing")
	ctxPullCmd.Flags().BoolVar(&ctxPullForce, "force", false, "Write into a folder that is not empty")
	ctxEditCmd.Flags().StringVar(&ctxEditName, "name", "", "The new name")
	ctxEditCmd.Flags().StringVar(&ctxEditDescription, "description", "", "The new description")
	ctxRmCmd.Flags().BoolVar(&ctxRmYes, "yes", false, "Do not ask for confirmation")
}

type pushOptions struct {
	create, force, allowSecrets, dryRun, skipInvalid, json bool
}

func pushOptionsFromFlags() pushOptions {
	return pushOptions{create: ctxPushCreate, force: ctxPushForce, allowSecrets: ctxPushAllowSecrets, dryRun: ctxPushDryRun,
		skipInvalid: ctxPushSkipInvalid, json: ctxJSON}
}

func contextNameRule(name string) string {
	return fmt.Sprintf("%q is not a valid context name: use lowercase letters, digits and dashes, starting with a letter or digit, at most 63 characters (not a Windows reserved name like con or nul)",
		runs.Truncate(runs.SanitizeLine(name), 80))
}

// ─── errors ────────────────────────────────────────────────────────────────

// ctxExit maps a refused contexts request onto an exit code and a message.
func ctxExit(err error, env *runsEnv, scope string) *ExitError {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit
	}
	ce, ok := api.AsContextError(err)
	if !ok {
		return writeExit(err, env.cred, "contexts", scope)
	}
	msg := runs.SanitizeLine(ce.Message)
	switch ce.Code {
	case "not_found", "":
		if ce.Status == 404 {
			return apiOff("using agent contexts")
		}
	case "context_not_found":
		return usageErrorf("no such context (see: nan context ls)")
	case "context_name_taken":
		return usageErrorf("you already have a context with that name")
	case "too_many_contexts":
		return exitf(exitDataErr, "%s", msg)
	case "context_in_use":
		return usageErrorf("automations use this context; remove it from them first (nan automations edit):\n%s",
			joinNames(refLabels(ce.Automations)))
	case "context_quota_exceeded":
		return exitf(exitDataErr, "%s (%s of %s used). Delete contexts or versions you no longer need, or upgrade: %s/membership",
			msg, humanBytes(ce.Used), humanBytes(ce.Quota), runs.PortalURL)
	case "context_too_large", "unsupported_file_type", "invalid_path", "skill_invalid", "context_secret_detected":
		return exitf(exitDataErr, "%s", msg)
	case "storage_unavailable", "upload_in_progress", "blob_busy", "part_conflict", "upload_expired":
		return exitf(exitUnavailable, "%s", msg)
	case "rate_limited":
		if ce.RetryAfter > 0 {
			return exitf(exitUnavailable, "%s (try again in %s)", msg, ce.RetryAfter.Round(time.Second))
		}
		return exitf(exitUnavailable, "%s", msg)
	case "blob_hash_mismatch":
		return exitf(exitUnavailable, "a file changed while it was being uploaded; push again (%s)", msg)
	case "entitlement_required", "tier_restricted":
		return exitf(exitAuth, "your membership does not include agent runs, which contexts are for: see %s/membership", runs.PortalURL)
	}
	return writeExit(ce.APIError, env.cred, "contexts", scope)
}

// ctxRetry runs fn again after a Retry-After the platform asked for (rate
// limits, an upload of the same blob in progress, storage briefly away), at
// most ctxRetries times and never longer than ctxMaxWait each.
func ctxRetry(ctx context.Context, env *runsEnv, what string, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		ce, ok := api.AsContextError(err)
		if !ok || attempt >= ctxRetries || !retryable(ce) {
			return err
		}
		wait := ce.RetryAfter
		if wait <= 0 {
			wait = 5 * time.Second
		}
		if wait > ctxMaxWait {
			return err
		}
		fmt.Fprintf(env.stderr, "%s: %s; retrying in %s\n", what, runs.SanitizeLine(ce.Message), wait.Round(time.Second))
		if serr := ctxSleep(ctx, wait); serr != nil {
			return err
		}
	}
}

func retryable(ce *api.ContextError) bool {
	switch ce.Code {
	case "rate_limited", "upload_in_progress", "blob_busy", "storage_unavailable":
		return true
	}
	return ce.Status == 429
}

func ctxCode(err error) string {
	if ce, ok := api.AsContextError(err); ok {
		return ce.Code
	}
	return ""
}

// ─── ls, show ──────────────────────────────────────────────────────────────

func doContextLs(ctx context.Context, env *runsEnv) error {
	list, err := env.client.ListContexts(ctx)
	if err != nil {
		return ctxExit(err, env, scopeContextsRead)
	}
	if ctxJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintf(env.stderr, "no contexts yet; create one with: nan context create NAME --from DIR\n")
	} else {
		tw := tabwriter.NewWriter(env.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tVERSION\tFILES\tSIZE\tUPDATED\tDESCRIPTION")
		for _, c := range list.Data {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\n", runs.SanitizeLine(c.Name), c.Version, c.FileCount, humanBytes(c.TotalBytes),
				localTime(c.UpdatedAt), runs.Truncate(runs.SanitizeLine(c.Description), 48))
		}
		_ = tw.Flush()
	}
	fmt.Fprintf(env.stdout, "storage: %s of %s used\n", humanBytes(list.Usage.StoredBytes), humanBytes(list.Usage.QuotaBytes))
	return nil
}

func doContextShow(ctx context.Context, env *runsEnv, ref string) error {
	d, err := env.client.GetContext(ctx, ref)
	if err != nil {
		return ctxExit(err, env, scopeContextsRead)
	}
	if ctxJSON {
		return printJSON(env.stdout, d, true)
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 4, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, v) }
	row("name", runs.SanitizeLine(d.Name))
	row("id", runs.SanitizeLine(d.ID))
	if d.Description != "" {
		row("description", runs.SanitizeLine(d.Description))
	}
	row("version", fmt.Sprintf("%d (%s)", d.Version, localTime(d.UpdatedAt)))
	row("files", fmt.Sprintf("%d, %s", d.FileCount, humanBytes(d.TotalBytes)))
	row("instructions", yesNo(d.HasInstructions))
	skills := make([]string, len(d.Skills))
	for i, s := range d.Skills {
		skills[i] = runs.SanitizeLine(s)
	}
	if len(skills) == 0 {
		row("skills", "none")
	} else {
		row("skills", strings.Join(skills, ", "))
	}
	_ = tw.Flush()
	if len(d.Files) > 0 {
		fmt.Fprintln(env.stdout)
		tw = tabwriter.NewWriter(env.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PATH\tSIZE\tSHA256")
		files := append([]api.ContextFile(nil), d.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, f := range files {
			p := runs.SanitizeLine(f.Path)
			if f.Executable {
				p += " (executable)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p, humanBytes(f.Size), runs.Truncate(runs.SanitizeLine(f.SHA256), 12))
		}
		_ = tw.Flush()
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// humanBytes is a size as people read it (1024-based, one decimal).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// ─── create, edit, rm ──────────────────────────────────────────────────────

func doContextCreate(ctx context.Context, env *runsEnv, name string) error {
	if !contexts.ValidContextName(name) {
		return usageErrorf("%s", contextNameRule(name))
	}
	opts := pushOptions{allowSecrets: ctxPushAllowSecrets, skipInvalid: ctxPushSkipInvalid, json: ctxJSON}
	var local *localTree
	if ctxCreateFrom != "" {
		// The folder is checked before the context exists, so a folder that
		// cannot be pushed does not leave an empty context behind.
		var err error
		if local, err = scanLocal(env, ctxCreateFrom, opts.skipInvalid); err != nil {
			return err
		}
	}
	idem := newIdempotencyKey()
	var c *api.Context
	err := ctxRetry(ctx, env, "create", func() error {
		var err error
		c, err = env.client.CreateContext(ctx, name, ctxCreateDescription, idem)
		return err
	})
	if err != nil {
		return ctxExit(err, env, scopeContextsWrite)
	}
	if local == nil {
		if ctxJSON {
			return printJSON(env.stdout, c, true)
		}
		fmt.Fprintf(env.stdout, "context %s created (empty, version %d)\n", runs.SanitizeLine(c.Name), c.Version)
		fmt.Fprintf(env.stderr, "add content with: nan context push %s DIR\n", runs.SanitizeLine(c.Name))
		return nil
	}
	fmt.Fprintf(env.stderr, "context %s created\n", runs.SanitizeLine(c.Name))
	return pushTree(ctx, env, &api.ContextDetail{Context: *c, Files: []api.ContextFile{}}, local, opts)
}

func doContextEdit(ctx context.Context, env *runsEnv, ref string, patch api.ContextPatch) error {
	d, err := env.client.GetContext(ctx, ref)
	if err != nil {
		return ctxExit(err, env, scopeContextsRead)
	}
	c, err := env.client.UpdateContext(ctx, d.ID, patch)
	if err != nil {
		return ctxExit(err, env, scopeContextsWrite)
	}
	if ctxJSON {
		return printJSON(env.stdout, c, true)
	}
	fmt.Fprintf(env.stdout, "context %s updated\n", runs.SanitizeLine(c.Name))
	return nil
}

func doContextRm(ctx context.Context, env *runsEnv, ref string) error {
	d, err := env.client.GetContext(ctx, ref)
	if err != nil {
		return ctxExit(err, env, scopeContextsRead)
	}
	name := runs.SanitizeLine(d.Name)
	if err := confirm(env, ctxRmYes, "delete context "+name+" and all its versions?"); err != nil {
		return err
	}
	if err := env.client.DeleteContext(ctx, d.ID); err != nil {
		return ctxExit(err, env, scopeContextsWrite)
	}
	fmt.Fprintf(env.stdout, "context %s deleted\n", name)
	return nil
}

// ─── the local tree ────────────────────────────────────────────────────────

// localFile is one file of the folder being pushed.
type localFile struct {
	path string // NFC, '/'-separated, relative to the folder
	abs  string
	size int64
	exec bool
	sha  string
}

// refusedFile is one file push will not publish, with its reason.
type refusedFile struct {
	path, reason, detail string
}

type localTree struct {
	files []localFile
}

func (t *localTree) totalBytes() int64 {
	var n int64
	for _, f := range t.files {
		n += f.size
	}
	return n
}

// scanLocal walks dir, checks every name and the layout, applies the
// per-file limits and hashes what is left. Refused files stop the push
// (exit 65) unless skipInvalid; dotfiles are always skipped with a notice.
func scanLocal(env *runsEnv, dir string, skipInvalid bool) (*localTree, error) {
	shown := runs.SanitizeLine(dir)
	st, err := os.Stat(dir)
	if err != nil {
		return nil, usageErrorf("cannot read %s: %v", shown, unwrapPath(err))
	}
	if !st.IsDir() {
		return nil, usageErrorf("%s is not a folder", shown)
	}
	var (
		files   []localFile
		refused []refusedFile
		dotted  []string
	)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		slash := filepath.ToSlash(rel)
		name := norm.NFC.String(slash)
		if strings.HasPrefix(d.Name(), ".") {
			dotted = append(dotted, name)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			refused = append(refused, refusedFile{name, "symlink", "links are not kept; copy the file instead"})
			return nil
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			refused = append(refused, refusedFile{name, "not_a_file", ""})
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		files = append(files, localFile{path: name, abs: p, size: info.Size(),
			exec: runtime.GOOS != "windows" && info.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		return nil, usageErrorf("cannot read %s: %v", shown, unwrapPath(err))
	}
	for _, p := range dotted {
		fmt.Fprintf(env.stderr, "skipped %s (dotfiles are not kept in a context)\n", runs.SanitizeLine(p))
	}

	refused = append(refused, checkTree(files)...)
	bad := map[string]bool{}
	for _, r := range refused {
		bad[r.path] = true
	}
	// A skill folder that lost its SKILL.md loses the whole skill.
	for _, r := range refused {
		if r.reason == "skill_invalid" && strings.HasSuffix(r.path, "/"+contexts.SkillFileName) {
			prefix := strings.TrimSuffix(r.path, contexts.SkillFileName)
			for _, f := range files {
				if strings.HasPrefix(f.path, prefix) {
					bad[f.path] = true
				}
			}
		}
	}
	if len(refused) > 0 {
		sort.SliceStable(refused, func(i, j int) bool { return refused[i].path < refused[j].path })
		verb := "refused"
		if skipInvalid {
			verb = "skipped"
		}
		for _, r := range refused {
			line := fmt.Sprintf("%s %s: %s", verb, runs.SanitizeLine(r.path), r.reason)
			if r.detail != "" {
				line += " (" + r.detail + ")"
			}
			fmt.Fprintln(env.stderr, line)
		}
		if !skipInvalid {
			return nil, exitf(exitDataErr, "%d %s cannot be published; nothing was uploaded. Rename or remove %s, or leave %s out with --skip-invalid",
				len(refused), pluralWord(len(refused), "file", "files"), pluralWord(len(refused), "it", "them"), pluralWord(len(refused), "it", "them"))
		}
	}
	kept := files[:0]
	for _, f := range files {
		if !bad[f.path] {
			kept = append(kept, f)
		}
	}
	tree := &localTree{files: kept}
	if err := checkTreeLimits(tree); err != nil {
		return nil, err
	}
	for i := range tree.files {
		f := &tree.files[i]
		sum, size, err := hashFile(f.abs)
		if err != nil {
			return nil, exitf(exitFailed, "cannot read %s: %v", runs.SanitizeLine(f.path), unwrapPath(err))
		}
		f.sha, f.size = sum, size
	}
	sort.Slice(tree.files, func(i, j int) bool { return tree.files[i].path < tree.files[j].path })
	return tree, nil
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// checkTree is the platform's path validator over the whole tree (names,
// layout, case collisions), plus the per-file limits a publish enforces.
func checkTree(files []localFile) []refusedFile {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	var out []refusedFile
	for _, pe := range contexts.ValidateTree(paths) {
		r := refusedFile{path: pe.Path, reason: string(pe.Reason)}
		top := strings.SplitN(pe.Path, "/", 2)[0]
		switch {
		case pe.Path == "CLAUDE.md":
			r.detail = "rename it to AGENTS.md: that is the file the agents read"
		case pe.Reason == contexts.ReasonBadTopLevel && !strings.Contains(pe.Path, "/"):
			r.detail = "only AGENTS.md lives at the top; put reference files under files/"
		case pe.Reason == contexts.ReasonBadTopLevel && top != "skills" && top != "files":
			r.detail = "put reference files under files/ and skills under skills/<name>/"
		case pe.Reason == contexts.ReasonBadCharacter && top == "skills" && strings.Count(pe.Path, "/") >= 2:
			if !contexts.ValidSkillName(strings.SplitN(pe.Path, "/", 3)[1]) {
				r.detail = "a skill folder name is lowercase letters, digits and dashes"
			}
		}
		out = append(out, r)
	}
	skillDirs, skillFiles := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		switch {
		case f.size > api.ContextMaxFileBytes:
			out = append(out, refusedFile{f.path, "too_large", "a file is at most 100 MiB"})
		case f.path == contexts.InstructionsPath && f.size > ctxMaxAgentsMD:
			out = append(out, refusedFile{f.path, "too_large", "AGENTS.md is at most 32 KiB"})
		}
		parts := strings.SplitN(f.path, "/", 3)
		if len(parts) == 3 && parts[0] == "skills" && contexts.ValidSkillName(parts[1]) {
			skillDirs[parts[1]] = true
			if parts[2] == contexts.SkillFileName {
				skillFiles[parts[1]] = true
				if f.size > ctxMaxSkillMD {
					out = append(out, refusedFile{f.path, "skill_invalid", "SKILL.md is at most 64 KiB"})
				}
			}
		}
	}
	for dir := range skillDirs {
		if !skillFiles[dir] {
			out = append(out, refusedFile{"skills/" + dir + "/" + contexts.SkillFileName, "skill_invalid", "every skill folder needs a SKILL.md"})
		}
	}
	return out
}

func checkTreeLimits(t *localTree) error {
	if len(t.files) > ctxMaxFiles {
		return exitf(exitDataErr, "%d files: a context version holds at most %d", len(t.files), ctxMaxFiles)
	}
	if total := t.totalBytes(); total > ctxMaxTotalBytes {
		return exitf(exitDataErr, "%s in total: a context version holds at most 250 MiB", humanBytes(total))
	}
	skills := map[string]bool{}
	for _, f := range t.files {
		if parts := strings.SplitN(f.path, "/", 3); len(parts) == 3 && parts[0] == "skills" {
			skills[parts[1]] = true
		}
	}
	if len(skills) > ctxMaxSkills {
		return exitf(exitDataErr, "%d skills: a context holds at most %d", len(skills), ctxMaxSkills)
	}
	return nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ─── push ──────────────────────────────────────────────────────────────────

func doContextPush(ctx context.Context, env *runsEnv, ref, dir string, opts pushOptions) error {
	local, err := scanLocal(env, dir, opts.skipInvalid)
	if err != nil {
		return err
	}
	d, err := env.client.GetContext(ctx, ref)
	if ctxCode(err) == "context_not_found" && opts.create {
		if !contexts.ValidContextName(ref) {
			return usageErrorf("%s", contextNameRule(ref))
		}
		if opts.dryRun {
			d = &api.ContextDetail{Context: api.Context{Name: ref, Version: 0}, Files: []api.ContextFile{}}
			err = nil
		} else {
			var c *api.Context
			idem := newIdempotencyKey()
			err = ctxRetry(ctx, env, "create", func() error {
				var cerr error
				c, cerr = env.client.CreateContext(ctx, ref, "", idem)
				return cerr
			})
			if err == nil {
				fmt.Fprintf(env.stderr, "context %s created\n", runs.SanitizeLine(c.Name))
				d = &api.ContextDetail{Context: *c, Files: []api.ContextFile{}}
			}
		}
	}
	if err != nil {
		if ctxCode(err) == "context_not_found" {
			return usageErrorf("no context %s: create it with nan context create %s, or add --create",
				runs.SanitizeLine(ref), runs.SanitizeLine(ref))
		}
		return ctxExit(err, env, scopeContextsRead)
	}
	return pushTree(ctx, env, d, local, opts)
}

// treeDiff is what a push changes against the version it is based on.
type treeDiff struct {
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
}

func (d treeDiff) empty() bool { return len(d.Added)+len(d.Changed)+len(d.Removed) == 0 }

func diffTree(remote []api.ContextFile, local *localTree) treeDiff {
	d := treeDiff{Added: []string{}, Changed: []string{}, Removed: []string{}}
	have := make(map[string]api.ContextFile, len(remote))
	for _, f := range remote {
		have[f.Path] = f
	}
	seen := map[string]bool{}
	for _, f := range local.files {
		seen[f.path] = true
		r, ok := have[f.path]
		switch {
		case !ok:
			d.Added = append(d.Added, f.path)
		case r.SHA256 != f.sha || r.Executable != f.exec:
			d.Changed = append(d.Changed, f.path)
		}
	}
	for _, f := range remote {
		if !seen[f.Path] {
			d.Removed = append(d.Removed, f.Path)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Changed)
	sort.Strings(d.Removed)
	return d
}

func printDiff(w io.Writer, d treeDiff, sizes map[string]int64) {
	for _, p := range d.Added {
		fmt.Fprintf(w, "  + %s (%s)\n", runs.SanitizeLine(p), humanBytes(sizes[p]))
	}
	for _, p := range d.Changed {
		fmt.Fprintf(w, "  ~ %s (%s)\n", runs.SanitizeLine(p), humanBytes(sizes[p]))
	}
	for _, p := range d.Removed {
		fmt.Fprintf(w, "  - %s\n", runs.SanitizeLine(p))
	}
}

// uploadPlan is the blobs a push sends: one local file per missing sha.
type uploadPlan struct {
	files []localFile
	bytes int64
}

func planUploads(local *localTree, missing []string) uploadPlan {
	want := map[string]bool{}
	for _, s := range missing {
		want[s] = true
	}
	var p uploadPlan
	for _, f := range local.files {
		if want[f.sha] {
			p.files = append(p.files, f)
			p.bytes += f.size
			delete(want, f.sha)
		}
	}
	return p
}

func distinctShas(local *localTree) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range local.files {
		if !seen[f.sha] {
			seen[f.sha] = true
			out = append(out, f.sha)
		}
	}
	sort.Strings(out)
	return out
}

type dryRunReport struct {
	Context     string   `json:"context"`
	BaseVersion int      `json:"base_version"`
	Diff        treeDiff `json:"changes"`
	Upload      struct {
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	} `json:"upload"`
}

func pushTree(ctx context.Context, env *runsEnv, d *api.ContextDetail, local *localTree, opts pushOptions) error {
	name := runs.SanitizeLine(d.Name)
	sizes := map[string]int64{}
	for _, f := range local.files {
		sizes[f.path] = f.size
	}
	diff := diffTree(d.Files, local)
	if diff.empty() && d.Version > 0 {
		if opts.json {
			return printJSON(env.stdout, d.Context, true)
		}
		fmt.Fprintf(env.stdout, "context %s is up to date (version %d)\n", name, d.Version)
		return nil
	}
	var missing []string
	if len(local.files) > 0 {
		err := ctxRetry(ctx, env, "check", func() error {
			var err error
			missing, err = env.client.CheckBlobs(ctx, distinctShas(local))
			return err
		})
		if err != nil {
			return ctxExit(err, env, scopeContextsWrite)
		}
	}
	plan := planUploads(local, missing)
	if opts.dryRun {
		if opts.json {
			r := dryRunReport{Context: d.Name, BaseVersion: d.Version, Diff: diff}
			r.Upload.Files, r.Upload.Bytes = len(plan.files), plan.bytes
			return printJSON(env.stdout, r, true)
		}
		fmt.Fprintf(env.stdout, "context %s: version %d -> %d (dry run, nothing changed)\n", name, d.Version, d.Version+1)
		printDiff(env.stdout, diff, sizes)
		fmt.Fprintf(env.stdout, "%d added, %d changed, %d removed; %d %s to upload (%s)\n", len(diff.Added), len(diff.Changed),
			len(diff.Removed), len(plan.files), pluralWord(len(plan.files), "file", "files"), humanBytes(plan.bytes))
		return nil
	}
	if err := uploadBlobs(ctx, env, plan); err != nil {
		return err
	}

	req := api.PublishRequest{BaseVersion: d.Version, AllowSecretLike: opts.allowSecrets}
	for _, f := range local.files {
		req.Files = append(req.Files, api.PublishFile{Path: f.path, SHA256: f.sha, Executable: f.exec})
	}
	idem := newIdempotencyKey()
	missingRounds, rebases := 0, 0
	for {
		var c *api.Context
		err := ctxRetry(ctx, env, "publish", func() error {
			var err error
			c, err = env.client.PublishContext(ctx, d.ID, req, idem)
			return err
		})
		if err == nil {
			return reportPushed(env, c, diff, plan, opts)
		}
		ce, ok := api.AsContextError(err)
		if !ok {
			return ctxExit(err, env, scopeContextsWrite)
		}
		switch ce.Code {
		case "blob_missing":
			// The platform's cleanup removed a blob between the check and
			// the publish: check again and upload what it lacks.
			if missingRounds >= ctxRetries {
				return ctxExit(err, env, scopeContextsWrite)
			}
			missingRounds++
			fmt.Fprintf(env.stderr, "%d %s no longer stored on nan.builders; uploading again\n", len(ce.MissingBlobs),
				pluralWord(len(ce.MissingBlobs), "file is", "files are"))
			again, cerr := env.client.CheckBlobs(ctx, ce.MissingBlobs)
			if cerr != nil {
				return ctxExit(cerr, env, scopeContextsWrite)
			}
			if err := uploadBlobs(ctx, env, planUploads(local, again)); err != nil {
				return err
			}
			continue
		case "version_conflict":
			current := "a newer version"
			if ce.CurrentVersion != nil {
				current = fmt.Sprintf("version %d", *ce.CurrentVersion)
			}
			if !opts.force {
				return exitf(exitFailed, "context %s changed while pushing (it is now at %s; this push was based on version %d). "+
					"Nothing was published. Push again to compare with it, or add --force to publish on top of it", name, current, req.BaseVersion)
			}
			if rebases >= ctxRetries {
				return exitf(exitFailed, "context %s keeps changing; nothing was published, try again", name)
			}
			rebases++
			latest, gerr := env.client.GetContext(ctx, d.ID)
			if gerr != nil {
				return ctxExit(gerr, env, scopeContextsRead)
			}
			fmt.Fprintf(env.stderr, "context %s changed to version %d meanwhile; publishing on top of it (--force). Against it, this push:\n",
				name, latest.Version)
			diff = diffTree(latest.Files, local)
			printDiff(env.stderr, diff, sizes)
			req.BaseVersion = latest.Version
			idem = newIdempotencyKey()
			continue
		case "context_secret_detected":
			for _, h := range ce.Findings {
				fmt.Fprintf(env.stderr, "  %s:%d looks like %s\n", runs.SanitizeLine(h.Path), h.Line, runs.SanitizeLine(h.Pattern))
			}
			return exitf(exitDataErr, "some files look like they hold credentials; nothing was published. Keep credentials in nan secrets "+
				"(the agent can read and copy anything in a context), or publish anyway with --allow-secrets")
		case "invalid_path", "unsupported_file_type", "skill_invalid", "context_too_large":
			for _, r := range ce.Files {
				line := fmt.Sprintf("  %s: %s", runs.SanitizeLine(r.Path), runs.SanitizeLine(r.Reason))
				if r.Detail != "" {
					line += " (" + runs.SanitizeLine(r.Detail) + ")"
				}
				fmt.Fprintln(env.stderr, line)
			}
			return exitf(exitDataErr, "%s Nothing was published", runs.SanitizeLine(ce.Message))
		}
		return ctxExit(err, env, scopeContextsWrite)
	}
}

func reportPushed(env *runsEnv, c *api.Context, diff treeDiff, plan uploadPlan, opts pushOptions) error {
	if opts.json {
		return printJSON(env.stdout, c, true)
	}
	fmt.Fprintf(env.stdout, "context %s: version %d published (%d %s, %s; %d added, %d changed, %d removed; uploaded %d %s, %s)\n",
		runs.SanitizeLine(c.Name), c.Version, c.FileCount, pluralWord(c.FileCount, "file", "files"), humanBytes(c.TotalBytes),
		len(diff.Added), len(diff.Changed), len(diff.Removed), len(plan.files), pluralWord(len(plan.files), "file", "files"), humanBytes(plan.bytes))
	return nil
}

// uploadBlobs sends every blob of plan, ctxConcurrency at a time. The first
// failure stops the push after the uploads in flight finish; a multipart
// upload that was cut is resumed by the next push.
func uploadBlobs(ctx context.Context, env *runsEnv, plan uploadPlan) error {
	if len(plan.files) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan localFile)
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	workers := max(1, min(ctxConcurrency, len(plan.files)))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := uploadOne(ctx, env, f); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, f := range plan.files {
		select {
		case jobs <- f:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

func uploadOne(ctx context.Context, env *runsEnv, f localFile) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var err error
	if f.size <= ctxSingleMax {
		err = ctxRetry(ctx, env, "upload "+runs.SanitizeLine(f.path), func() error { return putSingle(ctx, env, f) })
	} else {
		err = putMultipart(ctx, env, f)
	}
	if err != nil {
		var exit *ExitError
		if errors.As(err, &exit) {
			return err
		}
		e := ctxExit(err, env, scopeContextsWrite)
		return &ExitError{Code: e.Code, Err: fmt.Errorf("upload of %s failed: %w", runs.SanitizeLine(f.path), e.Err)}
	}
	return nil
}

// openChecked opens a file being uploaded and makes sure it is still the
// size it was hashed at (the platform verifies the sha256 too).
func openChecked(f localFile) (*os.File, error) {
	fh, err := os.Open(f.abs)
	if err != nil {
		return nil, exitf(exitFailed, "cannot read %s: %v", runs.SanitizeLine(f.path), unwrapPath(err))
	}
	if st, err := fh.Stat(); err != nil || st.Size() != f.size {
		fh.Close()
		return nil, exitf(exitFailed, "%s changed while pushing; push again", runs.SanitizeLine(f.path))
	}
	return fh, nil
}

func putSingle(ctx context.Context, env *runsEnv, f localFile) error {
	fh, err := openChecked(f)
	if err != nil {
		return err
	}
	defer fh.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	_, err = env.client.PutBlob(ctx, f.sha, io.NewSectionReader(fh, 0, f.size), f.size, path.Base(f.path))
	return err
}

// uploadState is what a push remembers about a multipart upload it did not
// finish: enough to ask the platform where it stands. The id alone grants
// nothing without the member's credential.
type uploadState struct {
	UploadID string `json:"upload_id"`
	Size     int64  `json:"size"`
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func uploadStatePath(sha string) (string, error) {
	if !shaPattern.MatchString(sha) {
		return "", fmt.Errorf("bad sha")
	}
	dir, err := ctxUploadStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sha+".json"), nil
}

func loadUploadState(sha string, size int64) *uploadState {
	p, err := uploadStatePath(sha)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var s uploadState
	if json.Unmarshal(b, &s) != nil || s.Size != size || s.UploadID == "" {
		return nil
	}
	return &s
}

func saveUploadState(sha string, s uploadState) {
	p, err := uploadStatePath(sha)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(p, b, 0o600)
}

func clearUploadState(sha string) {
	if p, err := uploadStatePath(sha); err == nil {
		_ = os.Remove(p)
	}
}

// validUpload checks the platform's description of an upload against the
// file: a part size this client can send and the part count it implies.
func validUpload(u *api.ContextUpload, size int64) bool {
	if u == nil || u.PartSize <= 0 || u.PartSize > api.ContextPartSize || u.NextPart < 1 {
		return false
	}
	parts := (size + int64(u.PartSize) - 1) / int64(u.PartSize)
	return int64(u.Parts) == parts && u.NextPart <= u.Parts+1
}

func putMultipart(ctx context.Context, env *runsEnv, f localFile) error {
	fh, err := openChecked(f)
	if err != nil {
		return err
	}
	defer fh.Close()
	shown := runs.SanitizeLine(f.path)

	var up *api.ContextUpload
	if s := loadUploadState(f.sha, f.size); s != nil {
		st, err := env.client.UploadStatus(ctx, f.sha, s.UploadID)
		switch {
		case err == nil && validUpload(st, f.size):
			up = st
			up.UploadID = s.UploadID
			fmt.Fprintf(env.stderr, "resuming the upload of %s at part %d of %d\n", shown, up.NextPart, up.Parts)
		case err == nil:
			// An upload this client cannot continue: drop it, or its marker
			// would refuse the new one (upload_in_progress).
			_ = env.client.AbortUpload(ctx, f.sha, s.UploadID)
			clearUploadState(f.sha)
		case ctxCode(err) == "upload_not_found", ctxCode(err) == "upload_expired":
			clearUploadState(f.sha)
		default:
			return err
		}
	}
	if up == nil {
		var blob *api.ContextBlob
		err := ctxRetry(ctx, env, "upload "+shown, func() error {
			var err error
			up, blob, err = env.client.StartUpload(ctx, f.sha, f.size, path.Base(f.path))
			return err
		})
		if err != nil {
			return err
		}
		if blob != nil {
			return nil // the platform already holds it
		}
		if !validUpload(up, f.size) {
			return exitf(exitUnavailable, "nan.builders answered an upload of %s this CLI cannot send; update nan (nan --version)", shown)
		}
		saveUploadState(f.sha, uploadState{UploadID: up.UploadID, Size: f.size})
	}

	failures := 0
	for n := up.NextPart; n <= up.Parts; {
		off := int64(n-1) * int64(up.PartSize)
		size := min(int64(up.PartSize), f.size-off)
		pctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		next, err := env.client.PutPart(pctx, f.sha, up.UploadID, n, io.NewSectionReader(fh, off, size), size)
		cancel()
		if err == nil && next > n {
			n = next
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if code := ctxCode(err); code == "upload_not_found" || code == "upload_expired" {
			clearUploadState(f.sha)
			return err
		}
		failures++
		if failures > ctxRetries {
			if err == nil {
				err = fmt.Errorf("the platform did not take part %d", n)
			}
			return err
		}
		if ce, ok := api.AsContextError(err); ok && retryable(ce) && ce.RetryAfter > 0 && ce.RetryAfter <= ctxMaxWait {
			if serr := ctxSleep(ctx, ce.RetryAfter); serr != nil {
				return err
			}
		}
		// Ask where the upload stands: the part may have been stored
		// before the answer was lost, or another attempt holds it.
		st, serr := env.client.UploadStatus(ctx, f.sha, up.UploadID)
		if serr != nil {
			return err
		}
		if !validUpload(st, f.size) {
			return exitf(exitUnavailable, "nan.builders answered an upload of %s this CLI cannot send; update nan (nan --version)", shown)
		}
		fmt.Fprintf(env.stderr, "part %d of %s did not go through; resuming at part %d\n", n, shown, st.NextPart)
		n = st.NextPart
	}
	err = ctxRetry(ctx, env, "upload "+shown, func() error {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		_, err := env.client.CompleteUpload(cctx, f.sha, up.UploadID)
		return err
	})
	if err != nil {
		if code := ctxCode(err); code == "upload_not_found" || code == "upload_expired" || code == "blob_hash_mismatch" {
			clearUploadState(f.sha)
		}
		return err
	}
	clearUploadState(f.sha)
	return nil
}

// ─── pull ──────────────────────────────────────────────────────────────────

func doContextPull(ctx context.Context, env *runsEnv, ref, dir string) error {
	shown := runs.SanitizeLine(dir)
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return usageErrorf("%s is not a folder", shown)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return usageErrorf("cannot read %s: %v", shown, unwrapPath(err))
		}
		if len(entries) > 0 && !ctxPullForce {
			return usageErrorf("%s is not empty: pull into an empty or new folder, or add --force to write into it", shown)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return usageErrorf("cannot read %s: %v", shown, unwrapPath(err))
	}

	d, err := env.client.GetContext(ctx, ref)
	if err != nil {
		return ctxExit(err, env, scopeContextsRead)
	}
	if err := checkServerTree(env, d.Files); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return usageErrorf("cannot create %s: %v", shown, unwrapPath(err))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return usageErrorf("cannot open %s: %v", shown, unwrapPath(err))
	}
	defer root.Close()

	files := append([]api.ContextFile(nil), d.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var total int64
	for _, f := range files {
		if err := pullOne(ctx, env, root, d, f); err != nil {
			return err
		}
		total += f.Size
	}
	fmt.Fprintf(env.stdout, "context %s version %d written to %s (%d %s, %s)\n", runs.SanitizeLine(d.Name), d.Version, shown,
		len(files), pluralWord(len(files), "file", "files"), humanBytes(total))
	return nil
}

// checkServerTree refuses a file list a well-behaved platform never sends:
// paths the shared validator refuses (absolute, .., case collisions,
// reserved names, ...), bad hashes or sizes. Nothing is written then.
func checkServerTree(env *runsEnv, files []api.ContextFile) error {
	if len(files) > ctxMaxFiles {
		return exitf(exitDataErr, "nan.builders sent %d files, more than a context holds; nothing was written", len(files))
	}
	paths := make([]string, len(files))
	var bad []string
	for i, f := range files {
		paths[i] = f.Path
		if !shaPattern.MatchString(f.SHA256) || f.Size < 0 || f.Size > api.ContextMaxFileBytes {
			bad = append(bad, fmt.Sprintf("%s: bad hash or size", runs.SanitizeLine(f.Path)))
		}
	}
	for _, pe := range contexts.ValidateTree(paths) {
		bad = append(bad, fmt.Sprintf("%s: %s", runs.SanitizeLine(pe.Path), pe.Reason))
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	for _, b := range bad {
		fmt.Fprintf(env.stderr, "refused %s\n", b)
	}
	return exitf(exitDataErr, "nan.builders sent %d %s this CLI will not write; nothing was written", len(bad), pluralWord(len(bad), "path", "paths"))
}

func pullOne(ctx context.Context, env *runsEnv, root *os.Root, d *api.ContextDetail, f api.ContextFile) error {
	shown := runs.SanitizeLine(f.Path)
	local := filepath.FromSlash(f.Path)
	if parent := filepath.Dir(local); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return exitf(exitFailed, "cannot create the folder of %s: %v", shown, unwrapPath(err))
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if ctxPullForce {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	mode := os.FileMode(0o644)
	if f.Executable {
		mode = 0o755
	}
	out, err := root.OpenFile(local, flags, mode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return exitf(exitFailed, "%s already exists; nothing more was written (use --force to replace files)", shown)
		}
		return exitf(exitFailed, "cannot write %s: %v", shown, unwrapPath(err))
	}
	fail := func(e *ExitError) error {
		out.Close()
		_ = root.Remove(local)
		return e
	}
	var body io.ReadCloser
	err = ctxRetry(ctx, env, "download "+shown, func() error {
		var err error
		body, err = env.client.OpenContextFile(ctx, d.ID, f.Path, d.Version)
		return err
	})
	if err != nil {
		return fail(ctxExit(err, env, scopeContextsRead))
	}
	defer body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(body, f.Size+1))
	if err != nil {
		return fail(exitf(exitUnavailable, "the download of %s was cut: %v", shown, err))
	}
	if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return fail(exitf(exitUnavailable, "the download of %s does not match its sha256 and size; it was not kept", shown))
	}
	if err := out.Close(); err != nil {
		return exitf(exitFailed, "cannot write %s: %v", shown, err)
	}
	if f.Executable && runtime.GOOS != "windows" {
		// The umask may have taken the bits off the mode OpenFile asked for.
		_ = root.Chmod(local, 0o755)
	}
	return nil
}

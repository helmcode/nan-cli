package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
)

var (
	secretsLsJSON   bool
	secretsShowJSON bool

	secretSetValueFile   string
	secretSetValueStdin  bool
	secretSetKind        string
	secretSetProvider    string
	secretSetBaseURL     string
	secretSetDescription string
	secretSetJSON        bool

	secretEditDescription string
	secretEditBaseURL     string
	secretEditJSON        bool

	secretRmYes bool
)

// secretsURL is Settings > Secrets in the portal.
const secretsURL = runs.PortalURL + "/settings"

// maxSecretValue is the platform's limit on a secret's value.
const maxSecretValue = 16 * 1024

// secretVerbHints answer the verbs a member may reach for that are not
// commands, with the one that is.
var secretVerbHints = map[string]string{
	"add": "set", "create": "set", "new": "set", "put": "set", "update": "set (the value) or nan secrets edit (description, base URL)",
	"rotate": "set", "replace": "set", "delete": "rm", "remove": "rm", "get": "show", "reveal": "show",
}

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage your secrets: values go in, only metadata comes out",
	Long: `Manage the secrets you keep for your automations: list them, see one, add
one or replace its value, edit its description or base URL, delete it.
A secret's value is never shown again after it is saved, here or anywhere
else: you see its name, kind, last characters as a hint and which
automations use it.

Kinds: generic (a value an automation hands to its runs) and provider_key
(your own key for Anthropic, OpenAI, Google Gemini or an OpenAI-compatible
endpoint, with its base URL). Run on a provider key with:
  nan run --byok-secret NAME --model ID "<prompt>"

The value is read from a file or from stdin, never from the command line,
where it would stay in your shell history:
  nan secrets set GITHUB_TOKEN --value-file ./token.txt
  pbpaste | nan secrets set GITHUB_TOKEN --value-stdin

` + writeCredentialHelp + `

The portal does all of this too, Settings > Secrets at ` + runs.PortalURL + `,
signed in through the browser.

Docs: ` + writeDocs + ` and https://nan.builders/docs/runs`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			shown := runs.Truncate(runs.SanitizeLine(args[0]), 64)
			if hint, ok := secretVerbHints[args[0]]; ok {
				return usageErrorf("there is no nan secrets %s; try: nan secrets %s", shown, hint)
			}
			return usageErrorf("unknown command %q for \"nan secrets\"; try: nan secrets --help", shown)
		}
		return cmd.Help()
	},
}

var secretsLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List your secrets: names and metadata, never values",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doSecretsLs(cmd.Context(), env) })
	},
}

var secretsShowCmd = &cobra.Command{
	Use:         "show <NAME|id>",
	Short:       "Show one secret's metadata (never its value)",
	Args:        oneSecret,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkSecretRef(args[0]); err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doSecretsShow(cmd.Context(), env, args[0]) })
	},
}

var secretsSetCmd = &cobra.Command{
	Use:   "set NAME (--value-file PATH | --value-stdin)",
	Short: "Add a secret, or replace the value of one you have",
	Long: `Add a secret, or replace the value of the one with that NAME.

The value comes from --value-file PATH or from a pipe with --value-stdin,
never from the command line. One trailing newline is dropped (the one an
editor or echo adds); anything else is kept as it is. Up to 16 KiB.

A new secret is generic unless you say otherwise. A provider key:
  nan secrets set ANTHROPIC_KEY --kind provider_key --provider anthropic --value-file key.txt
  nan secrets set LOCAL_LLM --provider openai_compatible --base-url https://llm.example.com/v1 --value-stdin
A secret's kind and provider are fixed when it is created: to change them,
delete it and add it again.

NAME: capital letters, digits and _, starting with a letter (GITHUB_TOKEN).

` + writeCredentialHelp + `

Docs: ` + writeDocs,
	Example: `  nan secrets set GITHUB_TOKEN --value-file ./token.txt --description "fine-grained, repo acme/api"
  op read op://dev/github/token | nan secrets set GITHUB_TOKEN --value-stdin`,
	Args:        oneSecretName,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		in, err := buildSecretSet(cmd, args[0])
		if err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doSecretsSet(cmd.Context(), env, in) })
	},
}

var secretsEditCmd = &cobra.Command{
	Use:   "edit <NAME|id> [--description TEXT] [--base-url URL]",
	Short: "Change a secret's description or base URL (not its value: see nan secrets set)",
	Long: `Change a secret's description, or the base URL of an openai_compatible
provider key. To replace the value, use nan secrets set.

` + writeCredentialHelp + `

Docs: ` + writeDocs,
	Args:        oneSecret,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkSecretRef(args[0]); err != nil {
			return err
		}
		var p api.SecretPatch
		if cmd.Flags().Changed("description") {
			p.Description = &secretEditDescription
		}
		if cmd.Flags().Changed("base-url") {
			p.BaseURL = &secretEditBaseURL
		}
		if p.Description == nil && p.BaseURL == nil {
			return usageErrorf("nothing to change: give --description, --base-url or both")
		}
		return withWriteEnv(func(env *runsEnv) error { return doSecretsEdit(cmd.Context(), env, args[0], p) })
	},
}

var secretsRmCmd = &cobra.Command{
	Use:     "rm <NAME|id> [--yes]",
	Aliases: []string{"delete", "remove"},
	Short:   "Delete a secret",
	Long: `Delete a secret. A secret an automation uses cannot be deleted: remove it
from those automations first; the error names them.

Asks before deleting on a terminal; elsewhere, confirm with --yes.

` + writeCredentialHelp + `

Docs: ` + writeDocs,
	Args:        oneSecret,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkSecretRef(args[0]); err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doSecretsRm(cmd.Context(), env, args[0]) })
	},
}

func init() {
	rootCmd.AddCommand(secretsCmd)
	secretsCmd.AddCommand(secretsLsCmd, secretsShowCmd, secretsSetCmd, secretsEditCmd, secretsRmCmd)
	secretsCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	secretsLsCmd.Flags().BoolVar(&secretsLsJSON, "json", false, "Print the list as JSON (metadata only)")
	secretsShowCmd.Flags().BoolVar(&secretsShowJSON, "json", false, "Print the secret's metadata as JSON")

	f := secretsSetCmd.Flags()
	f.StringVar(&secretSetValueFile, "value-file", "", "Read the value from `PATH`")
	f.BoolVar(&secretSetValueStdin, "value-stdin", false, "Read the value from stdin (a pipe, never a terminal)")
	f.StringVar(&secretSetKind, "kind", "", "generic (default) or provider_key, for a new secret")
	f.StringVar(&secretSetProvider, "provider", "", "anthropic, openai, google or openai_compatible (implies --kind provider_key)")
	f.StringVar(&secretSetBaseURL, "base-url", "", "The endpoint of an openai_compatible provider key")
	f.StringVar(&secretSetDescription, "description", "", "What the secret is for (never the value)")
	f.BoolVar(&secretSetJSON, "json", false, "Print the secret's metadata as JSON")

	secretsEditCmd.Flags().StringVar(&secretEditDescription, "description", "", "The new description (\"\" clears it)")
	secretsEditCmd.Flags().StringVar(&secretEditBaseURL, "base-url", "", "The new base URL of an openai_compatible provider key")
	secretsEditCmd.Flags().BoolVar(&secretEditJSON, "json", false, "Print the secret's metadata as JSON")

	secretsRmCmd.Flags().BoolVar(&secretRmYes, "yes", false, "Delete without asking")
}

// oneSecret is cobra.ExactArgs(1) with a message that says what is missing.
func oneSecret(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing the secret's NAME or id; see: nan secrets ls")
	case len(args) > 1:
		return usageErrorf("one secret at a time; the value goes in --value-file or --value-stdin, never on the command line")
	}
	return nil
}

// oneSecretName is oneSecret for set: one NAME, and an extra argument is
// almost certainly the value, which is then in the shell history.
func oneSecretName(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing the secret's NAME, like GITHUB_TOKEN")
	case len(args) > 1:
		return usageErrorf("nan secrets set takes the NAME only: the value goes in --value-file or --value-stdin. " +
			"If you typed the value on the command line, it is now in your shell history: revoke it where it was issued and set the new one from a file")
	}
	return checkSecretName(args[0])
}

// pastedValueHint is what to do about a value typed on the command line.
const pastedValueHint = "If that was the value, it is now in your shell history: revoke it where it was issued, " +
	"then save the new one with nan secrets set NAME --value-file PATH"

// checkSecretName checks a NAME without ever repeating something that may
// be the value itself.
func checkSecretName(name string) error {
	if secretNamePattern.MatchString(name) {
		return nil
	}
	const what = "a secret NAME is capital letters, digits and _, starting with a letter (like GITHUB_TOKEN)"
	switch upper := strings.ToUpper(name); {
	case looksLikeAKey(name):
		return usageErrorf("%s. %s", what, pastedValueHint)
	case len(name) <= 24 && secretNamePattern.MatchString(upper):
		return usageErrorf("%s. Did you mean %s?", what, upper)
	}
	return usageErrorf("%s", what)
}

// checkSecretRef accepts a NAME or a secret id.
func checkSecretRef(ref string) error {
	if runIDPattern.MatchString(ref) {
		return nil
	}
	return checkSecretName(ref)
}

// secretSet is what nan secrets set was asked to do. The value never
// appears in an error, a log line or the output.
type secretSet struct {
	name        string
	value       string
	kind        string
	provider    string
	baseURL     string
	description *string
}

func buildSecretSet(cmd *cobra.Command, name string) (*secretSet, error) {
	in := &secretSet{name: name, kind: secretSetKind, provider: secretSetProvider, baseURL: secretSetBaseURL}
	if cmd.Flags().Changed("description") {
		d := secretSetDescription
		in.description = &d
	}
	switch in.kind {
	case "", api.SecretKindGeneric, api.SecretKindProviderKey:
	default:
		return nil, usageErrorf("--kind is generic or provider_key")
	}
	if in.provider != "" && in.kind == "" {
		in.kind = api.SecretKindProviderKey
	}
	if in.provider != "" && in.kind != api.SecretKindProviderKey {
		return nil, usageErrorf("--provider is for --kind provider_key")
	}
	if in.kind == api.SecretKindProviderKey && in.provider == "" {
		return nil, usageErrorf("--kind provider_key needs --provider: anthropic, openai, google or openai_compatible")
	}
	value, err := readSecretValue(secretSetValueFile, secretSetValueStdin)
	if err != nil {
		return nil, err
	}
	in.value = value
	return in, nil
}

// readSecretValue reads the value from --value-file or a piped stdin, and
// drops one trailing newline. Errors never include the value.
func readSecretValue(path string, fromStdin bool) (string, error) {
	switch {
	case path != "" && fromStdin:
		return "", usageErrorf("give --value-file or --value-stdin, not both")
	case path == "" && !fromStdin:
		return "", usageErrorf("the value comes from --value-file PATH or --value-stdin (a pipe), never from the command line")
	}
	var (
		raw []byte
		err error
	)
	if fromStdin {
		if writeStdinTTY() {
			return "", usageErrorf("--value-stdin reads a piped value, and stdin is a terminal, where typing it would show it on screen: " +
				"pipe it (op read ... | nan secrets set NAME --value-stdin) or use --value-file PATH")
		}
		raw, err = io.ReadAll(io.LimitReader(writeStdin, maxSecretValue+3))
		if err != nil {
			return "", usageErrorf("could not read the value from stdin")
		}
	} else {
		shown := runs.SanitizeLine(path)
		f, openErr := os.Open(path)
		if openErr != nil {
			return "", usageErrorf("could not read --value-file %s: %v", shown, unwrapPath(openErr))
		}
		defer f.Close()
		if info, statErr := f.Stat(); statErr == nil {
			if info.IsDir() {
				return "", usageErrorf("--value-file %s is a directory", shown)
			}
			if runtime.GOOS != "windows" && info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
				fmt.Fprintf(writeStderr, "note: %s can be read by other users of this machine (chmod 600 %s)\n", shown, shown)
			}
		}
		raw, err = io.ReadAll(io.LimitReader(f, maxSecretValue+3))
		if err != nil {
			return "", usageErrorf("could not read --value-file %s: %v", shown, unwrapPath(err))
		}
	}
	value := string(raw)
	if strings.HasSuffix(value, "\r\n") {
		value = value[:len(value)-2]
	} else if strings.HasSuffix(value, "\n") {
		value = value[:len(value)-1]
	}
	switch {
	case value == "":
		return "", usageErrorf("the value is empty")
	case len(value) > maxSecretValue:
		return "", usageErrorf("the value is over %d KiB, the most a secret holds", maxSecretValue/1024)
	case !utf8.ValidString(value):
		return "", usageErrorf("the value is not UTF-8 text")
	}
	return value, nil
}

// listSecrets lists the member's secrets: a session reads the portal's
// route (any session may, whatever the write API), a token /v1/secrets
// with secrets:read.
func listSecrets(ctx context.Context, env *runsEnv) (*api.SecretList, error) {
	if env.cred.kind == credSession {
		list, err := env.client.ListSecrets(ctx)
		if err != nil {
			var apiErr *api.APIError
			if errors.As(err, &apiErr) && apiErr.Status == 404 {
				return nil, exitf(exitAuth, "secrets are not enabled for your account")
			}
			return nil, apiExit(err, env.cred)
		}
		return list, nil
	}
	list, err := env.client.SecretsV1(ctx)
	if err != nil {
		return nil, secretsV1Exit(err, env.cred, scopeSecretsRead)
	}
	return list, nil
}

// secretsV1Exit is writeExit for GET /v1/secrets, where a 404 means the
// platform has no /v1/secrets for the member (write API off).
func secretsV1Exit(err error, cred credential, scope string) *ExitError {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return writeAPIOff("secrets")
	}
	return writeExit(err, cred, "secrets", scope)
}

func findSecret(list *api.SecretList, ref string) *api.Secret {
	for i := range list.Data {
		if list.Data[i].Name == ref || list.Data[i].ID == ref {
			return &list.Data[i]
		}
	}
	return nil
}

func secretNotFound(ref string) *ExitError {
	return usageErrorf("no secret named %q; see: nan secrets ls", runs.Truncate(runs.SanitizeLine(ref), 64))
}

func doSecretsLs(ctx context.Context, env *runsEnv) error {
	list, err := listSecrets(ctx, env)
	if err != nil {
		return err
	}
	if secretsLsJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintln(env.stderr, "No secrets yet. Add one with: nan secrets set NAME --value-file PATH")
		return nil
	}
	// The base URL column only when there is one: it is set on
	// OpenAI-compatible provider keys only.
	withURL := false
	for _, s := range list.Data {
		if s.BaseURL != nil && *s.BaseURL != "" {
			withURL = true
		}
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	header := "NAME\tKIND\tPROVIDER\tHINT\tUSED BY\tLAST USED\tUPDATED\tCHANGED FROM"
	if withURL {
		header += "\tBASE URL"
	}
	fmt.Fprintln(tw, header)
	for _, s := range list.Data {
		lastUsed := "never"
		if s.LastUsedAt != nil {
			lastUsed = localTime(*s.LastUsedAt)
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			runs.Truncate(runs.SanitizeLine(s.Name), 64),
			runs.SanitizeLine(s.Kind), orDash(s.Provider, 24), orDash(s.Hint, 12), runs.Truncate(usedByList(s), 40),
			lastUsed, localTime(s.UpdatedAt), dashIfEmpty(updatedByLabel(s.UpdatedBy)))
		if withURL {
			line += "\t" + orDash(s.BaseURL, 60)
		}
		fmt.Fprintln(tw, line)
	}
	return tw.Flush()
}

func orDash(v *string, max int) string {
	if v == nil || *v == "" {
		return "-"
	}
	return runs.Truncate(runs.SanitizeLine(*v), max)
}

func dashIfEmpty(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func usedByList(s api.Secret) string {
	used := make([]string, 0, len(s.UsedBy))
	for _, u := range s.UsedBy {
		used = append(used, runs.SanitizeLine(u.Label()))
	}
	if len(used) == 0 {
		return "-"
	}
	return strings.Join(used, ", ")
}

func doSecretsShow(ctx context.Context, env *runsEnv, ref string) error {
	list, err := listSecrets(ctx, env)
	if err != nil {
		return err
	}
	s := findSecret(list, ref)
	if s == nil {
		return secretNotFound(ref)
	}
	if secretsShowJSON {
		return printJSON(env.stdout, s, true)
	}
	printSecret(env, s)
	return nil
}

func printSecret(env *runsEnv, s *api.Secret) {
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" && v != "-" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("name", runs.SanitizeLine(s.Name))
	row("id", runs.SanitizeLine(s.ID))
	row("kind", runs.SanitizeLine(s.Kind))
	row("provider", orDash(s.Provider, 40))
	row("base url", orDash(s.BaseURL, 200))
	row("hint", orDash(s.Hint, 12))
	if s.ValueBytes > 0 {
		row("size", fmt.Sprintf("%d bytes", s.ValueBytes))
	}
	row("description", runs.Truncate(runs.SanitizeLine(s.Description), 256))
	row("used by", usedByList(*s))
	row("last changed from", updatedByLabel(s.UpdatedBy))
	row("created", localTime(s.CreatedAt))
	row("updated", localTime(s.UpdatedAt))
	lastUsed := "never"
	if s.LastUsedAt != nil {
		lastUsed = localTime(*s.LastUsedAt)
	}
	row("last used", lastUsed)
	_ = tw.Flush()
}

// newIdempotencyKey is the Idempotency-Key of one nan secrets set: a retry
// of the same request after a lost answer returns the secret the first
// attempt created instead of 409 name_taken.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "nan-cli-" + hex.EncodeToString(b)
}

// createAttempts is how many times nan secrets set sends a create whose
// answer was lost on the way (never one the platform refused).
var createAttempts = 2

func doSecretsSet(ctx context.Context, env *runsEnv, in *secretSet) error {
	if err := requireWriteAPI(ctx, env, "secrets"); err != nil {
		return err
	}
	list, err := env.client.SecretsV1(ctx)
	var existing *api.Secret
	if err != nil {
		var apiErr *api.APIError
		// A token with secrets:write and not secrets:read cannot look the
		// name up: it can still create.
		if !(errors.As(err, &apiErr) && apiErr.Code == "token_scope_denied") {
			return secretsV1Exit(err, env.cred, scopeSecretsRead)
		}
	} else {
		existing = findSecret(list, in.name)
	}
	if existing == nil {
		return createSecret(ctx, env, in, list != nil && err == nil)
	}
	return replaceSecret(ctx, env, in, existing)
}

func createSecret(ctx context.Context, env *runsEnv, in *secretSet, looked bool) error {
	req := api.CreateSecretRequest{Name: in.name, Value: in.value, Kind: in.kind, Provider: in.provider, BaseURL: in.baseURL, Description: in.description}
	key := newIdempotencyKey()
	var (
		s        *api.Secret
		replayed bool
		err      error
	)
	for attempt := 0; attempt < createAttempts; attempt++ {
		s, replayed, err = env.client.CreateSecret(ctx, req, key)
		if err == nil || !lostAnswer(err) {
			break
		}
	}
	if err != nil {
		var apiErr *api.APIError
		if !looked && errors.As(err, &apiErr) && apiErr.Code == "name_taken" {
			return usageErrorf("%s already exists, and %s cannot look it up to replace its value: that needs the %s scope too",
				in.name, env.cred.describe(), scopeSecretsRead)
		}
		return writeExit(err, env.cred, "secrets", scopeSecretsWrite)
	}
	if secretSetJSON {
		return printJSON(env.stdout, s, true)
	}
	verb := "created"
	if replayed {
		verb = "created (the platform had it from the first attempt)"
	}
	fmt.Fprintf(env.stdout, "secret %s %s: %s, %d bytes, hint %s\n", runs.SanitizeLine(s.Name), verb,
		runs.SanitizeLine(s.Kind), s.ValueBytes, orDash(s.Hint, 12))
	return nil
}

// lostAnswer is a request that may or may not have reached the platform:
// no answer came back. A refusal, a 5xx or an expired session is not one.
func lostAnswer(err error) bool {
	var apiErr *api.APIError
	return !errors.As(err, &apiErr) && !errors.Is(err, api.ErrSessionExpired) && !errors.Is(err, context.Canceled)
}

func replaceSecret(ctx context.Context, env *runsEnv, in *secretSet, existing *api.Secret) error {
	if in.kind != "" && in.kind != existing.Kind {
		return usageErrorf("%s is a %s secret: the kind is fixed when a secret is created (delete it and add it again to change it)",
			in.name, runs.SanitizeLine(existing.Kind))
	}
	if in.provider != "" && (existing.Provider == nil || *existing.Provider != in.provider) {
		return usageErrorf("%s has another provider: the provider is fixed when a secret is created (delete it and add it again to change it)", in.name)
	}
	s, err := env.client.ReplaceSecretValue(ctx, existing.ID, in.value)
	if err != nil {
		return writeExit(err, env.cred, "secrets", scopeSecretsWrite)
	}
	if in.description != nil || in.baseURL != "" {
		p := api.SecretPatch{Description: in.description}
		if in.baseURL != "" {
			p.BaseURL = &in.baseURL
		}
		if s, err = env.client.UpdateSecret(ctx, existing.ID, p); err != nil {
			fmt.Fprintln(env.stderr, "the value was replaced, but not the description or base URL:")
			return writeExit(err, env.cred, "secrets", scopeSecretsWrite)
		}
	}
	if secretSetJSON {
		return printJSON(env.stdout, s, true)
	}
	fmt.Fprintf(env.stdout, "secret %s: value replaced, %d bytes, hint %s\n", runs.SanitizeLine(s.Name), s.ValueBytes, orDash(s.Hint, 12))
	if len(s.UsedBy) > 0 {
		fmt.Fprintf(env.stderr, "the next runs of these automations get the new value: %s\n", usedByList(*s))
	}
	return nil
}

// resolveSecretForWrite finds a secret by NAME or id through /v1/secrets.
func resolveSecretForWrite(ctx context.Context, env *runsEnv, ref string) (*api.Secret, error) {
	if err := requireWriteAPI(ctx, env, "secrets"); err != nil {
		return nil, err
	}
	list, err := env.client.SecretsV1(ctx)
	if err != nil {
		return nil, secretsV1Exit(err, env.cred, scopeSecretsRead)
	}
	s := findSecret(list, ref)
	if s == nil {
		return nil, secretNotFound(ref)
	}
	return s, nil
}

func doSecretsEdit(ctx context.Context, env *runsEnv, ref string, p api.SecretPatch) error {
	existing, err := resolveSecretForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	s, err := env.client.UpdateSecret(ctx, existing.ID, p)
	if err != nil {
		return writeExit(err, env.cred, "secrets", scopeSecretsWrite)
	}
	if secretEditJSON {
		return printJSON(env.stdout, s, true)
	}
	fmt.Fprintf(env.stdout, "secret %s updated\n", runs.SanitizeLine(s.Name))
	return nil
}

func doSecretsRm(ctx context.Context, env *runsEnv, ref string) error {
	existing, err := resolveSecretForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	name := runs.SanitizeLine(existing.Name)
	if err := confirm(env, secretRmYes, "delete secret "+name+"? Its value cannot be recovered"); err != nil {
		return err
	}
	if err := env.client.DeleteSecret(ctx, existing.ID); err != nil {
		return writeExit(err, env.cred, "secrets", scopeSecretsWrite)
	}
	fmt.Fprintf(env.stdout, "secret %s deleted\n", name)
	return nil
}

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/spf13/cobra"
)

var secretsLsJSON bool

// secretsURL is where secrets are written: Settings > Secrets in the portal.
const secretsURL = runs.PortalURL + "/settings"

// portalOnlySecretVerbs are the writes a member may reach for. Each one
// answers with where to do it instead of "unknown command".
var portalOnlySecretVerbs = map[string]bool{
	"set": true, "add": true, "create": true, "new": true, "put": true, "update": true, "edit": true,
	"rotate": true, "replace": true, "rm": true, "delete": true, "remove": true, "get": true, "show": true, "reveal": true,
}

const secretsPortalOnly = "secrets are written in the portal only: Settings > Secrets at " + secretsURL +
	". Writing or replacing a secret needs you signed in through the browser, so that nothing that can read this " +
	"machine's CLI session or tokens (an agent in a workspace, a CI job) can swap one for its own. " +
	"A secret's value is never shown again after it is saved, anywhere: nan secrets ls lists names and metadata"

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "List your secrets (names and metadata only)",
	Long: `List the secrets you keep for your automations: names, kinds, the last
characters as a hint, and which automations use each one. Values are never
shown, here or anywhere else, after they are saved.

Kinds: generic (a value an automation hands to its runs) and provider_key
(your own key for Anthropic, OpenAI, Google Gemini or an OpenAI-compatible
endpoint, with its base URL). Run on a provider key with:
  nan run --byok-secret NAME --model ID "<prompt>"

Portal-only, and why: adding, replacing and deleting secrets happen in the
portal, Settings > Secrets at ` + runs.PortalURL + `, signed in
through the browser. A secret written from a credential an agent or a CI job
could read (this machine's CLI session, a token) could be swapped for one
the attacker controls.

nan secrets ls needs your nan auth login session: platform tokens and API
keys cannot read secrets.

Docs: https://nan.builders/docs/runs`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && portalOnlySecretVerbs[args[0]] {
			return usageErrorf("%s", secretsPortalOnly)
		}
		if len(args) > 0 {
			return usageErrorf("unknown command %q for \"nan secrets\"; try: nan secrets --help", runs.Truncate(runs.SanitizeLine(args[0]), 64))
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
		env, err := newSessionEnv()
		if err != nil {
			return err
		}
		return runtimeError(doSecretsLs(cmd.Context(), env))
	},
}

func init() {
	rootCmd.AddCommand(secretsCmd)
	secretsCmd.AddCommand(secretsLsCmd)
	secretsLsCmd.Flags().BoolVar(&secretsLsJSON, "json", false, "Print the list as JSON (metadata only)")
}

// newSessionEnv is newRunsEnv for the commands only a signed-in session can
// use. NAN_TOKEN, --token-file and a saved token are skipped rather than
// sent: the platform refuses them there, and the saved session is the one
// credential that works.
func newSessionEnv() (*runsEnv, error) {
	sess, err := session.Load()
	if err != nil && !errors.Is(err, session.ErrNotLoggedIn) {
		return nil, exitf(exitAuth, "could not read your session (%v): run nan auth login", err)
	}
	if sess == nil || sess.Token == "" {
		return nil, exitf(exitAuth, "nan secrets needs your session: run nan auth login (platform tokens and API keys cannot read secrets)")
	}
	cred := credential{kind: credSession, session: sess.Token}
	return &runsEnv{client: cred.client(), cred: cred, stdout: os.Stdout, stderr: os.Stderr}, nil
}

func doSecretsLs(ctx context.Context, env *runsEnv) error {
	list, err := env.client.ListSecrets(ctx)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return exitf(exitAuth, "secrets are not enabled for your account")
		}
		return apiExit(err, env.cred)
	}
	if secretsLsJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintf(env.stderr, "No secrets yet. Add them in the portal: Settings > Secrets at %s\n", secretsURL)
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
	header := "NAME\tKIND\tPROVIDER\tHINT\tUSED BY\tLAST USED\tUPDATED"
	if withURL {
		header += "\tBASE URL"
	}
	fmt.Fprintln(tw, header)
	for _, s := range list.Data {
		hint := "-"
		if s.Hint != nil && *s.Hint != "" {
			hint = runs.Truncate(runs.SanitizeLine(*s.Hint), 12)
		}
		provider := "-"
		if s.Provider != nil && *s.Provider != "" {
			provider = runs.Truncate(runs.SanitizeLine(*s.Provider), 24)
		}
		used := make([]string, 0, len(s.UsedBy))
		for _, u := range s.UsedBy {
			used = append(used, runs.SanitizeLine(u.Label()))
		}
		usedBy := "-"
		if len(used) > 0 {
			usedBy = runs.Truncate(strings.Join(used, ", "), 40)
		}
		lastUsed := "never"
		if s.LastUsedAt != nil {
			lastUsed = localTime(*s.LastUsedAt)
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s",
			runs.Truncate(runs.SanitizeLine(s.Name), 64),
			runs.SanitizeLine(s.Kind), provider, hint, usedBy, lastUsed, localTime(s.UpdatedAt))
		if withURL {
			baseURL := "-"
			if s.BaseURL != nil && *s.BaseURL != "" {
				baseURL = runs.Truncate(runs.SanitizeLine(*s.BaseURL), 60)
			}
			line += "\t" + baseURL
		}
		fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(env.stderr, "add, replace or delete secrets in the portal: Settings > Secrets at %s\n", secretsURL)
	return nil
}

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
	"go.yaml.in/yaml/v3"
)

// The write API (helmcode/nan-devops AGENT-AUTOMATIONS-PHASE3-WRITE-API.md):
// secrets and automations written from this CLI. The platform accepts a CLI
// session (nan auth login) or a platform token with the route's scope
// (secrets:read, secrets:write, automations:write); it refuses API keys
// (sk-), workspace keys and sessions from older clients. Approving a
// proposal stays portal-only.

// writeDocs is where the CLI's write commands are documented.
const writeDocs = "https://nan.builders/docs/nan-cli"

// The scopes the write commands need, as the platform names them.
const (
	scopeSecretsRead      = "secrets:read"
	scopeSecretsWrite     = "secrets:write"
	scopeAutomationsWrite = "automations:write"
)

// writeCredentialHelp is the part of every write command's help that says
// what it authenticates with and what that hands out.
const writeCredentialHelp = `Authentication, first match wins: --token-file, your session from
nan auth login, NAN_TOKEN, a platform token saved with nan auth login
--api-token. A platform token needs the scope of what it does:
secrets:read to list and show secrets, secrets:write to change them,
automations:write to change automations (automations:read to find one by
name). Those scopes are added to a token in the portal only, Settings >
Tokens, signed in through the browser. API keys (sk-) and workspace keys
never read or write secrets and never change automations.

Whoever holds a credential that can write here can change your secrets and
automations: an automation receives the secrets you assign it, so a
credential that can write automations can reach every secret you have. Sign
in with nan auth login on your own machine, not inside a workspace (an
agent there could read the session file). In a workspace or CI job, use a
token with only the scopes the job needs.`

// Test seams: where a confirmation or a piped value is read from, and
// whether that is a terminal.
var (
	writeStdin    io.Reader = os.Stdin
	writeStdinTTY           = func() bool { return isTerminal(os.Stdin) }
	writeStdout   io.Writer = os.Stdout
	writeStderr   io.Writer = os.Stderr
)

// newWriteEnv picks the credential of a secrets or write command: an
// explicit --token-file first, then the session (a CLI session can do
// everything here without any scope set up), then NAN_TOKEN, then a saved
// platform token. An API key is refused before it is sent: the platform
// never takes one here.
func newWriteEnv() (*runsEnv, error) {
	if tokenFileFlag != "" {
		tok, err := readTokenFile(tokenFileFlag)
		if err != nil {
			return nil, err
		}
		return writeEnvFor(credential{kind: credFile, path: tokenFileFlag, bearer: tok})
	}
	sess, err := session.Load()
	if err != nil && !errors.Is(err, session.ErrNotLoggedIn) {
		return nil, exitf(exitAuth, "could not read your session (%v): run nan auth login", err)
	}
	if sess != nil && sess.Token != "" {
		return writeEnvFor(credential{kind: credSession, session: sess.Token})
	}
	if v := strings.TrimSpace(os.Getenv(tokenEnvVar)); v != "" {
		if !validTokenShape(v) {
			return nil, exitf(exitAuth, "%s is set but does not hold a valid token: %s", tokenEnvVar, tokenShapeHint)
		}
		return writeEnvFor(credential{kind: credEnv, bearer: v})
	}
	if sess != nil && sess.PlatformToken != "" {
		return writeEnvFor(credential{kind: credStoredToken, bearer: sess.PlatformToken})
	}
	return nil, exitf(exitAuth, "not signed in: run nan auth login, or set %s to a platform token with the scope you need (see --help)", tokenEnvVar)
}

func writeEnvFor(cred credential) (*runsEnv, error) {
	if strings.HasPrefix(cred.bearer, "sk-") {
		return nil, exitf(exitAuth, "%s is an API key (sk-): API keys never read or write secrets and never change automations. "+
			"Sign in with nan auth login, or use a platform token (nan_pat_) with the scope you need, created at %s/settings", cred.describe(), runs.PortalURL)
	}
	return &runsEnv{client: cred.client(), cred: cred, stdout: writeStdout, stderr: writeStderr}, nil
}

// withWriteEnv is withRunsEnv for the secrets and write commands.
func withWriteEnv(fn func(*runsEnv) error) error {
	env, err := newWriteEnv()
	if err != nil {
		return err
	}
	return runtimeError(fn(env))
}

// writeAPIOff is the answer when the platform does not take writes from
// this CLI (yet) for the member: the portal still does them.
func writeAPIOff(what string) *ExitError {
	return usageErrorf("changing %s from the CLI is not available for your account yet: do it in the portal at %s, "+
		"signed in through the browser. See %s", what, runs.PortalURL, writeDocs)
}

// requireWriteAPI asks the platform, for a session, whether it takes writes
// from the CLI (features.write_api). A token has no /api/auth/me: its
// request is sent and a refusal is mapped by writeExit instead. When the
// question itself fails for another reason than an expired session, the
// write goes ahead and the platform decides.
func requireWriteAPI(ctx context.Context, env *runsEnv, what string) error {
	if env.cred.kind != credSession {
		return nil
	}
	f, err := env.client.Features(ctx)
	if errors.Is(err, api.ErrSessionExpired) {
		return authError(env.cred)
	}
	if err == nil && !f.WriteAPI {
		return writeAPIOff(what)
	}
	return nil
}

// writeExit maps a refused write (or a read of the write API) onto an exit
// code and a message that says what to do. what is "secrets" or
// "automations"; scope is the platform-token scope of the request.
func writeExit(err error, cred credential, what, scope string) *ExitError {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return apiExit(err, cred)
	}
	msg := runs.SanitizeLine(apiErr.Message)
	switch apiErr.Code {
	case "browser_session_required":
		// A platform that predates the write API, or has it off.
		return writeAPIOff(what)
	case "credential_not_allowed":
		if cred.kind == credSession {
			return exitf(exitAuth, "the platform does not take this session for changing %s: it is from an older sign-in. "+
				"Sign in again with: nan auth login", what)
		}
		return exitf(exitAuth, "%s cannot do this: %s needs a CLI session or a platform token with the %s scope", cred.describe(), what, scope)
	case "workspace_key_not_allowed":
		return exitf(exitAuth, "a workspace key never reads or writes secrets and never changes automations: "+
			"use your own session (nan auth login) or a platform token with the %s scope", scope)
	case "token_scope_denied":
		return exitf(exitAuth, "%s lacks a scope: %s. Create a token with the %s scope in the portal, Settings > Tokens at %s, "+
			"signed in through the browser (secrets:read, secrets:write and automations:write can only be added there)",
			cred.describe(), msg, scope, runs.PortalURL)
	case "rate_limited":
		return exitf(exitUnavailable, "too many changes to your %s recently: try again later", what)
	case "name_taken":
		return usageErrorf("that name is already taken by another of your %s", what)
	case "too_many_secrets":
		return usageErrorf("you have as many secrets as an account can keep: delete one first (nan secrets rm NAME)")
	case "idempotency_key_reused":
		return usageErrorf("that idempotency key was already used to create a different secret")
	case "secret_in_use":
		return usageErrorf("the secret is assigned to these automations; remove it from them first (nan automations edit):\n%s",
			joinNames(refLabels(apiErr.Automations)))
	case "secrets_too_large":
		return usageErrorf("with the new value, the secrets these automations receive would be over the size a run takes:\n%s",
			joinNames(refLabels(apiErr.Automations)))
	case "platform_access_required":
		return exitf(exitAuth, "your membership does not include this: see %s/membership", runs.PortalURL)
	case "secrets_unavailable":
		return exitf(exitUnavailable, "the secrets store is unavailable right now; try again in a few minutes")
	}
	if apiErr.Status == 404 {
		return usageErrorf("not found (%s)", msg)
	}
	return apiExit(err, cred)
}

func refLabels(refs []api.Ref) []string {
	out := make([]string, 0, len(refs))
	for i := range refs {
		out = append(out, refs[i].Label())
	}
	return out
}

// updatedByLabel is the updated_by credential kind, as a person reads it.
func updatedByLabel(v string) string {
	switch v {
	case "browser_session":
		return "Browser"
	case "cli_session":
		return "CLI"
	case "platform_token":
		return "API token"
	case "":
		return ""
	}
	return runs.SanitizeLine(v)
}

// maxBodyFile bounds an automation or trigger file. The platform's own
// limit is lower; this only keeps a wrong file from being read whole.
const maxBodyFile = 1 << 20

// readBodyFile reads -f: a YAML or JSON file ("-" for stdin) holding one
// object, and returns it as compact JSON, sent as written.
func readBodyFile(path string) (json.RawMessage, error) {
	if path == "" {
		return nil, usageErrorf("-f FILE is required: a YAML or JSON file (- for stdin); see --help")
	}
	var (
		raw []byte
		err error
	)
	shown := runs.SanitizeLine(path)
	if path == "-" {
		if writeStdinTTY() {
			return nil, usageErrorf("-f - reads the file from stdin, and stdin is a terminal: pipe it, or give a path")
		}
		raw, err = io.ReadAll(io.LimitReader(writeStdin, maxBodyFile+1))
	} else {
		var f *os.File
		f, err = os.Open(path)
		if err != nil {
			return nil, usageErrorf("could not read %s: %v", shown, unwrapPath(err))
		}
		raw, err = io.ReadAll(io.LimitReader(f, maxBodyFile+1))
		f.Close()
	}
	if err != nil {
		return nil, usageErrorf("could not read %s: %v", shown, unwrapPath(err))
	}
	if len(raw) > maxBodyFile {
		return nil, usageErrorf("%s is over %d KiB", shown, maxBodyFile/1024)
	}
	if !utf8.Valid(raw) {
		return nil, usageErrorf("%s is not UTF-8 text", shown)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var compact bytes.Buffer
		if err := json.Compact(&compact, trimmed); err != nil {
			return nil, usageErrorf("%s is not valid JSON: %v", shown, err)
		}
		return compact.Bytes(), nil
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, usageErrorf("%s is neither valid JSON nor valid YAML: %v", shown, err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, usageErrorf("%s must hold one object (name: ..., workspace: ...), not a list or a value", shown)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, usageErrorf("%s has a value JSON cannot carry: %v", shown, err)
	}
	return out, nil
}

// confirm asks a yes/no question on a terminal. Without one, the answer has
// to be given with --yes: a script never deletes by accident.
func confirm(env *runsEnv, yes bool, question string) error {
	if yes {
		return nil
	}
	if !writeStdinTTY() {
		return usageErrorf("%s: confirm with --yes (stdin is not a terminal)", question)
	}
	fmt.Fprintf(env.stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(writeStdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return exitf(exitFailed, "cancelled: nothing was changed")
}

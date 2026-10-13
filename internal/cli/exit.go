package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
)

// Exit codes of `nan run` and `nan runs`. A script that starts a run needs to
// know how it ended without parsing text, so these are a contract: the first
// five are the run's own outcome, the rest follow sysexits.h.
const (
	exitSucceeded = 0
	exitFailed    = 1
	exitTimedOut  = 2
	exitCancelled = 3
	exitConfig    = 4
	// exitAwaitingApproval: the agent is done and its proposed actions wait
	// for the member's approval in the portal (runs.ExitAwaitingApproval).
	exitAwaitingApproval = runs.ExitAwaitingApproval
	exitUsage            = 64
	exitAuth             = 65
	exitUnavailable      = 69
	exitDetached         = 75
)

// ExitError ends the process with Code, printing Err first when there is one.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func exitf(code int, format string, args ...any) *ExitError {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

func usageErrorf(format string, args ...any) *ExitError {
	return exitf(exitUsage, format, args...)
}

// authError is a refused credential, named (never shown) with what fixes it.
func authError(cred credential) *ExitError {
	switch cred.kind {
	case credEnv, credFile:
		return exitf(exitAuth, "nan.builders refused %s: it may be revoked or expired; create a new one in Settings > Tokens at https://cloud.nan.builders", cred.describe())
	case credStoredToken:
		return exitf(exitAuth, "nan.builders refused %s. Save a new one with: nan auth login --api-token. While a token is saved, it is used before your email session; nan auth logout removes it", cred.describe())
	case credStoredKey:
		return exitf(exitAuth, "nan.builders refused %s: run: nan auth login", cred.describe())
	}
	return exitf(exitAuth, "%v - run: nan auth login", api.ErrSessionExpired)
}

// apiExit maps a failed request onto an exit code and a message that says
// what to do next. The platform's own message is guest-adjacent text too, so
// it is sanitised like everything else.
func apiExit(err error, cred credential) *ExitError {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit
	}
	if errors.Is(err, api.ErrSessionExpired) {
		return authError(cred)
	}
	if errors.Is(err, session.ErrNotLoggedIn) {
		return &ExitError{Code: exitAuth, Err: err}
	}
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return &ExitError{Code: exitUnavailable, Err: err}
	}
	msg := runs.SanitizeLine(apiErr.Error())
	switch apiErr.Code {
	case "tier_restricted":
		return exitf(exitAuth, "your membership does not include runs - see https://cloud.nan.builders/membership")
	case "workspace_key_not_allowed":
		return exitf(exitAuth, "a workspace key cannot start or read runs - sign in with: nan auth login")
	case "agent_not_installed", "no_inference_key", "config_error":
		return exitf(exitConfig, "%s", msg)
	case "idempotency_conflict":
		return usageErrorf("that idempotency key was already used for a different run")
	case "run_queue_full":
		return exitf(exitUnavailable, "too many runs are queued - wait for some to finish (%s)", msg)
	case "rate_limited":
		return exitf(exitUnavailable, "too many runs started in the last hour - try again later")
	case "browser_session_required":
		return exitf(exitAuth, "%s", browserOnly)
	case "token_scope_denied":
		return exitf(exitAuth, "%s: %s", msg, scopeHint(cred))
	case "automation_disabled":
		return exitf(exitConfig, "that automation is disabled: enable it in the portal at %s", runs.AutomationsURL)
	case "automation_rate_limited":
		return exitf(exitUnavailable, "that automation has started as many runs as its hourly limit allows; try again later (%s)", msg)
	case "skipped_active":
		return exitf(exitUnavailable, "that automation already has a run in progress, and it is set to skip a new one while one is active")
	case "not_awaiting_approval":
		return usageErrorf("that run is not awaiting approval; see its state with: nan runs show <id>")
	case "secrets_unavailable":
		return exitf(exitUnavailable, "the secrets store is unavailable right now; try again in a few minutes")
	}
	switch apiErr.Status {
	case 400:
		if apiErr.Param != "" {
			return usageErrorf("%s: %s", runs.SanitizeLine(apiErr.Param), msg)
		}
		return usageErrorf("%s", msg)
	case 401:
		return exitf(exitAuth, "%v", session.ErrNotLoggedIn)
	case 403:
		return exitf(exitAuth, "%s", msg)
	case 404:
		return usageErrorf("%s", msg)
	}
	return exitf(exitUnavailable, "nan.builders could not do that right now: %s", msg)
}

// browserOnly explains the one class of refusal no credential of this CLI
// can get past.
var browserOnly = "this needs you signed in through the browser: do it in the portal at " + runs.PortalURL +
	". Approving a proposal, writing a secret and creating or editing an automation are portal-only, so that " +
	"nothing that can read this machine's CLI session or tokens (an agent in a workspace, a CI job) can do them. " +
	"See https://nan.builders/docs/runs"

// scopeHint is what to do about a platform token missing a scope.
func scopeHint(cred credential) string {
	switch cred.kind {
	case credEnv, credFile, credStoredToken:
		return "create a token with that scope in Settings > Tokens at " + runs.PortalURL + " (existing tokens keep the scopes they were created with)"
	}
	return "sign in with: nan auth login"
}

// runExit is the exit for a run that ended - or the detach code for one
// that has not.
func runExit(run *api.Run) error {
	code, ok := runs.ExitCode(run)
	if !ok {
		return &ExitError{Code: exitDetached}
	}
	if code == exitSucceeded {
		return nil
	}
	return &ExitError{Code: code}
}

func joinNames(names []string) string {
	clean := make([]string, len(names))
	for i, n := range names {
		clean[i] = "  " + runs.SanitizeLine(n)
	}
	return strings.Join(clean, "\n")
}

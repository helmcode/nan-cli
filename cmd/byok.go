package cmd

import (
	"errors"
	"regexp"
	"strings"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
)

// BYOK (bring your own key): a run that uses the member's own provider key,
// saved in the portal as a secret of kind provider_key, instead of NaN's
// inference. The flag names the secret, never the key: the key is written
// in the portal only and never leaves the platform except into the run's
// agent, in the member's own workspace.

// secretNamePattern is the platform's secret name grammar.
var secretNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// byokDocs is where BYOK is explained.
const byokDocs = "https://nan.builders/docs/runs#use-your-own-provider-key-byok"

// byokSessionRequired is the refusal of an ad-hoc BYOK run with a token or
// an API key (AGENT-AUTOMATIONS-PHASE3-BYOK.md §4.3).
const byokSessionRequired = "runs with your own provider key need your session: sign in with nan auth login. " +
	"Platform tokens (nan_pat_...) and API keys (sk-...) cannot start them, because the prompt of a run can make " +
	"the agent print the key it holds. A token can trigger an automation that uses your key: nan automations run <name>"

// keyPrefixes are how provider keys usually start: a --byok-secret that
// starts like one is probably the key itself, pasted by mistake.
var keyPrefixes = []string{"sk-", "sk_", "AIza", "gsk_", "xai-", "pk-", "key-", "nan_pat_"}

func hasKeyPrefix(v string) bool {
	for _, p := range keyPrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

// looksLikeAKey is a secret name that is probably the key itself. A model id
// is checked by its prefix only: model ids can be long.
func looksLikeAKey(v string) bool { return hasKeyPrefix(v) || len(v) >= 32 }

// pastedKeyHint is what to do about a key typed on the command line.
const pastedKeyHint = "If that was the key, it is now in your shell history: revoke it at your provider, " +
	"then save the new one in the portal, Settings > Secrets at " + secretsURL

// checkBYOKFlags checks --byok-secret and its --model. The value is never
// repeated in an error when it is not a secret name: it may be the key.
func checkBYOKFlags(opts runOptions) error {
	name := opts.byokSecret
	if !secretNamePattern.MatchString(name) {
		const what = "--byok-secret takes the NAME of a provider key you saved in the portal (like ANTHROPIC_KEY: " +
			"capital letters, digits and _), never the key itself; your names are in: nan secrets ls"
		switch upper := strings.ToUpper(name); {
		case looksLikeAKey(name):
			return usageErrorf("%s. %s", what, pastedKeyHint)
		case len(name) <= 24 && secretNamePattern.MatchString(upper):
			return usageErrorf("%s. Did you mean %s?", what, upper)
		}
		return usageErrorf("%s", what)
	}
	if opts.model == "" {
		return usageErrorf("--byok-secret needs --model: the id of a model your provider serves (like claude-sonnet-4-5); " +
			"NaN does not pick one for your key")
	}
	if hasKeyPrefix(opts.model) {
		return usageErrorf("--model takes the id of a model (like claude-sonnet-4-5), never a key. %s", pastedKeyHint)
	}
	if strings.HasPrefix(opts.model, "-") {
		return usageErrorf("--model must not start with -")
	}
	return nil
}

// byokCredential is the credential of an ad-hoc BYOK run: the session from
// nan auth login, and nothing else. NAN_TOKEN and --token-file are an
// explicit choice, so they are refused rather than silently skipped; a saved
// platform token or API key is skipped for the session it would otherwise
// outrank.
func byokCredential(getenv func(string) string, tokenFile string) (credential, error) {
	switch {
	case getenv(tokenEnvVar) != "":
		return credential{}, exitf(exitAuth, "%s is set, and %s", tokenEnvVar, byokSessionRequired)
	case tokenFile != "":
		return credential{}, exitf(exitAuth, "--token-file was given, and %s", byokSessionRequired)
	}
	sess, err := session.Load()
	if err != nil && !errors.Is(err, session.ErrNotLoggedIn) {
		return credential{}, exitf(exitAuth, "could not read your session (%v): %s", err, byokSessionRequired)
	}
	if sess == nil || sess.Token == "" {
		return credential{}, exitf(exitAuth, "%s", byokSessionRequired)
	}
	return credential{kind: credSession, session: sess.Token}, nil
}

// byokRefusal maps the platform's refusals of a run's inference choice onto
// what to do about them. nil: not one of them.
func byokRefusal(apiErr *api.APIError, req *api.CreateRunRequest) *ExitError {
	byok := req.Inference != nil && req.Inference.Source == api.InferenceBYOK
	msg := runs.SanitizeLine(apiErr.Message)
	switch {
	case apiErr.Code == "session_required":
		return exitf(exitAuth, "%s", byokSessionRequired)
	case apiErr.Code == "tier_restricted" && byok:
		return exitf(exitAuth, "runs with your own provider key need a workspace of yours that is active "+
			"(or a NaN inference plan): see %s", runs.PortalURL)
	case apiErr.Code == "tier_restricted":
		return exitf(exitAuth, "your membership does not include runs on NaN inference: see %s/membership. "+
			"With a workspace of your own you can run with your own provider key: "+
			"nan run --byok-secret NAME --model ID (%s)", runs.PortalURL, byokDocs)
	case !byok:
		return nil
	case apiErr.Param == "inference.secret" && apiErr.Status == 404:
		return usageErrorf("you have no secret named %s: save your provider key as a Provider key in the portal, "+
			"Settings > Secrets at %s; your secret names are in: nan secrets ls", req.Inference.Secret, secretsURL)
	case apiErr.Param == "inference.secret":
		return usageErrorf("--byok-secret %s: %s. A run uses a secret of kind provider_key (see nan secrets ls); "+
			"save your key as a Provider key in the portal, Settings > Secrets at %s", req.Inference.Secret, msg, secretsURL)
	case apiErr.Param == "inference.model":
		return usageErrorf("--model: %s", msg)
	case apiErr.Param == "inference":
		return exitf(exitAuth, "runs with your own provider key are not available for your account: %s", msg)
	}
	return nil
}

// providerNames are the providers a provider key can be for.
var providerNames = map[string]string{
	"openai_compatible": "OpenAI-compatible",
	"anthropic":         "Anthropic",
	"openai":            "OpenAI",
	"gemini":            "Google Gemini",
}

func providerName(p *string) string {
	if p == nil || *p == "" {
		return ""
	}
	if n, ok := providerNames[*p]; ok {
		return n
	}
	return runs.SanitizeLine(*p)
}

// inferenceLabel is the inference of a run or an automation, for show.
func inferenceLabel(in *api.Inference) string {
	switch {
	case in == nil || in.Source == "" || in.Source == api.InferenceNaN:
		return "NaN"
	case !in.BYOK():
		return runs.SanitizeLine(in.Source)
	}
	label := "your key"
	if in.Secret != nil && *in.Secret != "" {
		label += " " + runs.SanitizeLine(*in.Secret)
	}
	if p := providerName(in.Provider); p != "" {
		label += " (" + p + ")"
	}
	return label + ": billed by your provider, not metered by NaN"
}

// inferenceShort is the inference of a run, for a table.
func inferenceShort(in *api.Inference) string {
	switch {
	case in == nil || in.Source == "" || in.Source == api.InferenceNaN:
		return "nan"
	case !in.BYOK():
		return runs.SanitizeLine(in.Source)
	}
	if in.Secret != nil && *in.Secret != "" {
		return "key " + runs.Truncate(runs.SanitizeLine(*in.Secret), 32)
	}
	return "key"
}

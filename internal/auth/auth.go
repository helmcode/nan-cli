// Package auth is the sign-in flow, shared by the `nan auth login` command and
// by the panel.
//
// It lived inside cmd/ until the panel learned to sign a member in on its own.
// Leaving it there would have meant the TUI importing a cobra command package
// to make three HTTP calls, or a second copy of the flow that drifts from the
// first - and the whole point of doing it in the panel is that it is the same
// flow, reached without having to quit and come back.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	sessionCookie = "nan_session"
	// The domain the platform sends its sign-in links from. Subdomains count:
	// the link lands on the web app, the API lives next door.
	linkDomain = "nan.builders"

	// How long a sign-in link works for, in the platform's own words on its
	// login page: "It expires in 15 minutes and can only be used once." This
	// flow spends long enough waiting for a member to fetch a link out of an
	// inbox that the number is worth saying: an email that turns up late turns
	// up dead, and the paste prompt cannot tell anybody that.
	LinkValidity = "15 minutes"
)

// SignInBaseURL is where the three calls of the sign-in go: the link
// request, the link exchange and the two-factor challenge. Nothing else: the
// rest of the CLI (internal/api - nan me, logout, runs, the panel's tabs)
// keeps its own production URLs.
//
// A variable and not a constant for two readers only: the tests, which point
// the sign-in at a server of their own, and a build for QA, which points it at
// the dev platform without a flag or an environment variable that a released
// binary would carry:
//
//	go build -ldflags "-X github.com/nxssie/nan-cli/internal/auth.SignInBaseURL=https://cloud-api-dev.nan.builders" -o nan-qa .
//	HOME=$(mktemp -d) ./nan-qa auth login --email you@example.com
//
// Run a QA build with a HOME of its own. It saves the dev session in
// ~/.config/nan/session.json, over the member's production one, and every
// command after the sign-in sends that dev session to production: the panel
// takes the refusal for an expired session and clears it, and logout asks
// production, not dev, to end it.
//
// Nothing else sets it.
var SignInBaseURL = "https://cloud-api.nan.builders"

func loginRequestURL() string { return SignInBaseURL + "/api/auth/login/request" }
func loginVerifyURL() string  { return SignInBaseURL + "/api/auth/login/verify" }

// clientKind is what this CLI declares on the verify POST, so the platform
// records the session it hands back as a CLI session. A CLI session can
// read, start runs and reject proposals; approving a proposal, writing a
// secret and editing an automation need a session signed in through the
// browser. The session file this CLI keeps is readable by anything running
// as the member - an agent in a workspace included - and must never be able
// to do those. The platform never takes a client's word for "browser": that
// kind is proven by the browser flow itself, so declaring "cli" here only
// ever narrows what the session can do.
const clientKind = "cli"

// A timeout, because http.DefaultClient has none: a connection that is
// accepted and then never answered - a captive portal, a hotel network, a
// firewall that drops instead of refusing - hangs the panel on a spinner with
// no way out but ctrl-c.
const requestTimeout = 30 * time.Second

func RequestSignInLink(email string) error {
	body, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Post(loginRequestURL(), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("too many sign-in attempts, wait a few minutes")
	case resp.StatusCode >= 400:
		return fmt.Errorf("could not send the sign-in link (HTTP %d)", resp.StatusCode)
	}
	// 202 comes back whether or not the address belongs to a member, so a
	// successful call here is not proof that an email is on the way.
	return nil
}

// Accepts the whole link, or just the token if the mail client mangled it.
func TokenFromLink(pasted string) (string, error) {
	if pasted == "" {
		return "", fmt.Errorf("nothing pasted")
	}
	if strings.Contains(pasted, "://") {
		u, err := url.Parse(pasted)
		if err != nil {
			return "", fmt.Errorf("that does not parse as a link: %w", err)
		}
		// Ours, or nothing. The token in a link from somewhere else is not one
		// this exchange can spend, and a member who has been sent a link that
		// only looks like ours is better told that here than after we have
		// posted whatever was in it and read back an error about it.
		if !isLinkHost(u.Hostname()) {
			return "", fmt.Errorf("that link points at %s, not %s", u.Hostname(), linkDomain)
		}
		token := u.Query().Get("token")
		if token == "" {
			// The host and not the link: a link with no `token` in its query
			// can still be carrying one somewhere this does not read, and the
			// message is rendered in the panel and copied into issues.
			return "", fmt.Errorf("that %s link carries no token", u.Hostname())
		}
		return token, nil
	}
	if strings.ContainsAny(pasted, " \t") {
		return "", fmt.Errorf("that is neither a link nor a token")
	}
	return pasted, nil
}

func isLinkHost(host string) bool {
	host = strings.ToLower(host)
	return host == linkDomain || strings.HasSuffix(host, "."+linkDomain)
}

// The browser flow ends on a page that POSTs the token and gets the session
// cookie back. This does the same POST and keeps the cookie instead of
// following the redirect, which is the whole reason the old flow had to send
// people into DevTools.
//
// An account with two-factor authentication gets no session from the link
// alone: the answer is a challenge, returned here as *MFARequiredError, and
// the session comes from answering it (MFARequiredError.Complete).
func ExchangeToken(token string) (string, error) {
	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// mfa=1 says this build can answer a two-factor challenge. Without it the
	// platform refuses an account that has a factor outright, with a reason
	// that tells the member to update - which is what every build before this
	// one gets, since none of them could ask for the code.
	form := url.Values{"token": {token}, "client": {clientKind}, "mfa": {"1"}}
	resp, err := client.PostForm(loginVerifyURL(), form)
	if err != nil {
		return "", fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()

	var pending string
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c.Value, nil
		}
		if c.Name == mfaPendingCookie && c.Value != "" {
			pending = c.Value
		}
	}

	// A spent or expired link redirects to the platform's access-denied page
	// with the reason in the query, which is more useful than the status code.
	// A link that worked for an account with a second factor redirects to the
	// page that asks for the code, with the challenge in a cookie.
	if location, err := resp.Location(); err == nil {
		reason := location.Query().Get("reason")
		switch {
		case reason == mfaRequiredReason && pending != "":
			return "", &MFARequiredError{pending: pending}
		case reason == mfaRequiredReason:
			return "", errors.New("nan.builders asked for a two-factor code but sent no sign-in to answer it — ask for a new link and sign in again")
		case reason != "":
			return "", errors.New(linkReasonMessage(reason))
		}
	}
	return "", fmt.Errorf("no session came back (HTTP %d)", resp.StatusCode)
}

// The platform's reasons, in the query of the page it redirects a refused link
// to. Two of them have something the member can do about it, and both were
// arriving here as a slug with the underscores taken out: `invalid_link` covers
// a link that expired and a link somebody else already spent, and named
// neither, which left the person this happens to - the one whose email turned
// up late - with nothing to do but paste the same dead link again.
var linkReasons = map[string]string{
	"invalid_link": "that link expired or was already used — a link works once and " +
		"expires " + LinkValidity + " after it is sent, so ask for another one",
	"missing_token": "that link is incomplete — copy the whole link out of the most recent email",
	// What a build from before two-factor sign-in gets for an account that
	// has it. This build says it can answer the challenge, so it should never
	// be told this; if it is, the sentence is still the right one.
	"mfa_enabled_update_nan_cli": "this account uses two-factor authentication, and this nan-cli cannot ask for the code — " +
		"update it with the installer (https://github.com/helmcode/nan-cli#install) and ask for a new link",
	// An account that has not finished signing up and has two-factor
	// authentication on: the setup, behind the code, happens in the browser
	// only. The link this was is spent.
	"finish_onboarding_in_the_browser": "your NaN account is not set up yet — finish it in the browser: " +
		"ask for a new link at https://cloud.nan.builders (this one is spent), complete the setup there, " +
		"then sign in here again",
}

func linkReasonMessage(reason string) string {
	if message, ok := linkReasons[reason]; ok {
		return message
	}
	return "the link did not work: " + strings.ReplaceAll(reason, "_", " ")
}

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// Signing in is a magic link: the platform emails one, and the member pastes
// it back. Nothing here opens a browser or listens on a port. A loopback
// callback would need the platform to know about it, and pasting works over
// ssh, in a container, and on a machine with no browser at all.

// linkDomain is the domain the platform sends its links from. Subdomains
// count: the link lands on the web app, the API lives next door.
const linkDomain = "nan.builders"

// LinkValidity is how long a sign-in link works for, in the platform's own
// words on its login page: "It expires in 15 minutes and can only be used
// once." Worth saying, because an email that turns up late turns up dead.
const LinkValidity = "15 minutes"

// RequestMagicLink asks the platform to email a sign-in link.
//
// It answers 202 whether or not the address belongs to a member, so success
// here is not proof that an email is on the way, and nothing should say it
// is.
func (c *Client) RequestMagicLink(ctx context.Context, email string) error {
	err := c.do(ctx, http.MethodPost, "/api/auth/login/request", map[string]string{"email": email}, nil)
	var refused *Error
	if errors.As(err, &refused) {
		if refused.Status == http.StatusTooManyRequests {
			return errors.New("too many sign-in attempts, wait a few minutes")
		}
		return fmt.Errorf("could not send the sign-in link (HTTP %d)", refused.Status)
	}
	return err
}

// TokenFromLink accepts the whole link, or just the token if the mail client
// mangled it.
func (c *Client) TokenFromLink(pasted string) (string, error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return "", errors.New("nothing pasted")
	}
	if !strings.Contains(pasted, "://") {
		if strings.ContainsAny(pasted, " \t") {
			return "", errors.New("that is neither a link nor a token")
		}
		return pasted, nil
	}

	u, err := url.Parse(pasted)
	if err != nil {
		return "", fmt.Errorf("that does not parse as a link: %w", err)
	}
	// Ours, or nothing. The token in a link from somewhere else is not one
	// this exchange can spend, and a member who has been sent a link that
	// only looks like ours is better told that here. The refusal names the
	// host, never the link: it is shown on screen and pasted into issues.
	if !c.isOurs(u.Hostname()) {
		return "", fmt.Errorf("that link points at %s, not %s", u.Hostname(), linkDomain)
	}
	token := u.Query().Get("token")
	if token == "" {
		return "", fmt.Errorf("that %s link carries no token", u.Hostname())
	}
	return token, nil
}

// isOurs accepts nan.builders and its subdomains, and whatever portal this
// client was pointed at, so a local stand-in's links work too.
func (c *Client) isOurs(host string) bool {
	host = strings.ToLower(host)
	if host == linkDomain || strings.HasSuffix(host, "."+linkDomain) {
		return true
	}
	if c.appURL == defaultAppURL {
		return false
	}
	if u, err := url.Parse(c.appURL); err == nil && u.Hostname() != "" {
		return host == strings.ToLower(u.Hostname())
	}
	return false
}

// Verify spends a link's token for a session.
//
// The browser flow ends on a page that POSTs the token and gets the session
// cookie back. This does the same POST and keeps the cookie instead of
// following the redirect.
//
// An account with two-factor authentication gets no session from the link
// alone: the answer is a challenge, returned here as *MFARequiredError, and
// the session comes from answering it (CompleteMFA). Where the sign-in goes
// is the client's base URL, like every other call: WithBaseURL for a test,
// NAN_API_URL for a local stand-in or a QA build against the dev platform.
func (c *Client) Verify(ctx context.Context, token string) (string, error) {
	// mfa=1 says this build can answer a two-factor challenge. Without it the
	// platform refuses an account that has a factor outright, with a reason
	// that tells the member to update - which is what every build before this
	// one gets, since none of them could ask for the code.
	form := url.Values{"token": {token}, "client": {"cli"}, "mfa": {"1"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth/login/verify", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	var pending string
	for _, cookie := range resp.Cookies() {
		if cookie.Name == SessionCookie && cookie.Value != "" {
			return cookie.Value, nil
		}
		if cookie.Name == mfaPendingCookie && cookie.Value != "" {
			pending = cookie.Value
		}
	}
	// A spent or expired link redirects to the platform's access-denied
	// page with the reason in the query, which says more than the status. A
	// link that worked for an account with a second factor redirects to the
	// page that asks for the code, with the challenge in a cookie.
	if location, err := resp.Location(); err == nil {
		reason := location.Query().Get("reason")
		switch {
		case reason == mfaRequiredReason && pending != "":
			return "", &MFARequiredError{pending: pending}
		case reason == mfaRequiredReason:
			return "", errors.New("nan.builders asked for a two-factor code but sent no sign-in to answer it - " + newLinkHint)
		case reason != "":
			return "", errors.New(linkReasonMessage(reason))
		}
	}
	return "", fmt.Errorf("no session came back (HTTP %d)", resp.StatusCode)
}

// The platform's reasons, in the query of the page it redirects a refused
// link to. `invalid_link` covers a link that expired and one somebody else
// already spent; the member this happens to is the one whose email turned up
// late, so it names the clock and what to do about it.
var linkReasons = map[string]string{
	"invalid_link": "that link expired or was already used - a link works once and " +
		"expires " + LinkValidity + " after it is sent, so ask for another one",
	"missing_token": "that link is incomplete - copy the whole link out of the most recent email",
	// What a build from before two-factor sign-in gets for an account that
	// has it. This build says it can answer the challenge, so it should never
	// be told this; if it is, the sentence is still the right one.
	"mfa_enabled_update_nan_cli": "this account uses two-factor authentication, and this nan-cli cannot ask for the code - " +
		"update it with the installer (https://github.com/helmcode/nan-cli#install) and ask for a new link",
	// An account that has not finished signing up and has two-factor
	// authentication on: the setup, behind the code, happens in the browser
	// only. The link this was is spent.
	"finish_onboarding_in_the_browser": "your NaN account is not set up yet - finish it in the browser: " +
		"ask for a new link at https://cloud.nan.builders (this one is spent), complete the setup there, " +
		"then sign in here again",
}

func linkReasonMessage(reason string) string {
	if message, ok := linkReasons[reason]; ok {
		return message
	}
	// The reason comes out of a redirect's query, decoded, so it can carry
	// anything - an escape sequence, a paragraph. Only something shaped like
	// the platform's reasons is put on the terminal.
	if !isErrorCode(reason) {
		return "the link did not work, and nan.builders did not say why in a way this version can show - ask for a new link"
	}
	return "the link did not work: " + strings.ReplaceAll(reason, "_", " ")
}

// logoutTimeout is short: signing out must not hang the dashboard on a
// platform that accepts the connection and never answers.
var logoutTimeout = 10 * time.Second

// Logout ends the session on the platform, not only here. A session token
// copied off the machine before signing out otherwise keeps working until
// it expires.
//
// The session goes as a bearer and as the cookie, so an older platform that
// only reads the cookie still revokes it. A 401 is success: a session the
// platform already refuses is already over.
func (c *Client) Logout(ctx context.Context) error {
	if c.token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, logoutTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: c.token}) //nolint:gosec // G124: an outgoing request cookie; the attributes are for the server to set
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// ValidateEmail is the check before a link is asked for: obvious typos are
// caught here instead of as an email that never arrives.
func ValidateEmail(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("an email address is needed")
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return errors.New("that is not an email address")
	}
	at := strings.LastIndex(s, "@")
	domain := s[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return fmt.Errorf("%q is not a domain this could reach", domain)
	}
	return nil
}

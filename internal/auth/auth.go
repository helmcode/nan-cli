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
	loginRequestURL = "https://cloud-api.nan.builders/api/auth/login/request"
	loginVerifyURL  = "https://cloud-api.nan.builders/api/auth/login/verify"
	sessionCookie   = "nan_session"
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
	resp, err := client.Post(loginRequestURL, "application/json", bytes.NewReader(body))
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
func ExchangeToken(token string) (string, error) {
	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	form := url.Values{"token": {token}}
	resp, err := client.PostForm(loginVerifyURL, form)
	if err != nil {
		return "", fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()

	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c.Value, nil
		}
	}

	// A spent or expired link redirects to the platform's access-denied page
	// with the reason in the query, which is more useful than the status code.
	if location, err := resp.Location(); err == nil {
		if reason := location.Query().Get("reason"); reason != "" {
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
}

func linkReasonMessage(reason string) string {
	if message, ok := linkReasons[reason]; ok {
		return message
	}
	return "the link did not work: " + strings.ReplaceAll(reason, "_", " ")
}

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
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	loginRequestURL = "https://cloud-api.nan.builders/api/auth/login/request"
	loginVerifyURL  = "https://cloud-api.nan.builders/api/auth/login/verify"
	sessionCookie   = "nan_session"
)

func RequestSignInLink(email string) error {
	body, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		return err
	}
	resp, err := http.Post(loginRequestURL, "application/json", bytes.NewReader(body))
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
		token := u.Query().Get("token")
		if token == "" {
			return "", fmt.Errorf("that link carries no token: %s", pasted)
		}
		return token, nil
	}
	if strings.ContainsAny(pasted, " \t") {
		return "", fmt.Errorf("that is neither a link nor a token")
	}
	return pasted, nil
}

// The browser flow ends on a page that POSTs the token and gets the session
// cookie back. This does the same POST and keeps the cookie instead of
// following the redirect, which is the whole reason the old flow had to send
// people into DevTools.
func ExchangeToken(token string) (string, error) {
	client := &http.Client{
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
			return "", fmt.Errorf("the link did not work: %s", strings.ReplaceAll(reason, "_", " "))
		}
	}
	return "", fmt.Errorf("no session came back (HTTP %d)", resp.StatusCode)
}

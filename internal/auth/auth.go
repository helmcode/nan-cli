// Package auth is the sign-in flow, shared by the `nan auth login` command and
// by the panel.
//
// The member signs in through the browser: the CLI starts a device request,
// shows a code, and polls until the member types that code on the approval
// page and approves it (the same device flow Helmcode Desktop uses). The CLI
// never sees a password, a link or a two-factor code: the browser that
// approves satisfies them, and the session this CLI gets back is a CLI
// session, narrower than a browser one.
//
// It lived inside cmd/ until the panel learned to sign a member in on its own.
// Leaving it there would have meant the TUI importing a cobra command package
// to make two HTTP calls, or a second copy of the flow that drifts from the
// first - and the whole point of doing it in the panel is that it is the same
// flow, reached without having to quit and come back.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SignInBaseURL is where the two calls of the sign-in go: device start and
// poll. Nothing else: the rest of the CLI (internal/api - nan me, logout,
// runs, the panel's tabs) keeps its own production URLs.
//
// A variable and not a constant for two readers only: the tests, which point
// the sign-in at a server of their own, and a build for QA, which points it at
// the dev platform without a flag or an environment variable that a released
// binary would carry:
//
//	go build -ldflags "-X github.com/nxssie/nan-cli/internal/auth.SignInBaseURL=https://cloud-api-dev.nan.builders" -o nan-qa .
//	HOME=$(mktemp -d) ./nan-qa auth login
//
// Run a QA build with a HOME of its own. It saves the dev session in
// ~/.config/nan/session.json, over the member's production one, and every
// command after the sign-in sends that dev session to production: the panel
// takes the refusal for an expired session and clears it, and logout asks
// production, not dev, to end it.
//
// Nothing else sets it.
var SignInBaseURL = "https://cloud-api.nan.builders"

func deviceStartURL() string { return SignInBaseURL + "/api/auth/device/start" }
func devicePollURL() string  { return SignInBaseURL + "/api/auth/device/poll" }

// clientKind is what this CLI declares on /device/start, so the platform
// issues a CLI session (no inference key) when the request is approved. A CLI
// session can read, start runs and reject proposals; approving a proposal,
// writing a secret and editing an automation need a session signed in through
// the browser. The session file this CLI keeps is readable by anything running
// as the member - an agent in a workspace included - and must never be able
// to do those.
const clientKind = "cli"

// A timeout, because http.DefaultClient has none: a connection that is
// accepted and then never answered - a captive portal, a hotel network, a
// firewall that drops instead of refusing - hangs the panel on a spinner with
// no way out but ctrl-c.
const requestTimeout = 30 * time.Second

const (
	defaultInterval = 5 * time.Second
	// slowDownStep is what RFC 8628 section 3.5 adds to the interval on every
	// slow_down.
	slowDownStep = 5 * time.Second
	// maxTransient is how many failed polls in a row end the wait.
	maxTransient = 3
)

// now and after are variables so the tests advance time without sleeping.
var (
	now   = time.Now
	after = time.After
)

var (
	// ErrServerTooOld: the platform did not echo client_kind "cli".
	ErrServerTooOld = errors.New("nan.builders does not support signing in from the CLI with the browser yet — try again later")
	ErrDenied       = errors.New("the sign-in was denied in the browser")
	ErrExpired      = errors.New("the sign-in expired before it was approved — run nan auth login again")

	errRedirect = errors.New("nan.builders answered with a redirect; refusing to follow it")
)

// newClient is the client for start AND poll. The device_code is a bearer
// credential, and a 307/308 would replay the POST body somewhere else, so a
// redirect is never followed.
func newClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// DeviceLogin is a started device sign-in: what to show the member and what
// to poll with. DeviceCode is a bearer credential - never print or log it.
type DeviceLogin struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresAt       time.Time
	Interval        time.Duration
}

// StartDeviceLogin asks the platform for a CLI device request. The platform
// echoes client_kind; a server that does not (one from before the CLI
// variant) would have started a Helmcode Desktop request, whose approval
// mints an inference key nobody asked for - so no echo, no sign-in.
func StartDeviceLogin(ctx context.Context) (*DeviceLogin, error) {
	body, err := json.Marshal(map[string]string{"client": clientKind})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceStartURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := newClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, errRedirect
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("too many sign-in attempts, wait a few minutes")
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("could not start the sign-in (HTTP %d)", resp.StatusCode)
	}

	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		ClientKind      string `json:"client_kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errors.New("nan.builders answered the sign-in with something this version cannot read")
	}
	if out.ClientKind != clientKind {
		return nil, ErrServerTooOld
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURL == "" {
		return nil, errors.New("nan.builders answered the sign-in without a code")
	}
	if err := checkVerificationURL(out.VerificationURL); err != nil {
		return nil, err
	}
	interval := time.Duration(out.Interval) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	return &DeviceLogin{
		DeviceCode:      out.DeviceCode,
		UserCode:        out.UserCode,
		VerificationURL: out.VerificationURL,
		ExpiresAt:       now().Add(time.Duration(out.ExpiresIn) * time.Second),
		Interval:        interval,
	}, nil
}

// checkVerificationURL accepts only a page on the platform: the URL is opened
// in the member's browser, and the member types a code there.
func checkVerificationURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return errors.New("nan.builders sent a verification address this version cannot use")
	}
	base, err := url.Parse(SignInBaseURL)
	if err != nil {
		return err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(scheme == "http" && strings.EqualFold(base.Scheme, "http")) {
		return errors.New("nan.builders sent a verification address that is not https")
	}
	host := strings.ToLower(u.Hostname())
	if host != strings.ToLower(base.Hostname()) && host != "nan.builders" && !strings.HasSuffix(host, ".nan.builders") {
		return fmt.Errorf("nan.builders sent a verification address on %s, which is not a nan.builders page", host)
	}
	return nil
}

// PollResult is a finished sign-in.
type PollResult struct{ SessionToken string }

// WaitForApproval polls until the member approves, denies, the request
// expires or ctx is cancelled. It honours the advertised interval and adds
// 5 s on every slow_down (RFC 8628 section 3.5).
func WaitForApproval(ctx context.Context, d *DeviceLogin) (*PollResult, error) {
	interval := d.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	transient := 0
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-after(interval):
		}
		if !d.ExpiresAt.IsZero() && now().After(d.ExpiresAt) {
			return nil, ErrExpired
		}

		status, backoff, err := pollOnce(ctx, d.DeviceCode)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if backoff {
			interval += slowDownStep
		}
		if err != nil {
			if !isTransient(err) {
				return nil, err
			}
			transient++
			lastErr = err
			if transient >= maxTransient {
				return nil, lastErr
			}
			continue
		}
		transient = 0
		switch status.state {
		case "approved":
			if status.token == "" {
				return nil, errors.New("nan.builders approved the sign-in without a session")
			}
			return &PollResult{SessionToken: status.token}, nil
		case "denied":
			return nil, ErrDenied
		case "expired":
			return nil, ErrExpired
		}
		// pending (or an unknown status): keep waiting.
	}
}

type pollStatus struct {
	state string
	token string
}

// transientError marks a failure that is worth another tick.
type transientError struct{ error }

func (e transientError) Unwrap() error { return e.error }

func isTransient(err error) bool {
	var t transientError
	return errors.As(err, &t)
}

// pollOnce makes one poll. backoff says the platform asked to slow down. The
// device_code is never part of an error.
func pollOnce(ctx context.Context, deviceCode string) (pollStatus, bool, error) {
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return pollStatus{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, devicePollURL(), bytes.NewReader(body))
	if err != nil {
		return pollStatus{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := newClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return pollStatus{}, false, ctx.Err()
		}
		// url.Error carries the URL, never the body; still, wrap plainly.
		return pollStatus{}, false, transientError{fmt.Errorf("could not reach nan.builders: %w", err)}
	}
	defer resp.Body.Close()

	var out struct {
		Status       string `json:"status"`
		SessionToken string `json:"session_token"`
		Error        string `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return pollStatus{}, false, errRedirect
	case resp.StatusCode == http.StatusTooManyRequests:
		// Both the per-row "slow_down ..." and the per-IP "too many requests"
		// back off by one step; the second counts toward the transient budget.
		if strings.HasPrefix(out.Error, "slow_down") {
			return pollStatus{state: "pending"}, true, nil
		}
		return pollStatus{}, true, transientError{errors.New("nan.builders asked to slow down (HTTP 429)")}
	case resp.StatusCode >= 400:
		return pollStatus{}, false, fmt.Errorf("the sign-in check failed (HTTP %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK || decodeErr != nil:
		return pollStatus{}, false, transientError{fmt.Errorf("the sign-in check answered something unreadable (HTTP %d)", resp.StatusCode)}
	}
	return pollStatus{state: out.Status, token: out.SessionToken}, false, nil
}

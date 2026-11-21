package api

// Signing in is the browser-approved device flow, the one Helmcode Desktop
// uses (RFC 8628 shaped): the CLI starts a request, shows a short code and a
// page, and polls until the member types the code on that page and approves
// it. The CLI never sees a password, a link or a two-factor code - the
// browser that approves satisfies them - and the session it gets back is a CLI
// session, narrower than a browser one.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// clientKind is what this CLI declares on /device/start, so the platform
// issues a CLI session (no inference key) when the request is approved. A CLI
// session can read, start runs and reject proposals; approving a proposal,
// writing a secret and editing an automation need a session signed in through
// the browser. The session file this CLI keeps is readable by anything running
// as the member, an agent in a workspace included, and must never be able to
// do those.
const clientKind = "cli"

const (
	defaultDeviceInterval = 5 * time.Second
	// defaultDeviceTTL is what a start that names no lifetime gets. The
	// platform always sends 900; a zero must not expire the request on the
	// first tick.
	defaultDeviceTTL = 15 * time.Minute
	// slowDownStep is what RFC 8628 section 3.5 adds to the interval on every
	// slow_down.
	slowDownStep = 5 * time.Second
	// maxBackoff caps the wait between polls while the platform is failing.
	maxBackoff = time.Minute
)

// deviceNow and deviceAfter are variables so the tests advance time without
// sleeping.
var (
	deviceNow   = time.Now
	deviceAfter = time.After
)

// The ends of a device sign-in that is not an approval. They carry no advice
// on what to do next: each surface (the command, the dashboard) adds its own.
var (
	// ErrDeviceUnsupported: the platform did not echo client_kind "cli", so it
	// would have started a Helmcode Desktop request, whose approval mints an
	// inference key nobody asked for.
	ErrDeviceUnsupported = errors.New("nan.builders does not support signing in from the CLI with the browser yet")
	ErrDeviceDenied      = errors.New("the sign-in was denied in the browser")
	ErrDeviceExpired     = errors.New("the sign-in expired before it was approved")

	errDeviceRedirect = errors.New("nan.builders answered with a redirect; refusing to follow it")
)

// Device is a started device sign-in: what to show the member and what to
// poll with. DeviceCode is a bearer credential - never print or log it.
type Device struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresAt       time.Time
	Interval        time.Duration
}

// StartDevice asks the platform for a CLI device request.
func (c *Client) StartDevice(ctx context.Context) (*Device, error) {
	body, err := json.Marshal(map[string]string{"client": clientKind})
	if err != nil {
		return nil, err
	}
	status, raw, err := c.devicePost(ctx, "/api/auth/device/start", body)
	if err != nil {
		return nil, err
	}
	switch {
	case status >= 300 && status < 400:
		return nil, errDeviceRedirect
	case status == http.StatusTooManyRequests:
		return nil, errors.New("too many sign-in attempts, wait a few minutes")
	case status != http.StatusOK:
		return nil, fmt.Errorf("could not start the sign-in (HTTP %d)", status)
	}

	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		ClientKind      string `json:"client_kind"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("nan.builders answered the sign-in with something this version cannot read")
	}
	if out.ClientKind != clientKind {
		return nil, ErrDeviceUnsupported
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURL == "" {
		return nil, errors.New("nan.builders answered the sign-in without a code")
	}
	if err := c.checkVerificationURL(out.VerificationURL); err != nil {
		return nil, err
	}
	interval := time.Duration(out.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = defaultDeviceTTL
	}
	return &Device{
		DeviceCode:      out.DeviceCode,
		UserCode:        out.UserCode,
		VerificationURL: out.VerificationURL,
		ExpiresAt:       deviceNow().Add(ttl),
		Interval:        interval,
	}, nil
}

// checkVerificationURL accepts only a page on the platform: the URL is opened
// in the member's browser, and the member types a code there. url.Parse
// refuses control characters, and only an https address on the platform's own
// host or *.nan.builders gets this far, so nothing can become an argument to
// open, xdg-open or rundll32.
func (c *Client) checkVerificationURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return errors.New("nan.builders sent a verification address this version cannot use")
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(scheme == "http" && strings.EqualFold(base.Scheme, "http")) {
		return errors.New("nan.builders sent a verification address that is not https")
	}
	host := strings.ToLower(u.Hostname())
	if host != strings.ToLower(base.Hostname()) && host != platformDomain && !strings.HasSuffix(host, "."+platformDomain) {
		return fmt.Errorf("nan.builders sent a verification address on %s, which is not a nan.builders page", host)
	}
	return nil
}

// platformDomain is the domain the platform's pages live on. Subdomains
// count: the approval page lives on the web app, the API next door.
const platformDomain = "nan.builders"

// WaitForApproval polls until the member approves (it returns the session
// token), denies, the request expires or ctx is cancelled.
//
// It honours the advertised interval and adds 5 s on every slow_down. A
// platform that fails meanwhile - a 5xx, a gateway answering 502 during a
// rollout, the network dropping - is waited out with a growing pause until
// the request expires: the member may be halfway through approving in the
// browser, and losing a 15-minute sign-in to one bad answer is worse than a
// longer wait. Only an answer that cannot get better ends it.
func (c *Client) WaitForApproval(ctx context.Context, d *Device) (string, error) {
	interval := d.Interval
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	deadline := d.ExpiresAt
	if deadline.IsZero() {
		deadline = deviceNow().Add(defaultDeviceTTL)
	}
	// The platform decides when a request expires; the local clock is only a
	// backstop, one interval late, so an approval in the last interval is
	// still found by the poll that follows it.
	backstop := deadline.Add(interval)

	failures := 0
	var lastErr error
	for {
		wait := interval
		for i := 0; i < failures && wait < maxBackoff; i++ {
			wait *= 2
		}
		if wait > maxBackoff {
			wait = maxBackoff
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deviceAfter(wait):
		}

		state, token, err := c.pollDevice(ctx, d.DeviceCode)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		switch {
		case err != nil && !isTransient(err):
			return "", err
		case err != nil:
			failures++
			lastErr = err
			if errors.Is(err, errSlowDown) {
				interval += slowDownStep
			}
		default:
			failures = 0
			lastErr = nil
			switch state {
			case "approved":
				if token == "" {
					return "", errors.New("nan.builders approved the sign-in without a session")
				}
				return token, nil
			case "denied":
				return "", ErrDeviceDenied
			case "expired":
				return "", ErrDeviceExpired
			case "slow_down":
				interval += slowDownStep
			}
			// pending (or a status this version does not know): keep waiting.
		}
		if deviceNow().After(backstop) {
			if lastErr != nil {
				return "", fmt.Errorf("could not check the sign-in before it expired: %w", lastErr)
			}
			return "", ErrDeviceExpired
		}
	}
}

var errSlowDown = errors.New("nan.builders asked to slow down (HTTP 429)")

// transientError marks a failure that is worth another tick.
type transientError struct{ error }

func (e transientError) Unwrap() error { return e.error }

func isTransient(err error) bool {
	var t transientError
	return errors.As(err, &t)
}

// pollDevice makes one poll. The device_code is never part of an error.
func (c *Client) pollDevice(ctx context.Context, deviceCode string) (state, token string, err error) {
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return "", "", err
	}
	status, raw, err := c.devicePost(ctx, "/api/auth/device/poll", body)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", err
		}
		// The network failing, or one poll timing out: the next one may work.
		return "", "", transientError{err}
	}

	var out struct {
		Status       string `json:"status"`
		SessionToken string `json:"session_token"`
		Error        string `json:"error"`
	}
	decodeErr := json.Unmarshal(raw, &out)

	switch {
	case status >= 300 && status < 400:
		return "", "", errDeviceRedirect
	case status == http.StatusTooManyRequests:
		// Both the per-request "slow_down" and the per-IP "too many requests"
		// back off by one step; neither ends the wait.
		if strings.HasPrefix(out.Error, "slow_down") {
			return "slow_down", "", nil
		}
		return "", "", transientError{errSlowDown}
	case status == http.StatusRequestTimeout, status >= 500:
		// A 500 on a database hiccup, a 502/503/504 from the tunnel during a
		// rollout: the request is still open on the platform.
		return "", "", transientError{fmt.Errorf("nan.builders is having trouble (HTTP %d)", status)}
	case status == http.StatusBadRequest:
		return "", "", errors.New("nan.builders does not recognise this sign-in request")
	case status >= 400:
		return "", "", fmt.Errorf("the sign-in check failed (HTTP %d)", status)
	case status != http.StatusOK || decodeErr != nil:
		return "", "", transientError{fmt.Errorf("the sign-in check answered something unreadable (HTTP %d)", status)}
	}
	return out.Status, out.SessionToken, nil
}

// devicePost sends one JSON POST to the platform without a session, and reads
// a bounded answer. Redirects are never followed (the client refuses them):
// the device_code is a bearer credential, and a 307/308 would replay the body
// somewhere else.
func (c *Client) devicePost(ctx context.Context, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, nil, unreachable(err)
	}
	return resp.StatusCode, raw, nil
}

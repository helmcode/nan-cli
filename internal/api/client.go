// Package api speaks to nan.builders: the platform behind the web portal
// (cloud-api.nan.builders), and the inference API a member points their
// tools at (api.nan.builders/v1).
//
// Two clients, because the two halves authenticate differently. Client is
// the dashboard's: the member's session, as the cookie the browser flow
// sets. RunsClient is the agent runs': a session, a platform token or an API
// key, and a stream that must not time out.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Where the platform and the web portal live. Both are overridable so the
// CLI can be pointed at a local stand-in without a rebuild, and the tests
// can run against a server of their own.
const (
	defaultAPIURL = "https://cloud-api.nan.builders"
	defaultAppURL = "https://cloud.nan.builders"

	envAPIURL       = "NAN_API_URL"
	envAppURL       = "NAN_APP_URL"
	envInferenceURL = "NAN_INFERENCE_URL"
)

// SessionCookie is the cookie the platform keeps the session in.
const SessionCookie = "nan_session"

// ErrSessionExpired is the platform refusing the session a request carried.
// The session expires on the server, and nothing on this side knows when:
// session.json still holds it. What the platform says instead is `invalid
// session`, which names what it decided and not what to do about it, so a
// 401 comes back as this and the caller can tell it apart from a platform
// that is down.
var ErrSessionExpired = errors.New("your session has expired")

// A client with no timeout waits forever on a connection that is accepted and
// then never answered, which in the dashboard is a spinner that never stops.
const requestTimeout = 30 * time.Second

// Client is the dashboard's client: one member's session.
type Client struct {
	baseURL string
	appURL  string
	token   string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at another platform, for tests.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithAppURL overrides the web portal, whose links the sign-in accepts.
func WithAppURL(u string) Option { return func(c *Client) { c.appURL = strings.TrimRight(u, "/") } }

// New builds a client for a session token, which may be empty.
func New(token string, opts ...Option) *Client {
	c := &Client{
		baseURL: resolveBase(envAPIURL, defaultAPIURL),
		appURL:  resolveBase(envAppURL, defaultAppURL),
		token:   token,
		// No redirects: Go's default policy re-sends the cookie on the host
		// alone, scheme not considered, so a redirect to plain http on the
		// same host would put the session on the wire in the clear.
		http: &http.Client{Timeout: requestTimeout, CheckRedirect: noRedirects},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Token is what this client sends. The dashboard builds a client once, at
// start, and has to build another when a member signs in from inside it: a
// client still holding the token it started with sends an empty cookie and
// the platform answers `unauthorized`, which reads exactly like a failed
// sign-in.
func (c *Client) Token() string { return c.token }

// AppURL is the web portal this client is paired with, for printing links.
func (c *Client) AppURL() string { return c.appURL }

// APIHost names the platform when it is not the real one, so a screen can
// say which one it is talking to. Empty for nan.builders itself.
func (c *Client) APIHost() string {
	if c.baseURL == defaultAPIURL {
		return ""
	}
	if u, err := url.Parse(c.baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return c.baseURL
}

// resolveBase reads an override, keeping only its scheme and host, and
// refuses anything that would send the session in the clear to somewhere
// that is not this machine.
func resolveBase(env, fallback string) string {
	u, ok := override(env, fallback)
	if !ok {
		return fallback
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// resolveEndpoint is resolveBase for a URL whose path matters, such as the
// inference API's /v1.
func resolveEndpoint(env, fallback string) string {
	u, ok := override(env, fallback)
	if !ok {
		return fallback
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimRight(u.Path, "/")}).String()
}

func override(env, fallback string) (*url.URL, bool) {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		fmt.Fprintf(os.Stderr, "%s is not a URL, using %s\n", env, fallback)
		return nil, false
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())) {
		fmt.Fprintf(os.Stderr, "%s must be https (or http on a local address), using %s\n", env, fallback)
		return nil, false
	}
	return u, true
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if c.token != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: c.token}) //nolint:gosec // G124: an outgoing request cookie; the attributes are for the server to set
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.token != "" {
		return ErrSessionExpired
	}
	if resp.StatusCode >= 400 {
		return parseError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("nan.builders sent something this version cannot read: %w", err)
	}
	return nil
}

// maxResponseBytes caps what one answer may make this process hold. The
// largest the dashboard asks for is a usage summary; anything near this is
// a misbehaving server, not data.
const maxResponseBytes = 8 << 20

// unreachable says the network failed, and leaves a cancellation alone: a
// ctrl-c is not a connectivity problem.
func unreachable(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("could not reach nan.builders: %w", err)
}

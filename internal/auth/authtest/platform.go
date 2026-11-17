// Package authtest is a fake nan.builders device sign-in for tests: the
// /api/auth/device/start and /api/auth/device/poll routes, answering the way
// the platform's device flow does for a CLI request (nan-cloud-api
// internal/handlers/auth_device.go).
//
// It is imported by tests only, so it never reaches the binary.
package authtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	// Values a test can look for: in what was sent, and in what must never
	// be printed or written anywhere.
	DeviceCode = "fake-device-code-0123456789abcdefghijklmnop"
	UserCode   = "WDJB-MJHT"
	Session    = "fake-session-from-the-platform"
)

// Step is one scripted answer of the poll route.
type Step struct {
	Status int    // HTTP status; 0 means 200
	Body   string // JSON body
}

var (
	Pending  = Step{Body: `{"status":"pending"}`}
	Denied   = Step{Body: `{"status":"denied"}`}
	ExpiredS = Step{Body: `{"status":"expired"}`}
	// SlowDown is the exact body the platform sends for a poll that came too
	// early.
	SlowDown = Step{Status: http.StatusTooManyRequests, Body: `{"error":"slow_down — poll at the advertised interval"}`}
	// TooManyRequests is the per-IP limiter's answer.
	TooManyRequests = Step{Status: http.StatusTooManyRequests, Body: `{"error":"too many requests"}`}
)

// Approved is the CLI poll answer: a session, no key.
func Approved(token string) Step {
	b, _ := json.Marshal(map[string]string{"status": "approved", "session_token": token, "region": "EU"})
	return Step{Body: string(b)}
}

// Platform is the fake. Set the exported fields before the first request.
type Platform struct {
	*httptest.Server

	// OmitClientKind makes start answer without the client_kind echo, like a
	// server from before the CLI variant.
	OmitClientKind bool
	// StartStatus, when set, is the (error) status start answers with.
	StartStatus int
	// VerificationURL overrides the verification_url of the start answer.
	VerificationURL string
	// Interval is the advertised poll interval in seconds (default 1).
	Interval int
	// ExpiresIn is the advertised lifetime in seconds (default 900).
	ExpiresIn int
	// OnStart, when set, runs inside the start handler before it answers.
	OnStart func()
	// OnPoll, when set, runs inside the poll handler before it answers.
	OnPoll func(n int)

	mu         sync.Mutex
	script     []Step
	starts     []string
	polls      []string
	pollTimes  []time.Time
	startCount int
}

// New starts the fake. It stops when the test ends. Until a test scripts the
// poll route, every poll answers pending.
func New(t testing.TB) *Platform {
	t.Helper()
	p := &Platform{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/device/start", p.start)
	mux.HandleFunc("POST /api/auth/device/poll", p.poll)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// Script sets the poll answers, in order; the last one repeats.
func (p *Platform) Script(steps ...Step) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = steps
}

// Approve makes every following poll answer approved with token.
func (p *Platform) Approve(token string) { p.Script(Approved(token)) }

// Deny makes every following poll answer denied.
func (p *Platform) Deny() { p.Script(Denied) }

// Expire makes every following poll answer expired.
func (p *Platform) Expire() { p.Script(ExpiredS) }

// StartBodies are the bodies the start route received.
func (p *Platform) StartBodies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.starts...)
}

// Starts is how many start requests arrived.
func (p *Platform) Starts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.starts)
}

// PollBodies are the bodies the poll route received.
func (p *Platform) PollBodies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.polls...)
}

// PollTimes are when each poll arrived.
func (p *Platform) PollTimes() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.pollTimes...)
}

func (p *Platform) start(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<12))
	p.mu.Lock()
	p.starts = append(p.starts, string(body))
	p.startCount++
	status, omit, vurl := p.StartStatus, p.OmitClientKind, p.VerificationURL
	interval, expires := p.Interval, p.ExpiresIn
	hook := p.OnStart
	p.mu.Unlock()

	if hook != nil {
		hook()
	}

	w.Header().Set("Cache-Control", "no-store")
	if status != 0 {
		writeJSON(w, status, map[string]any{"error": "refused"})
		return
	}
	if vurl == "" {
		vurl = "https://cloud-api.nan.builders/api/auth/device"
	}
	if interval == 0 {
		interval = 1
	}
	if expires == 0 {
		expires = 900
	}
	out := map[string]any{
		"device_code":      DeviceCode,
		"user_code":        UserCode,
		"verification_url": vurl,
		"expires_in":       expires,
		"interval":         interval,
	}
	if !omit {
		out["client_kind"] = "cli"
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Platform) poll(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<12))
	p.mu.Lock()
	p.polls = append(p.polls, string(body))
	p.pollTimes = append(p.pollTimes, time.Now())
	n := len(p.polls)
	step := Pending
	if len(p.script) > 0 {
		i := n - 1
		if i >= len(p.script) {
			i = len(p.script) - 1
		}
		step = p.script[i]
	}
	hook := p.OnPoll
	p.mu.Unlock()

	if hook != nil {
		hook(n)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	status := step.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, step.Body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

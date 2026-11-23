// Package authtest is a fake nan.builders device sign-in for tests, answering
// the way the platform's contract does (nan-cloud-api, internal/handlers/
// auth_device.go): start with client=cli echoes client_kind, poll answers
// pending / denied / expired / approved, and approval carries the session
// token and region only.
//
// It is imported by tests only, so it never reaches the binary.
package authtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	// UserCode is what the member would type on the approval page.
	UserCode = "ABCD-EFGH"
	// DeviceCode is the bearer credential of the request: a value a test can
	// look for in everything that must never contain it.
	DeviceCode = "device-code-secret-for-a-test"
	// Session is the session token an approval ends with.
	Session = "fake-session-from-the-platform"
)

// Platform is the fake.
type Platform struct {
	*httptest.Server

	// Legacy makes start answer like a platform from before the CLI variant:
	// the body is ignored and no client_kind is echoed. Set before use.
	Legacy bool

	mu      sync.Mutex
	state   string // pending, approved, denied, expired
	fail    int    // HTTP status the next failures answer with
	failFor int
	starts  []map[string]string
	polls   int
}

// New starts the fake, pending. It stops when the test ends.
func New(t testing.TB) *Platform {
	t.Helper()
	p := &Platform{state: "pending"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/device/start", p.start)
	mux.HandleFunc("POST /api/auth/device/poll", p.poll)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// Approve makes the next poll answer approved.
func (p *Platform) Approve() { p.set("approved") }

// Deny makes the next poll answer denied.
func (p *Platform) Deny() { p.set("denied") }

// Expire makes the next poll answer expired.
func (p *Platform) Expire() { p.set("expired") }

func (p *Platform) set(state string) {
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
}

// FailNext makes the next n polls answer with status.
func (p *Platform) FailNext(status, n int) {
	p.mu.Lock()
	p.fail, p.failFor = status, n
	p.mu.Unlock()
}

// Starts are the bodies the start route received.
func (p *Platform) Starts() []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]string(nil), p.starts...)
}

// Polls is how many polls arrived.
func (p *Platform) Polls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.polls
}

func (p *Platform) start(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	p.starts = append(p.starts, body)
	p.mu.Unlock()
	kind := "desktop"
	if body["client"] == "cli" {
		kind = "cli"
	}
	if p.Legacy {
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code": DeviceCode, "user_code": UserCode, "verification_url": p.URL + "/device",
			"expires_in": 900, "interval": 1,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      DeviceCode,
		"user_code":        UserCode,
		"verification_url": p.URL + "/device",
		"expires_in":       int((15 * time.Minute).Seconds()),
		"interval":         1,
		"client_kind":      kind,
	})
}

func (p *Platform) poll(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	p.polls++
	if p.failFor > 0 {
		p.failFor--
		status := p.fail
		p.mu.Unlock()
		writeJSON(w, status, map[string]string{"error": "internal error"})
		return
	}
	state := p.state
	p.mu.Unlock()
	if body["device_code"] != DeviceCode {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid device_code"})
		return
	}
	if state == "approved" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "session_token": Session, "region": "eu"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": state})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

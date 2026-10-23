// Package authtest is a fake nan.builders sign-in for tests: the link
// exchange and the two-factor challenge, answering the way the platform's API
// contract says they answer (nan-cloud-api openspec/changes/mfa/api-contract.md,
// "POST /api/auth/login/verify", "POST /api/auth/mfa/challenge" and "CLI
// contract").
//
// It is imported by tests only, so it never reaches the binary.
package authtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// FrontendURL is the UI the platform redirects to, and the only origin
	// the challenge route accepts.
	FrontendURL = "https://cloud.nan.builders"

	// PendingCookie is the challenge cookie's name.
	PendingCookie = "__Host-nan_mfa_pending"

	// Values a test can look for: in what was sent, and in what must never
	// be written anywhere.
	LinkToken = "link-token-for-a-test"
	Pending   = "fake-pending-challenge-0123456789abcdefghij" // 43 base64url characters, like the real one
	Session   = "fake-session-from-the-platform"
	Code      = "246810"
)

// Platform is the fake. Set the exported fields before the first request.
type Platform struct {
	*httptest.Server

	// Factor: the account has a confirmed authenticator. Without it the link
	// alone signs in, exactly as before two-factor sign-in existed.
	Factor bool
	// RecoveryCodes the account has not used yet, in canonical form
	// (xxxx-xxxx-xxxx-xxxx).
	RecoveryCodes []string
	// Next, when set, answers the next challenge POST instead of checking
	// it, and is then cleared: a refusal the test wants to see handled.
	Next *Refusal

	mu sync.Mutex
	// What came in, for the test to look at.
	verifies   []url.Values
	challenges []Request

	spent       bool
	open        bool   // a challenge is waiting for its code
	ended       string // why the last challenge ended: used, exhausted, expired
	attempts    int    // left on the open challenge
	codeUsed    bool   // Code's step has been spent (anti-replay)
	lockedUntil time.Time
}

// Refusal is an answer the test forces on the challenge route.
type Refusal struct {
	Status int
	Body   string
	Header map[string]string
}

// Request is a challenge POST as it arrived.
type Request struct {
	Header http.Header
	Body   string
	// Cookie is the challenge cookie it carried, "" without one.
	Cookie string
}

// New starts the fake. It stops when the test ends.
func New(t testing.TB) *Platform {
	t.Helper()
	p := &Platform{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login/request", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /api/auth/login/verify", p.verify)
	mux.HandleFunc("POST /api/auth/mfa/challenge", p.challenge)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// Verifies are the forms the verify route received.
func (p *Platform) Verifies() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.verifies...)
}

// Challenges are the challenge POSTs the platform received.
func (p *Platform) Challenges() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.challenges...)
}

// NewLink lets one more link through, as a new email would.
func (p *Platform) NewLink() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spent = false
}

// Expire ends the open challenge the way ten minutes would.
func (p *Platform) Expire() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open {
		p.open, p.ended = false, "expired"
	}
}

// Lock pauses the account's two-factor sign-in until the given time.
func (p *Platform) Lock(until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lockedUntil = until
}

func (p *Platform) verify(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verifies = append(p.verifies, r.PostForm)

	// The link is consumed in every row of the contract's table.
	if r.PostForm.Get("token") != LinkToken || p.spent {
		http.Redirect(w, r, FrontendURL+"/access-denied?reason=invalid_link", http.StatusFound)
		return
	}
	p.spent = true

	switch {
	case !p.Factor:
		http.SetCookie(w, &http.Cookie{Name: "nan_session", Value: Session, Path: "/", Domain: ".nan.builders", Secure: true, HttpOnly: true})
		http.Redirect(w, r, FrontendURL+"/", http.StatusFound)
	case r.PostForm.Get("client") == "cli" && r.PostForm.Get("mfa") != "1":
		http.Redirect(w, r, FrontendURL+"/access-denied?reason=mfa_enabled_update_nan_cli", http.StatusFound)
	default:
		// A new challenge replaces any earlier one (D8).
		p.open, p.ended, p.attempts = true, "", 5
		http.SetCookie(w, &http.Cookie{Name: PendingCookie, Value: Pending, Path: "/", MaxAge: 600, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, FrontendURL+"/mfa?reason=mfa_required", http.StatusFound)
	}
}

func (p *Platform) challenge(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	cookie := ""
	if c, err := r.Cookie(PendingCookie); err == nil {
		cookie = c.Value
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.challenges = append(p.challenges, Request{Header: r.Header.Clone(), Body: string(body), Cookie: cookie})
	w.Header().Set("Cache-Control", "no-store")

	// The FRONTEND_URL origin only; without an Origin, refused when the
	// browser says it came from another site, through otherwise (the CLI).
	if o := r.Header.Get("Origin"); o != "" && o != FrontendURL {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden_origin"})
		return
	}
	if s := r.Header.Get("Sec-Fetch-Site"); r.Header.Get("Origin") == "" && (s == "cross-site" || s == "same-site") {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden_origin"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusBadRequest, refusal("invalid_request"))
		return
	}

	if p.Next != nil {
		next := p.Next
		p.Next = nil
		for k, v := range next.Header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(next.Status)
		_, _ = io.WriteString(w, next.Body)
		return
	}

	if cookie != Pending {
		expire(w)
		writeJSON(w, http.StatusForbidden, refusal("mfa_challenge_missing"))
		return
	}
	if !p.open {
		expire(w)
		code := "mfa_challenge_" + p.ended
		if p.ended == "" {
			code = "mfa_challenge_missing"
		}
		writeJSON(w, http.StatusForbidden, refusal(code))
		return
	}
	if time.Now().Before(p.lockedUntil) {
		p.locked(w)
		return
	}

	var answer map[string]string
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&answer); err != nil || len(answer) != 1 {
		writeJSON(w, http.StatusBadRequest, refusal("invalid_request"))
		return
	}

	if code, ok := answer["code"]; ok {
		code = strings.ReplaceAll(code, " ", "")
		if len(code) != 6 {
			writeJSON(w, http.StatusBadRequest, refusal("invalid_request"))
			return
		}
		switch {
		case code == Code && !p.codeUsed:
			p.codeUsed = true
			p.pass(w, nil)
		case code == Code:
			p.fail(w, "mfa_code_already_used")
		default:
			p.fail(w, "mfa_code_invalid")
		}
		return
	}
	if rc, ok := answer["recovery_code"]; ok {
		canonical, ok := canonicalRecovery(rc)
		if !ok {
			writeJSON(w, http.StatusBadRequest, refusal("invalid_request"))
			return
		}
		for i, have := range p.RecoveryCodes {
			if have == canonical {
				p.RecoveryCodes = append(p.RecoveryCodes[:i:i], p.RecoveryCodes[i+1:]...)
				left := len(p.RecoveryCodes)
				p.pass(w, &left)
				return
			}
		}
		p.fail(w, "mfa_recovery_code_invalid")
		return
	}
	writeJSON(w, http.StatusBadRequest, refusal("invalid_request"))
}

func (p *Platform) pass(w http.ResponseWriter, recoveryLeft *int) {
	p.open, p.ended = false, "used"
	expire(w)
	http.SetCookie(w, &http.Cookie{Name: "nan_session", Value: Session, Path: "/", Domain: ".nan.builders", Secure: true, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{"redirect": "/", "recovery_codes_remaining": recoveryLeft})
}

func (p *Platform) fail(w http.ResponseWriter, code string) {
	p.attempts--
	if p.attempts <= 0 {
		p.open, p.ended = false, "exhausted"
		expire(w)
		writeJSON(w, http.StatusForbidden, refusal("mfa_challenge_exhausted"))
		return
	}
	body := refusal(code)
	body["attempts_remaining"] = p.attempts
	writeJSON(w, http.StatusBadRequest, body)
}

func (p *Platform) locked(w http.ResponseWriter) {
	secs := int(time.Until(p.lockedUntil).Seconds()) + 1
	w.Header().Set("Retry-After", fmt.Sprint(secs))
	body := refusal("mfa_locked")
	body["locked_until"] = p.lockedUntil.UTC().Format(time.RFC3339)
	body["retry_after"] = secs
	writeJSON(w, http.StatusTooManyRequests, body)
}

// canonicalRecovery is the platform's reading of a recovery code: sixteen
// base32 characters in any case, with or without hyphens and spaces.
func canonicalRecovery(s string) (string, bool) {
	s = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != 16 {
		return "", false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return "", false
		}
	}
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16], true
}

func refusal(code string) map[string]any {
	return map[string]any{"error": code, "message": "A sentence the client must not branch on."}
}

func expire(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: PendingCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

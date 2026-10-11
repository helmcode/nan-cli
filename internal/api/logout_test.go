package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Spec (prod E2E 2026-10-12): signing out of the CLI must end the session on
// the platform, not only forget it locally. A session token copied off the
// machine before `nan auth logout` otherwise keeps working until it expires.
//
// Logout POSTs /auth/logout carrying the session as a bearer (and as the
// nan_session cookie, which is how every other call of this client presents
// it, so an older platform that only reads the cookie still revokes it).

func TestLogoutPostsTheSessionToThePlatform(t *testing.T) {
	var method, path, authz, cookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, authz = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if c, err := r.Cookie("nan_session"); err == nil {
			cookie = c.Value
		}
		_, _ = w.Write([]byte(`{"status":"logged out"}`))
	}))
	defer srv.Close()

	c := New("session-jwt").WithBaseURL(srv.URL)
	if err := c.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if method != http.MethodPost || path != "/auth/logout" {
		t.Errorf("sent %s %s, want POST /auth/logout", method, path)
	}
	if authz != "Bearer session-jwt" {
		t.Errorf("Authorization = %q, want the session as a bearer", authz)
	}
	if cookie != "session-jwt" {
		t.Errorf("nan_session cookie = %q, want the session", cookie)
	}
}

// A session the platform already refuses is already over: nothing to report.
func TestLogoutOfAnExpiredSessionIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if err := New("stale").WithBaseURL(srv.URL).Logout(); err != nil {
		t.Errorf("an already-dead session reads as a failed logout: %v", err)
	}
}

// A platform that is down did not revoke anything, and the caller has to be
// able to say so.
func TestLogoutReportsAPlatformThatDidNotAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	if err := New("t").WithBaseURL(srv.URL).Logout(); err == nil {
		t.Error("a 502 reads as a revoked session")
	}
	srv.Close()
	if err := New("t").WithBaseURL(srv.URL).Logout(); err == nil {
		t.Error("an unreachable platform reads as a revoked session")
	}
}

// No session, no request: there is nothing on the platform to end.
func TestLogoutWithoutASessionSendsNothing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()

	if err := New("").WithBaseURL(srv.URL).Logout(); err != nil {
		t.Errorf("Logout with no session: %v", err)
	}
	if calls != 0 {
		t.Errorf("sent %d requests with no session", calls)
	}
}

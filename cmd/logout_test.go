package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Spec (prod E2E 2026-10-12): `nan auth logout` ends the session on the
// platform BEFORE it deletes session.json, so the token stops working
// everywhere and not just on this machine. A platform that cannot be reached
// does not keep the member signed in locally: the file still goes, and the
// command says the session may outlive it.

func logoutServer(t *testing.T, status int) (*[]string, string) {
	t.Helper()
	seen := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The file must still be on disk when the platform is asked.
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "nan", "session.json")); err != nil {
			t.Error("session.json was deleted before the platform was asked to end the session")
		}
		*seen = append(*seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return seen, srv.URL
}

func withAPIBase(t *testing.T, u string) {
	t.Helper()
	orig := apiBaseURL
	apiBaseURL = u
	t.Cleanup(func() { apiBaseURL = orig })
}

func sessionFileExists(home string) bool {
	_, err := os.Stat(filepath.Join(home, ".config", "nan", "session.json"))
	return err == nil
}

func TestLogoutEndsTheSessionOnThePlatformFirst(t *testing.T) {
	home := fakeHome(t, `{"token":"session-jwt"}`)
	seen, u := logoutServer(t, http.StatusOK)
	withAPIBase(t, u)
	keepToolsFlag = true
	t.Cleanup(func() { keepToolsFlag = false })

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0] != "POST /auth/logout Bearer session-jwt" {
		t.Errorf("platform saw %v, want one POST /auth/logout with the session bearer", *seen)
	}
	if sessionFileExists(home) {
		t.Error("session.json survived the logout")
	}
}

func TestLogoutStillSignsOutLocallyWhenThePlatformFails(t *testing.T) {
	home := fakeHome(t, `{"token":"session-jwt"}`)
	_, u := logoutServer(t, http.StatusBadGateway)
	withAPIBase(t, u)
	keepToolsFlag = true
	t.Cleanup(func() { keepToolsFlag = false })

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	if sessionFileExists(home) {
		t.Error("a platform failure kept the member signed in locally")
	}
}

// A session that only carries a platform token or an API key has no platform
// session to end: nothing is sent.
func TestLogoutWithoutASessionTokenSendsNothing(t *testing.T) {
	home := fakeHome(t, `{"platformToken":"`+testPAT+`"}`)
	seen, u := logoutServer(t, http.StatusOK)
	withAPIBase(t, u)
	keepToolsFlag = true
	t.Cleanup(func() { keepToolsFlag = false })

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	if len(*seen) != 0 {
		t.Errorf("platform saw %v, want nothing", *seen)
	}
	if sessionFileExists(home) {
		t.Error("session.json survived the logout")
	}
}

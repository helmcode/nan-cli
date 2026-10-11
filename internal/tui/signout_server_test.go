package tui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/session"
)

// fakePlatform stands in for cloud-api so a sign-out in a test never reaches
// the real one, and records what it was asked.
type fakePlatform struct {
	url  string
	mu   sync.Mutex
	seen []string
	// sessionOnDisk records, per request, whether session.json still existed.
	sessionOnDisk []bool
}

func newFakePlatform(t *testing.T, status int) *fakePlatform {
	t.Helper()
	fp := &fakePlatform{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := session.Load()
		fp.mu.Lock()
		fp.seen = append(fp.seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		fp.sessionOnDisk = append(fp.sessionOnDisk, err == nil)
		fp.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	fp.url = srv.URL
	return fp
}

// Spec (prod E2E 2026-10-12): signing out from the panel ends the session on
// the platform before session.json goes, and a platform that does not answer
// does not keep the member signed in on this machine.
func TestSigningOutEndsTheSessionOnThePlatform(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("HERMES_HOME", filepath.Join(home, "h"))

			sess := &session.Session{Token: "session-jwt", APIKey: testKey}
			if err := session.Save(sess); err != nil {
				t.Fatal(err)
			}
			fp := newFakePlatform(t, status)
			m := newModel(api.New("session-jwt").WithBaseURL(fp.url), sess)
			m.lay = newLayout(90, 30)

			press := func(m model, r rune) model {
				updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				return updated.(model)
			}
			if after := press(m, 'o'); len(fp.seen) != 0 {
				t.Fatalf("one press already ended the session (%v); confirm=%v", fp.seen, after.confirmSignOut)
			}
			out := press(press(m, 'o'), 'o')

			if len(fp.seen) != 1 || fp.seen[0] != "POST /auth/logout Bearer session-jwt" {
				t.Fatalf("platform saw %v, want one POST /auth/logout with the session bearer", fp.seen)
			}
			if !fp.sessionOnDisk[0] {
				t.Error("session.json was deleted before the platform was asked")
			}
			if out.sess.Token != "" {
				t.Error("the panel kept the session")
			}
			if _, err := session.Load(); err == nil {
				t.Error("session.json is still on disk")
			}
		})
	}
}

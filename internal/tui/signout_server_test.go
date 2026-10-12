package tui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func pressKey(m model, r rune) (model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return updated.(model), cmd
}

// signOut presses `o` twice and lets the sign-out command run to completion,
// feeding its answer back the way the bubbletea runtime would.
func signOut(t *testing.T, m model) model {
	t.Helper()
	m, _ = pressKey(m, 'o')
	m, cmd := pressKey(m, 'o')
	if cmd == nil {
		t.Fatal("the second press started no sign-out")
	}
	msg, ok := cmd().(signedOutMsg)
	if !ok {
		t.Fatal("the sign-out command did not answer with signedOutMsg")
	}
	updated, _ := m.Update(msg)
	return updated.(model)
}

func signedInPanel(t *testing.T, platformURL string) model {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "h"))
	sess := &session.Session{Token: "session-jwt", APIKey: testKey}
	if err := session.Save(sess); err != nil {
		t.Fatal(err)
	}
	m := newModel(api.New("session-jwt").WithBaseURL(platformURL), sess)
	m.lay = newLayout(90, 30)
	return m
}

// Spec (prod E2E 2026-10-12): signing out from the panel ends the session on
// the platform before session.json goes, and a platform that does not answer
// does not keep the member signed in on this machine; the member is told the
// session may outlive the sign-out.
func TestSigningOutEndsTheSessionOnThePlatform(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fp := newFakePlatform(t, status)
			m := signedInPanel(t, fp.url)

			if _, cmd := pressKey(m, 'o'); cmd != nil || len(fp.seen) != 0 {
				t.Fatalf("one press already started a sign-out (%v)", fp.seen)
			}
			out := signOut(t, m)

			if len(fp.seen) != 1 || fp.seen[0] != "POST /auth/logout Bearer session-jwt" {
				t.Fatalf("platform saw %v, want one POST /auth/logout with the session bearer", fp.seen)
			}
			if !fp.sessionOnDisk[0] {
				t.Error("session.json was deleted before the platform was asked")
			}
			if out.sess.Token != "" || out.signingOut {
				t.Error("the panel kept the session")
			}
			if _, err := session.Load(); err == nil {
				t.Error("session.json is still on disk")
			}
			warned := strings.Contains(out.setupMsg, "did not confirm")
			if status == http.StatusOK && warned {
				t.Errorf("a confirmed sign-out warns: %q", out.setupMsg)
			}
			if status != http.StatusOK && !warned {
				t.Errorf("an unconfirmed sign-out does not say so: %q", out.setupMsg)
			}
		})
	}
}

// The platform is asked from a command, not from Update: a platform that
// accepts the connection and never answers must not freeze the panel. Until
// it answers, the member stays signed in here, the panel says it is signing
// out, and another `o` does not ask again.
func TestSigningOutDoesNotFreezeThePanelOnAHangingPlatform(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	m := signedInPanel(t, srv.URL)

	m, _ = pressKey(m, 'o')
	done := make(chan struct{})
	var cmd tea.Cmd
	go func() {
		m, cmd = pressKey(m, 'o')
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the second press blocked on the platform")
	}
	if cmd == nil || !m.signingOut || !strings.Contains(m.setupMsg, "signing out") {
		t.Fatalf("no sign-out in flight: cmd=%v signingOut=%v msg=%q", cmd != nil, m.signingOut, m.setupMsg)
	}
	if _, err := session.Load(); err != nil {
		t.Error("session.json went before the platform answered")
	}
	m, _ = pressKey(m, 'o')
	if _, again := pressKey(m, 'o'); again != nil {
		t.Error("another o while signing out asks the platform again")
	}
}

// A stray answer (no sign-out in flight) changes nothing.
func TestAStraySignedOutMsgIsIgnored(t *testing.T) {
	fp := newFakePlatform(t, http.StatusOK)
	m := signedInPanel(t, fp.url)
	updated, _ := m.Update(signedOutMsg{})
	if updated.(model).sess.Token == "" {
		t.Error("a signedOutMsg nobody asked for signed the member out")
	}
}

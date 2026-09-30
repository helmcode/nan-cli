package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/session"
)

// A session the platform stopped honouring left its token in session.json, so
// the panel went on believing it: Home said the member was signed in, `s` did
// nothing because there was a session, and every other tab said `invalid
// session`. The one key that fixes it was the one switched off.
func TestAnExpiredSessionCanBeSignedBackInto(t *testing.T) {
	sess := &session.Session{Token: "stale", APIKey: "sk-still-good"}
	m := setupModel(t, sess)
	m.client = api.New("stale")
	m.lay = newLayout(100, 40)
	m.active = tabIndex(tabProfile)

	mod, _ := m.Update(sessionExpiredMsg{token: "stale"})
	m = mod.(model)

	if m.sess.Token != "" {
		t.Error("the panel still holds the session the platform refused")
	}
	if m.sess.APIKey != "sk-still-good" {
		t.Error("the API key went with the session, and it had not expired")
	}
	if m.client.Token() != "" {
		t.Error("the client still sends the refused session")
	}
	if !strings.Contains(m.View(), "press s") {
		t.Errorf("the tab does not say what to do:\n%s", m.View())
	}

	// On disk too, or the next start is the same panel again.
	saved, err := session.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "" || saved.APIKey != "sk-still-good" {
		t.Errorf("session.json kept %+v", saved)
	}

	// Back to Home, which has to offer the sign-in again - and not the error
	// the tab it came from was showing.
	mod, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = mod.(model)
	if m.activeID() != tabHome {
		t.Fatalf("shift+tab from Profile lands on %v", m.activeID())
	}
	home := m.View()
	if strings.Contains(home, errSessionExpired.Error()) {
		t.Errorf("Home is drawing the error from the tab before it:\n%s", home)
	}
	if !strings.Contains(home, "sign in") {
		t.Errorf("Home does not offer the sign-in:\n%s", home)
	}

	mod, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if mod.(model).loginStage != loginAskEmail {
		t.Error("s still does nothing after the session expired")
	}
}

// A refusal of the old session that arrives after the member has signed in
// again is about a session that is already gone, and must not take the new
// one with it.
func TestALateRefusalDoesNotSignOutTheNewSession(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "fresh"})
	m.client = api.New("fresh")

	mod, _ := m.Update(sessionExpiredMsg{token: "stale"})
	if got := mod.(model).sess.Token; got != "fresh" {
		t.Errorf("the new session was dropped for the old one's refusal (token %q)", got)
	}
}

// Opening the panel onto an expired session is the same moment as opening it
// with none, so it goes straight to the sign-in the way a fresh machine does.
func TestOpeningOnAnExpiredSessionAsksToSignIn(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "stale", APIKey: "sk"})
	m.client = api.New("stale")

	mod, cmd := m.Update(firstRunMsg{})
	m = mod.(model)
	if cmd == nil {
		t.Fatal("the panel opens on a session without asking whether it still works")
	}
	if m.loginStage != loginOff {
		t.Fatal("the panel asks for a sign-in before it knows the session is gone")
	}

	mod, _ = m.Update(sessionExpiredMsg{token: "stale", atStart: true})
	if mod.(model).loginStage != loginAskEmail {
		t.Error("opening on an expired session does not start the sign-in")
	}
}

// The same answer later on, while someone is reading a tab, says so and waits:
// the sign-in takes every key while it is up.
func TestASessionThatExpiresLaterDoesNotGrabTheKeyboard(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "stale"})
	m.client = api.New("stale")
	m.active = tabIndex(tabUsage)

	mod, _ := m.Update(sessionExpiredMsg{token: "stale"})
	if mod.(model).loginStage != loginOff {
		t.Error("an expiry mid-session threw the member into the sign-in")
	}
}

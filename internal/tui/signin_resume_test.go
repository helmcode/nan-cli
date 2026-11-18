package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/auth"
	"github.com/nxssie/nan-cli/internal/session"
)

// ── signing in from Setup, with a key already saved (#81) ────────────────────

// signedOutWithKey is a machine with an API key saved on disk and no session,
// on the tab it is opened at. The key is written to session.json because the
// approval reloads the session from there.
func signedOutWithKey(t *testing.T, key string, tab tabID) (model, string) {
	t.Helper()
	home := toolHome(t)
	sess := &session.Session{APIKey: key}
	if key != "" {
		if err := session.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(api.New(""), sess)
	m.lay = newLayout(90, 30)
	m.active = tabIndex(tab)
	return m, home
}

// approve answers the sign-in in flight as approved, the way waitApproval
// would. The returned command is never run: it would reach the platform.
func approve(t *testing.T, m model) (model, tea.Cmd) {
	t.Helper()
	if m.loginStage == loginOff {
		t.Fatal("no sign-in in flight")
	}
	m.loginStage = loginWaiting
	updated, cmd := m.Update(deviceApprovedMsg{attempt: m.loginAttempt, token: "a-fresh-session-token"})
	return updated.(model), cmd
}

// toolFiles lists what was written under home other than session.json.
func toolFiles(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	for _, f := range filesUnder(t, home) {
		if f != session.Path() {
			out = append(out, f)
		}
	}
	return out
}

// The member already pressed c: once signed in, the configure they asked for
// runs on the tools step, with no key step and no key check in between.
func TestSigningInFromConfigureResumesItOnTheToolsStep(t *testing.T) {
	m, _ := signedOutWithKey(t, testKey, tabSetup)

	m, _ = pressKey(m, 'c')
	if m.loginStage == loginOff {
		t.Fatal("c with no session did not open the sign-in")
	}
	m, cmd := approve(t, m)

	if m.wizard != wizardTools {
		t.Errorf("wizard = %v, want the tools step", m.wizard)
	}
	if !m.configuring || cmd == nil {
		t.Error("the configure the member asked for did not resume after signing in")
	}
	if m.editingKey || m.keyCheck == keyChecking {
		t.Error("a saved key was asked for, or checked, again")
	}
	if m.sess.Token != "a-fresh-session-token" || m.sess.APIKey != testKey {
		t.Errorf("session after sign-in = token %q key %q", m.sess.Token, m.sess.APIKey)
	}
	if m.configureAfterLogin {
		t.Error("the request to configure outlived the sign-in it was waiting for")
	}
}

// A sign-in nobody started with c lands on the tools step too, but writing
// into the member's tools is still theirs to ask for.
func TestSigningInFromElsewhereLandsOnTheToolsStepWithoutConfiguring(t *testing.T) {
	for name, start := range map[string]func(m model) model{
		"s on Home": func(m model) model {
			m, _ = pressKey(m, 's')
			return m
		},
		"first run": func(m model) model {
			updated, _ := m.Update(firstRunMsg{})
			return updated.(model)
		},
		"c, cancelled, then s": func(m model) model {
			m.active = tabIndex(tabSetup)
			m, _ = pressKey(m, 'c')
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = updated.(model)
			m.active = tabIndex(tabHome)
			m, _ = pressKey(m, 's')
			return m
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, home := signedOutWithKey(t, testKey, tabHome)
			m = start(m)
			m, _ = approve(t, m)

			if m.wizard != wizardTools {
				t.Errorf("wizard = %v, want the tools step", m.wizard)
			}
			if m.configuring {
				t.Error("configured the tools without being asked")
			}
			if m.editingKey {
				t.Error("a saved key was asked for again")
			}
			if files := toolFiles(t, home); len(files) != 0 {
				t.Errorf("wrote %v", files)
			}
		})
	}
}

// With no key saved, or one the cluster already refused, the key step comes
// first, as before - and nothing is configured with a key that is not there.
func TestSigningInFromConfigureWithoutAUsableKeyAsksForTheKey(t *testing.T) {
	for name, tc := range map[string]struct {
		key      string
		keyCheck string
	}{
		"no key saved": {"", ""},
		"key refused":  {testKey, "error: the cluster refused this key — unauthorized"},
	} {
		t.Run(name, func(t *testing.T) {
			m, home := signedOutWithKey(t, tc.key, tabSetup)
			m.keyCheck = tc.keyCheck

			m, _ = pressKey(m, 'c')
			m, _ = approve(t, m)

			if m.wizard != wizardKey {
				t.Errorf("wizard = %v, want the key step", m.wizard)
			}
			if m.configuring {
				t.Error("configure ran without a usable key")
			}
			if m.configureAfterLogin {
				t.Error("the request to configure is still pending")
			}
			if files := toolFiles(t, home); len(files) != 0 {
				t.Errorf("wrote %v", files)
			}
		})
	}
}

// A sign-in from c that fails or is cancelled writes nothing: no session, no
// tool, and no configure left waiting to fire later.
func TestASignInFromConfigureThatDoesNotSucceedWritesNothing(t *testing.T) {
	for name, end := range map[string]func(m model) model{
		"denied": func(m model) model {
			m.loginStage = loginWaiting
			updated, _ := m.Update(deviceApprovedMsg{attempt: m.loginAttempt, err: auth.ErrDenied})
			return updated.(model)
		},
		"start error": func(m model) model {
			updated, _ := m.Update(deviceStartedMsg{attempt: m.loginAttempt, err: errors.New("could not start")})
			return updated.(model)
		},
		"cancelled, then a late approval": func(m model) model {
			m.loginStage = loginWaiting
			late := m.loginAttempt
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			updated, _ = updated.(model).Update(deviceApprovedMsg{attempt: late, token: "late-token"})
			return updated.(model)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, home := signedOutWithKey(t, testKey, tabSetup)
			m, _ = pressKey(m, 'c')
			m = end(m)

			if m.configuring {
				t.Error("configure ran after a sign-in that did not succeed")
			}
			if m.sess.Token != "" {
				t.Errorf("session token = %q", m.sess.Token)
			}
			if saved, err := session.Load(); err != nil || saved.Token != "" {
				t.Errorf("session.json after a failed sign-in: %+v, %v", saved, err)
			}
			if files := toolFiles(t, home); len(files) != 0 {
				t.Errorf("wrote %v", files)
			}
		})
	}
}

// Trying the sign-in again after a failure is the same request: the member
// still asked to configure.
func TestRetryingTheSignInFromConfigureStillConfigures(t *testing.T) {
	m, _ := signedOutWithKey(t, testKey, tabSetup)
	m, _ = pressKey(m, 'c')
	m.loginStage = loginWaiting
	updated, _ := m.Update(deviceApprovedMsg{attempt: m.loginAttempt, err: auth.ErrExpired})
	updated, _ = updated.(model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.loginStage != loginStarting {
		t.Fatalf("stage = %v, want a new sign-in", m.loginStage)
	}

	m, _ = approve(t, m)
	if m.wizard != wizardTools || !m.configuring {
		t.Errorf("after a retried sign-in: wizard %v configuring %v", m.wizard, m.configuring)
	}
}

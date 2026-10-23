package tui

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nxssie/nan-cli/internal/auth"
	"github.com/nxssie/nan-cli/internal/auth/authtest"
	"github.com/nxssie/nan-cli/internal/session"
)

// Two-factor sign-in from the panel: the same flow as `nan auth login`,
// through internal/auth, with the code asked for as a third question.

const mfaTestLink = "https://cloud-api.nan.builders/api/auth/login/verify?token=" + authtest.LinkToken

// atTheCode drives the panel up to the code question against a fake platform
// whose account has a factor: the address, the link, and the exchange that
// comes back with a challenge.
func atTheCode(t *testing.T) (model, *authtest.Platform) {
	t.Helper()
	p := authtest.New(t)
	p.Factor = true
	old := auth.SignInBaseURL
	auth.SignInBaseURL = p.URL
	t.Cleanup(func() { auth.SignInBaseURL = old })

	m := setupModel(t, &session.Session{})
	m.lay = newLayout(100, 40)
	m.startLogin()
	m.loginEmail = "member@example.com"
	mod, _ := m.Update(linkSentMsg{})
	m = mod.(model)

	msg := signIn(mfaTestLink)()
	if _, ok := msg.(mfaChallengeMsg); !ok {
		t.Fatalf("the link came back as %#v, not a challenge", msg)
	}
	mod, _ = m.Update(msg)
	return mod.(model), p
}

// enter types value into the field and presses enter, running what that
// starts and handing its answer back to the panel.
func enter(t *testing.T, m model, value string) model {
	t.Helper()
	m.loginInput.SetValue(value)
	mod, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter started nothing")
	}
	mod, _ = mod.(model).Update(cmd())
	return mod.(model)
}

func TestThePanelAsksForTheCode(t *testing.T) {
	m, _ := atTheCode(t)
	if m.loginStage != loginAskCode || m.wizard != wizardLink {
		t.Fatalf("stage %v, wizard %v", m.loginStage, m.wizard)
	}
	if m.loginInput.EchoMode == 0 {
		t.Error("the code field echoes: a recovery code works until it is used")
	}
	flat := strings.Join(strings.Fields(m.View()), " ")
	for _, want := range []string{"Authenticator code", "two-factor", "recovery codes"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the screen does not say %q:\n%s", want, m.View())
		}
	}

	m = enter(t, m, authtest.Code)
	if m.loginStage != loginOff || m.loginChallenge != nil {
		t.Errorf("still signing in after the right code: stage %v", m.loginStage)
	}
	sess, err := session.Load()
	if err != nil || sess.Token != authtest.Session {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	if m.client.Token() != authtest.Session {
		t.Error("the panel's client does not carry the new session")
	}
	assertNoChallengeUnderHome(t)
}

// A wrong code keeps the same question, empty, with what went wrong and how
// many tries are left.
func TestThePanelAsksAgainAfterAWrongCode(t *testing.T) {
	m, p := atTheCode(t)
	m = enter(t, m, "000000")
	if m.loginStage != loginAskCode {
		t.Fatalf("a wrong code left the question: stage %v", m.loginStage)
	}
	if m.loginInput.Value() != "" {
		t.Error("the wrong code is still in the field")
	}
	if !strings.Contains(m.loginMsg, "4 attempts left") {
		t.Errorf("loginMsg = %q", m.loginMsg)
	}
	m = enter(t, m, authtest.Code)
	if m.loginStage != loginOff {
		t.Errorf("the right code after a wrong one did not sign in: stage %v", m.loginStage)
	}
	if n := len(p.Challenges()); n != 2 {
		t.Errorf("%d challenge POSTs", n)
	}
}

// A sign-in that cannot go on - timed out, too many wrong codes, refused -
// spent its link, so the panel goes back to the address, filled in: enter
// asks for a new one.
func TestThePanelStartsAgainWhenTheSignInEnds(t *testing.T) {
	m, p := atTheCode(t)
	p.Expire()
	m = enter(t, m, authtest.Code)
	if m.loginStage != loginAskEmail || m.wizard != wizardEmail {
		t.Fatalf("stage %v, wizard %v", m.loginStage, m.wizard)
	}
	if m.loginChallenge != nil {
		t.Error("the panel keeps a challenge that is over")
	}
	if m.loginInput.Value() != "member@example.com" || m.loginInput.Prompt != "Email: " {
		t.Errorf("field = %q %q", m.loginInput.Prompt, m.loginInput.Value())
	}
	if !strings.HasPrefix(m.loginMsg, "error: ") || !strings.Contains(m.loginMsg, "new link") {
		t.Errorf("loginMsg = %q", m.loginMsg)
	}
	if s, err := session.Load(); err == nil && s.Token != "" {
		t.Errorf("saved a session: %+v", s)
	}
}

func TestThePanelSaysHowManyRecoveryCodesAreLeft(t *testing.T) {
	m, p := atTheCode(t)
	p.RecoveryCodes = []string{"q7wa-z2xd-pl4r-6wze", "aaaa-bbbb-cccc-dddd"}
	m = enter(t, m, "Q7WA-Z2XD-PL4R-6WZE")
	if m.loginStage != loginOff || m.wizard != wizardKey {
		t.Fatalf("stage %v, wizard %v", m.loginStage, m.wizard)
	}
	flat := strings.Join(strings.Fields(m.View()), " ")
	if !strings.Contains(flat, "1 left") || !strings.Contains(flat, "Two-factor authentication") {
		t.Errorf("the next step does not say how many are left:\n%s", m.View())
	}
}

// Esc leaves the code question and forgets the challenge; an exchange that
// answers after the member left does not bring the question back.
func TestEscapeForgetsTheChallenge(t *testing.T) {
	m, _ := atTheCode(t)
	mod, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mod.(model)
	if m.loginStage != loginOff || m.loginChallenge != nil {
		t.Errorf("stage %v, challenge kept: %v", m.loginStage, m.loginChallenge != nil)
	}

	m2, _ := atTheCode(t)
	challenge := m2.loginChallenge
	m2.cancelLogin()
	mod, _ = m2.Update(mfaChallengeMsg{challenge})
	if got := mod.(model); got.loginStage != loginOff || got.loginChallenge != nil {
		t.Errorf("a late challenge reopened the sign-in: stage %v", got.loginStage)
	}
}

// The screen never shows the challenge cookie, and nothing under HOME holds
// it.
func TestThePanelKeepsTheChallengeToItself(t *testing.T) {
	m, _ := atTheCode(t)
	m = enter(t, m, "000000")
	if strings.Contains(m.View(), authtest.Pending) || strings.Contains(m.loginMsg, authtest.Pending) {
		t.Error("the challenge cookie is on screen")
	}
	assertNoChallengeUnderHome(t)
}

func assertNoChallengeUnderHome(t *testing.T) {
	t.Helper()
	err := filepath.WalkDir(os.Getenv("HOME"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(authtest.Pending)) {
			t.Errorf("%s holds the challenge cookie", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

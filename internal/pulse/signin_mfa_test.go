package pulse

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/api/authtest"
	"github.com/helmcode/nan-cli/internal/session"
)

// typeInto sends text to the dashboard one key at a time. What a keystroke
// returns is the cursor's blink, a timer half a second out, so nothing is
// settled until the key that submits.
func typeInto(t *testing.T, m model, text string) model {
	t.Helper()
	for _, r := range text {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(model)
	}
	return m
}

// signInTo drives the dashboard's sign-in against a fake platform whose
// account has a factor, up to the code question.
func signInTo(t *testing.T, p *authtest.Platform) model {
	t.Helper()
	sandbox(t)
	t.Setenv("NAN_API_URL", p.URL)
	sess := &session.Session{}
	m := newModel(context.Background(), api.New(""), sess)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	m = next.(model)
	m.reveal, m.animating = 1, false
	m = settle(t, m, m.opening())
	if !m.signIn.Active() {
		t.Fatal("a fresh machine was not asked to sign in")
	}
	m = typeInto(t, m, "me@example.com")
	m = press(t, m, keyOf("enter"))
	m = typeInto(t, m, authtest.LinkToken)
	m = press(t, m, keyOf("enter"))
	if out := screen(m); !strings.Contains(out, "Authenticator code") {
		t.Fatalf("the dashboard does not ask for the code:\n%s", out)
	}
	return m
}

func TestTheDashboardSignsInWithTheCode(t *testing.T) {
	p := authtest.New(t)
	p.Factor = true
	m := signInTo(t, p)

	m = typeInto(t, m, authtest.Code)
	if strings.Contains(screen(m), authtest.Code) {
		t.Errorf("the code is echoed:\n%s", screen(m))
	}
	m = press(t, m, keyOf("enter"))
	if !m.session.SignedIn() || m.session.Token != authtest.Session || m.signIn.Active() {
		t.Fatalf("after the right code: token %q, sign-in open %v", m.session.Token, m.signIn.Active())
	}
	if m.client.Token() != authtest.Session {
		t.Error("the dashboard's client does not carry the new session")
	}
	saved, err := session.Load()
	if err != nil || saved.Token != authtest.Session {
		t.Errorf("session.json = %+v, %v", saved, err)
	}
}

func TestTheDashboardAsksAgainAfterAWrongCodeAndSaysWhenARecoveryCodeWasSpent(t *testing.T) {
	p := authtest.New(t)
	p.Factor = true
	p.RecoveryCodes = []string{"q7wa-z2xd-pl4r-6wze", "aaaa-bbbb-cccc-dddd"}
	m := signInTo(t, p)

	m = typeInto(t, m, "000000")
	m = press(t, m, keyOf("enter"))
	if !m.signIn.Active() || !strings.Contains(strings.Join(strings.Fields(screen(m)), " "), "4 attempts left") {
		t.Fatalf("a wrong code did not ask again:\n%s", screen(m))
	}

	m = typeInto(t, m, "Q7WA-Z2XD-PL4R-6WZE")
	m = press(t, m, keyOf("enter"))
	if !m.session.SignedIn() {
		t.Fatalf("a recovery code did not sign in:\n%s", screen(m))
	}
	out := strings.Join(strings.Fields(screen(m)), " ")
	if !strings.Contains(out, "1 left") || !strings.Contains(out, "cloud.nan.builders, Settings") {
		t.Errorf("the next step does not say how many recovery codes are left:\n%s", screen(m))
	}
	if strings.Count(out, "1 left") != 1 {
		t.Errorf("the recovery-code notice is on screen more than once:\n%s", screen(m))
	}
}

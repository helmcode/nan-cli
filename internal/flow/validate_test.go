package flow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nxssie/nan-cli/internal/api"
)

// fakeCluster is the inference API's model list, which is what decides
// whether a key works.
func fakeCluster(t *testing.T, good string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"glm5.3"},{"id":"glm5.3-flash"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("NAN_INFERENCE_URL", srv.URL+"/v1")
}

func checked(t *testing.T, s Setup, key string) Setup {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no tools found, so the flow finishes
	msg, ok := checkKey(context.Background(), key)().(keyCheckedMsg)
	if !ok {
		t.Fatal("checkKey did not return a keyCheckedMsg")
	}
	s.key = key
	s, _ = s.Update(context.Background(), msg, nil)
	return s
}

func TestAKeyTheClusterAcceptsGoesThrough(t *testing.T) {
	fakeCluster(t, "sk-good")
	var s Setup
	s.Start("", "")
	s = checked(t, s, "sk-good")
	if s.err != nil || s.Key != "sk-good" || s.models != 2 {
		t.Fatalf("err=%v key=%q models=%d", s.err, s.Key, s.models)
	}
}

// A refused key goes back to an empty field under the error: writing it
// anywhere would only move the failure into every tool.
func TestARefusedKeyOpensTheFieldAgain(t *testing.T) {
	fakeCluster(t, "sk-good")
	var s Setup
	s.Start("", "")
	s = checked(t, s, "sk-wrong")
	if s.stage != setupKey || s.Key != "" {
		t.Fatalf("stage=%d key=%q, want back on the key with nothing to save", s.stage, s.Key)
	}
	if !strings.Contains(s.View("", ""), "the cluster refused this key") {
		t.Errorf("the refusal is not said:\n%s", s.View("", ""))
	}
}

// A key that could not be checked because the cluster could not be reached
// is let through, and the summary says it was not checked.
func TestAKeyThatCannotBeCheckedIsSavedAndSaidSo(t *testing.T) {
	t.Setenv("NAN_INFERENCE_URL", "http://127.0.0.1:1/v1")
	var s Setup
	s.Start("", "")
	s = checked(t, s, "sk-offline")
	if s.Key != "sk-offline" || s.unchecked == nil {
		t.Fatalf("key=%q unchecked=%v", s.Key, s.unchecked)
	}
	if !strings.Contains(prose(s.View("", "")), "could not be checked") {
		t.Errorf("the summary does not say the key was not checked:\n%s", s.View("", ""))
	}
}

// What the key step says about the member's key on the platform. The
// platform never hands the key itself back, only whether there is one.
func TestTheKeyHintSaysWhereTheKeyIs(t *testing.T) {
	cases := []struct {
		status *api.KeyStatus
		want   string
	}{
		{&api.KeyStatus{Exists: true, Alias: "an-alias"}, "has a key (an-alias) - copy it from cloud.nan.builders"},
		{&api.KeyStatus{Exists: true, Name: "sk-name"}, "has a key (sk-name)"},
		{&api.KeyStatus{}, "no key yet - create one at cloud.nan.builders"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := KeyHint(c.status); !strings.Contains(got, c.want) || (c.want == "" && got != "") {
			t.Errorf("KeyHint(%+v) = %q, want %q", c.status, got, c.want)
		}
	}
	var s Setup
	s.Start("", KeyHint(&api.KeyStatus{}))
	if !strings.Contains(prose(s.View("", "")), "create one at cloud.nan.builders") {
		t.Error("the key step does not show the hint")
	}
}

// The key never appears on screen, typed or seeded.
func TestTheKeyIsNeverShown(t *testing.T) {
	var s Setup
	s.Start("sk-secret-on-screen", "")
	if strings.Contains(s.View("", ""), "sk-secret-on-screen") {
		t.Error("the saved key is on screen")
	}
}

// A machine with only a platform token is told why the dashboard still
// asks for an email.
func TestTheSignInExplainsAPlatformToken(t *testing.T) {
	s := NewSignIn()
	s.Start(TokenOnlyNotice)
	if !strings.Contains(prose(s.View("", "", "")), "works for nan run and nan runs only") {
		t.Errorf("the notice is not said:\n%s", s.View("", "", ""))
	}
}

// The first-run screen says why it is asking, and is skippable.
func TestOnboardingExplainsItselfAndCanBeSkipped(t *testing.T) {
	var s Setup
	s.Onboard("", Guide{Keys: [][2]string{{"tab", "into the pane"}}})
	// Why the key matters, that it can be skipped, and where it goes later.
	for _, want := range []string{"One more step", "set up the coding tools", "esc skips it", "6 Harnesses", "nan setup"} {
		if !strings.Contains(prose(s.View("", "")), want) {
			t.Errorf("the onboarding screen does not say %q:\n%s", want, s.View("", ""))
		}
	}

	// esc skips the key but still ends on how to get around: somebody who
	// skipped is exactly who is about to be left alone with the dashboard.
	s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: tea.KeyEscape}, nil)
	if !s.Active() {
		t.Fatal("esc on the first run closed it before showing the tour")
	}
	view := prose(s.View("", ""))
	for _, want := range []string{"Skipped: no key on this machine yet", "cannot reach NaN", "6 Harnesses", "tab into the pane", "any key to start"} {
		if !strings.Contains(view, want) {
			t.Errorf("the skipped first run does not say %q:\n%s", want, view)
		}
	}
	if s.Key != "" {
		t.Error("skipping left a key to save")
	}

	s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: 'x', Text: "x"}, nil)
	if s.Active() {
		t.Error("a key on the tour did not close it")
	}
}

// Outside a first run, esc is simply out: no tour for somebody who pressed e
// in a dashboard they already know.
func TestEscFromARerunLeavesStraightAway(t *testing.T) {
	var s Setup
	s.Start("", "")
	s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: tea.KeyEscape}, nil)
	if s.Active() {
		t.Error("esc from a re-run of Setup did not leave")
	}
}

// prose is a screen as text: the text wraps to the terminal, so a phrase can
// straddle a line break and the field's border.
func prose(view string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(view), "┃", "")), " ")
}

// From the link step, esc goes back to the address - filled in - rather
// than out of the sign-in. A mistyped address is the likeliest reason a
// link never arrives, and nothing else can tell you it was mistyped.
func TestEscFromTheLinkStepGoesBackToTheAddress(t *testing.T) {
	client := api.New("")
	s := NewSignIn()
	s.Start("")
	s = drive(t, s, client, typed("dev@acme.com")...)
	s = drive(t, s, client, linkSentMsg{})
	if s.stage != signInLink {
		t.Fatalf("not at the link step: %d", s.stage)
	}

	s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: tea.KeyEscape}, client)
	if s.stage != signInEmail {
		t.Fatalf("esc from the link step left the flow at stage %d, want the email step", s.stage)
	}
	if !strings.Contains(s.form.View(), "dev@acme.com") {
		t.Error("the address was not filled back in")
	}

	// From the email step, esc still leaves.
	s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: tea.KeyEscape}, client)
	if s.Active() {
		t.Error("esc from the email step did not leave the sign-in")
	}
}

// The summary says "Key saved", and the key has to still be there when it
// is dismissed: the dashboard saves once the flow closes, which is on that
// keypress. Dismissing used to hand back an empty flow and the key was lost.
func TestDismissingTheSummaryHandsBackTheKey(t *testing.T) {
	s := Setup{stage: setupDone, Key: "sk-2b8d0000aaaa441e", Tools: map[string]bool{"Pi": true}}
	s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: 'x', Text: "x"}, nil)
	if s.Active() {
		t.Fatal("a key on the summary did not close it")
	}
	if s.Key != "sk-2b8d0000aaaa441e" || !s.Tools["Pi"] {
		t.Errorf("closing the summary lost what to save: key=%q tools=%v", s.Key, s.Tools)
	}
}

// A tour on offer is a question with two answers: enter takes it, s skips
// it, and a stray key is neither. The shortcuts are shown either way.
func TestTheTourOfferTakesOnlyItsTwoAnswers(t *testing.T) {
	offer := func() Setup {
		var s Setup
		s.Onboard("", Guide{Keys: [][2]string{{"tab", "next area"}}, Tour: true})
		s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: tea.KeyEscape}, nil)
		return s
	}
	s := offer()
	view := prose(s.View("", ""))
	for _, want := range []string{"tab next area", "enter take the tour", "s skip it"} {
		if !strings.Contains(view, want) {
			t.Errorf("the offer does not say %q:\n%s", want, view)
		}
	}
	if s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: 'x', Text: "x"}, nil); !s.Active() {
		t.Error("a stray key answered the offer")
	}
	if s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: tea.KeyEnter}, nil); s.Active() || !s.TourRequested {
		t.Errorf("enter did not take the tour: active=%v requested=%v", s.Active(), s.TourRequested)
	}
	s = offer()
	if s, _ = s.Update(context.Background(), tea.KeyPressMsg{Code: 's', Text: "s"}, nil); s.Active() || s.TourRequested {
		t.Errorf("s did not skip the tour: active=%v requested=%v", s.Active(), s.TourRequested)
	}
}

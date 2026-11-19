package flow

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/api/authtest"
)

// Two-factor sign-in in the dashboard: the same flow as `nan auth login`,
// through internal/api, with the code asked for as a third question.

// A bare token is a link too (TokenFromLink), and a short one keeps the typing
// the tests drive short.
const mfaTestLink = authtest.LinkToken

// atTheCode drives the flow up to the code question against a fake platform
// whose account has a factor: the address, the link, and the exchange that
// comes back with a challenge.
func atTheCode(t *testing.T) (SignIn, *api.Client, *authtest.Platform) {
	t.Helper()
	p := authtest.New(t)
	p.Factor = true
	client := api.New("", api.WithBaseURL(p.URL))

	s := NewSignIn()
	s.Start("")
	s = drive(t, s, client, typed("me@acme.io")...)
	s = drive(t, s, client, linkSentMsg{})
	s, verifying := driveToNetwork(t, s, client, typed(mfaTestLink)...)
	if s.stage != signInVerifying {
		t.Fatalf("stage %d after pasting the link, want signInVerifying", s.stage)
	}
	msg := verifying()
	if _, ok := msg.(challengeMsg); !ok {
		t.Fatalf("the link came back as %#v, not a challenge", msg)
	}
	s = drive(t, s, client, msg)
	if s.stage != signInCode {
		t.Fatalf("stage %d after the challenge, want signInCode", s.stage)
	}
	return s, client, p
}

// enter types value into the code field and presses enter, running what that
// sends and handing the answer back to the flow.
func enter(t *testing.T, s SignIn, client *api.Client, value string) SignIn {
	t.Helper()
	s, checking := driveToNetwork(t, s, client, typed(value)...)
	if s.stage != signInChecking || checking == nil {
		t.Fatalf("enter left the flow at stage %d, want signInChecking", s.stage)
	}
	return drive(t, s, client, checking())
}

func view(s SignIn) string { return ansi.Strip(s.View("  ", "...", "")) }

func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestTheFlowAsksForTheCode(t *testing.T) {
	s, client, _ := atTheCode(t)
	out := flat(view(s))
	for _, want := range []string{"Authenticator code", "two-factor", "recovery codes"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen does not say %q:\n%s", want, out)
		}
	}

	// Without echo: a recovery code works until it is used, so what is typed
	// is dots on the screen, never the code.
	typing := make([]tea.Msg, 0, len(authtest.Code))
	for _, r := range authtest.Code {
		typing = append(typing, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	s = drive(t, s, client, typing...)
	if got := view(s); strings.Contains(got, authtest.Code) {
		t.Errorf("the code is echoed:\n%s", got)
	}
	s = enter(t, s, client, "")
	if !s.Done() || s.Token != authtest.Session {
		t.Fatalf("done=%v token=%q after the right code", s.Done(), s.Token)
	}
	if s.Notice != "" || s.challenge != nil {
		t.Errorf("notice %q, challenge kept: %v", s.Notice, s.challenge != nil)
	}
}

// A wrong code keeps the same question, empty, with what went wrong and how
// many tries are left.
func TestTheFlowAsksAgainAfterAWrongCode(t *testing.T) {
	s, client, p := atTheCode(t)
	s = enter(t, s, client, "000000")
	if s.stage != signInCode || s.Done() {
		t.Fatalf("a wrong code left the question: stage %d", s.stage)
	}
	if s.err == nil || !strings.Contains(s.err.Error(), "4 attempts left") {
		t.Errorf("err = %v", s.err)
	}
	if got := view(s); !strings.Contains(flat(got), "4 attempts left") || strings.Contains(got, "000000") {
		t.Errorf("the screen does not say what went wrong, or still holds the code:\n%s", got)
	}
	if got := s.form.GetString(fieldCode); got != "" {
		t.Errorf("the wrong code is still in the field: %q", got)
	}
	s = enter(t, s, client, authtest.Code)
	if !s.Done() || s.Token != authtest.Session {
		t.Errorf("the right code after a wrong one did not sign in: stage %d", s.stage)
	}
	if n := len(p.Challenges()); n != 2 {
		t.Errorf("%d challenge POSTs, want 2", n)
	}
}

// Something that is not a code is asked again too, without a request.
func TestTheFlowAsksAgainForSomethingThatIsNotACode(t *testing.T) {
	s, client, p := atTheCode(t)
	s = enter(t, s, client, "not a code")
	if s.stage != signInCode || s.err == nil || !strings.Contains(s.err.Error(), "neither a 6-digit code") {
		t.Fatalf("stage %d, err %v", s.stage, s.err)
	}
	if n := len(p.Challenges()); n != 0 {
		t.Errorf("%d challenge POSTs for something that is not a code", n)
	}
}

// A sign-in that cannot go on - timed out, too many wrong codes, refused -
// spent its link, so the flow goes back to the address, filled in: enter asks
// for a new one.
func TestTheFlowStartsAgainWhenTheSignInEnds(t *testing.T) {
	s, client, p := atTheCode(t)
	p.Expire()
	s = enter(t, s, client, authtest.Code)
	if s.stage != signInEmail || s.challenge != nil {
		t.Fatalf("stage %d, challenge kept: %v", s.stage, s.challenge != nil)
	}
	if got := view(s); !strings.Contains(got, "me@acme.io") {
		t.Errorf("the address field is not filled in:\n%s", got)
	}
	if s.err == nil || !strings.Contains(s.err.Error(), "new link") {
		t.Errorf("err = %v", s.err)
	}
	if s.Done() || s.Token != "" {
		t.Errorf("signed in: done=%v token=%q", s.Done(), s.Token)
	}
}

func TestTheFlowSaysHowManyRecoveryCodesAreLeft(t *testing.T) {
	s, client, p := atTheCode(t)
	p.RecoveryCodes = []string{"q7wa-z2xd-pl4r-6wze", "aaaa-bbbb-cccc-dddd"}
	s = enter(t, s, client, "Q7WA-Z2XD-PL4R-6WZE")
	if !s.Done() || s.Token != authtest.Session {
		t.Fatalf("done=%v token=%q", s.Done(), s.Token)
	}
	if !strings.Contains(s.Notice, "1 left") || !strings.Contains(s.Notice, "Two-factor authentication") {
		t.Errorf("Notice = %q", s.Notice)
	}
	if last := RecoveryCodesNotice(0); !strings.Contains(last, "last recovery code") {
		t.Errorf("RecoveryCodesNotice(0) = %q", last)
	}
}

// Esc from the code goes back, as the footer says: the link before it is
// spent, so to the address, filled in, with the challenge forgotten. An
// exchange that answers after the member left does not bring the question
// back.
func TestEscapeForgetsTheChallenge(t *testing.T) {
	s, client, _ := atTheCode(t)
	email := s.email
	s = drive(t, s, client, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !s.Active() || s.stage != signInEmail || s.challenge != nil {
		t.Errorf("active=%v stage=%d, challenge kept: %v", s.Active(), s.stage, s.challenge != nil)
	}
	if s.form.GetString(fieldEmail) != email && !strings.Contains(view(s), email) {
		t.Errorf("the address is not filled in after esc:\n%s", view(s))
	}
	if !strings.Contains(view(s), "spent") {
		t.Errorf("esc does not say the link is spent:\n%s", view(s))
	}

	late, client, _ := atTheCode(t)
	challenge := late.challenge
	late = drive(t, late, client, tea.KeyPressMsg{Code: tea.KeyEscape})
	late = drive(t, late, client, challengeMsg{challenge})
	if late.stage == signInCode || late.challenge != nil {
		t.Errorf("a late challenge reopened the code question: stage %d", late.stage)
	}
}

// Enter on an empty code is caught in the field, in a line, and nothing is
// sent. It used to get the long refusal of something that is not a code,
// cut off at 80 columns.
func TestAnEmptyCodeIsNotSent(t *testing.T) {
	s, client, p := atTheCode(t)
	before := len(p.Challenges())
	s = drive(t, s, client, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.stage != signInCode {
		t.Errorf("an empty code left the code question: stage %d", s.stage)
	}
	if n := len(p.Challenges()); n != before {
		t.Errorf("an empty code was sent: %d challenge POSTs", n-before)
	}
	if !strings.Contains(view(s), "type the code first") {
		t.Errorf("an empty enter is not answered in the field:\n%s", view(s))
	}
}

// The screen never shows the challenge cookie, nor does anything the flow
// holds that prints.
func TestTheFlowKeepsTheChallengeToItself(t *testing.T) {
	s, client, _ := atTheCode(t)
	s = enter(t, s, client, "000000")
	if strings.Contains(view(s), authtest.Pending) || (s.err != nil && strings.Contains(s.err.Error(), authtest.Pending)) {
		t.Error("the challenge cookie is on screen")
	}
}

// An account that has not finished signing up and has a factor: the flow
// stays on the link question with the sentence that sends it to the browser,
// and asks for no code.
func TestTheFlowSendsAnUnfinishedSignUpToTheBrowser(t *testing.T) {
	p := authtest.New(t)
	p.Factor, p.Onboarding = true, true
	client := api.New("", api.WithBaseURL(p.URL))

	s := NewSignIn()
	s.Start("")
	s = drive(t, s, client, typed("me@acme.io")...)
	s = drive(t, s, client, linkSentMsg{})
	s, verifying := driveToNetwork(t, s, client, typed(mfaTestLink)...)
	s = drive(t, s, client, verifying())

	if s.stage != signInLink || s.challenge != nil {
		t.Fatalf("stage %d, challenge %v", s.stage, s.challenge != nil)
	}
	const want = "your NaN account is not set up yet - finish it in the browser: ask for a new link at " +
		"https://cloud.nan.builders (this one is spent), complete the setup there, then sign in here again"
	if s.err == nil || s.err.Error() != want {
		t.Errorf("err = %v", s.err)
	}
	if !strings.Contains(flat(view(s)), "finish it in the browser") {
		t.Errorf("the screen does not say it:\n%s", view(s))
	}
	if s.Done() || s.Token != "" {
		t.Errorf("signed in: done=%v token=%q", s.Done(), s.Token)
	}
}

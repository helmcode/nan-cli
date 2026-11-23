package flow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/api/authtest"
	"github.com/helmcode/nan-cli/internal/browser"
)

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// quiet stubs the browser and clears the environment that would stop it, so
// a test sees what the flow decides rather than what the machine allows.
func quiet(t *testing.T) *[]string {
	t.Helper()
	t.Setenv("NAN_NO_BROWSER", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	var opened []string
	old := browser.Open
	browser.Open = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { browser.Open = old })
	return &opened
}

func clientFor(p *authtest.Platform) *api.Client {
	return api.New("", api.WithBaseURL(p.URL), api.WithClock(time.Now, func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}))
}

// run executes the commands a step returned, as the Bubble Tea runtime does,
// and returns what they produced. Batches are flattened.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, run(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// feed runs cmd and feeds every message it produced back into the flow,
// until nothing more comes.
func feed(t *testing.T, s SignIn, client *api.Client, cmd tea.Cmd) SignIn {
	t.Helper()
	queue := run(cmd)
	for i := 0; len(queue) > 0 && i < 20; i++ {
		var next tea.Cmd
		s, next = s.Update(context.Background(), queue[0], client)
		queue = append(queue[1:], run(next)...)
	}
	return s
}

// Opening the dashboard without a session must not create a device request,
// spend the start rate limit or steal focus with a browser tab: it waits for
// enter.
func TestStartAsksNothingOfThePlatformUntilEnter(t *testing.T) {
	opened := quiet(t)
	p := authtest.New(t)
	s := NewSignIn()
	if cmd := s.Start(""); cmd != nil {
		t.Fatal("Start returned a command: it would talk to the platform on its own")
	}
	if !s.Active() {
		t.Fatal("not active")
	}
	if got := prose(s.View("", "", "")); !strings.Contains(got, "Press enter to sign in with your browser") {
		t.Errorf("the screen does not say what enter does:\n%s", got)
	}
	if len(p.Starts()) != 0 || len(*opened) != 0 {
		t.Error("something happened before enter")
	}

	var cmd tea.Cmd
	s, cmd = s.Update(context.Background(), enter, clientFor(p))
	if s.Label() != "Asking for a code" || cmd == nil {
		t.Fatalf("enter did not start the request: label %q", s.Label())
	}
	s = waiting(t, s, clientFor(p), cmd)
	if len(p.Starts()) != 1 || p.Starts()[0]["client"] != "cli" {
		t.Errorf("starts = %v", p.Starts())
	}
}

func TestBeginIsAnExplicitRequestAndStartsAtOnce(t *testing.T) {
	opened := quiet(t)
	p := authtest.New(t)
	client := clientFor(p)
	s := NewSignIn()
	s = waiting(t, s, client, s.Begin(context.Background(), client, ""))
	view := prose(s.View("", "SPIN", ""))
	for _, want := range []string{authtest.UserCode, p.URL + "/device", "Tried to open it in your browser", "expires in"} {
		if !strings.Contains(view, want) {
			t.Errorf("the waiting screen does not say %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, authtest.DeviceCode) {
		t.Error("the device code is on screen")
	}
	if len(*opened) != 1 {
		t.Errorf("opened %v, want the page once", *opened)
	}
}

func TestOverSSHThePageIsNotOpenedAndNotClaimed(t *testing.T) {
	opened := quiet(t)
	t.Setenv("SSH_CONNECTION", "1.2.3.4 1 5.6.7.8 22")
	p := authtest.New(t)
	client := clientFor(p)
	s := NewSignIn()
	s = waiting(t, s, client, s.Begin(context.Background(), client, ""))
	view := prose(s.View("", "", ""))
	if len(*opened) != 0 || strings.Contains(view, "open it in your browser") {
		t.Errorf("opened %v / view:\n%s", *opened, view)
	}
	if !strings.Contains(view, p.URL+"/device") {
		t.Error("the address is not on screen")
	}
}

func TestApprovalLeavesTheSessionAndEndsTheFlow(t *testing.T) {
	quiet(t)
	p := authtest.New(t)
	p.Approve()
	client := clientFor(p)
	s := NewSignIn()
	s = feed(t, s, client, s.Begin(context.Background(), client, ""))
	if !s.Done() || s.Active() || s.Token != authtest.Session {
		t.Fatalf("done=%v active=%v token=%q", s.Done(), s.Active(), s.Token)
	}
}

func TestADeniedOrExpiredCodeOffersANewOneOnEnter(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*authtest.Platform)
		want string
	}{
		{"denied", (*authtest.Platform).Deny, "denied"},
		{"expired", (*authtest.Platform).Expire, "expired"},
	} {
		t.Run(c.name, func(t *testing.T) {
			quiet(t)
			p := authtest.New(t)
			c.set(p)
			client := clientFor(p)
			s := NewSignIn()
			s = feed(t, s, client, s.Begin(context.Background(), client, ""))
			if !s.Active() || s.Done() {
				t.Fatal("the flow ended instead of offering another go")
			}
			view := prose(s.View("", "", ""))
			if !strings.Contains(view, c.want) || !strings.Contains(view, "press enter for a new code") && !strings.Contains(view, "press enter if you want a new code") {
				t.Errorf("view:\n%s", view)
			}
			p.Approve()
			var cmd tea.Cmd
			s, cmd = s.Update(context.Background(), enter, client)
			s = feed(t, s, client, cmd)
			if !s.Done() || len(p.Starts()) != 2 {
				t.Errorf("done=%v starts=%d: enter did not start a new request", s.Done(), len(p.Starts()))
			}
		})
	}
}

func TestAServerErrorWhilePollingDoesNotEndTheFlow(t *testing.T) {
	quiet(t)
	p := authtest.New(t)
	p.FailNext(503, 2)
	p.Approve()
	client := clientFor(p)
	s := NewSignIn()
	s = feed(t, s, client, s.Begin(context.Background(), client, ""))
	if !s.Done() {
		t.Fatalf("a 503 ended the sign-in:\n%s", s.View("", "", ""))
	}
}

func TestEscCancelsThePollingAndALateAnswerIsDropped(t *testing.T) {
	quiet(t)
	p := authtest.New(t)
	client := clientFor(p)
	s := NewSignIn()
	cmd := s.Begin(context.Background(), client, "")
	s, _ = s.Update(context.Background(), run(cmd)[0], client) // waiting
	if s.Label() != "Waiting for approval" {
		t.Fatalf("label = %q", s.Label())
	}
	var late tea.Msg = deviceDoneMsg{attempt: s.attempt, token: "late-token"}
	s, _ = s.Update(context.Background(), esc, client)
	if s.Active() {
		t.Fatal("esc did not close the flow")
	}
	s, _ = s.Update(context.Background(), late, client)
	if s.Done() || s.Token != "" {
		t.Error("an approval that arrived after cancelling signed the member in")
	}
}

func TestEnterOnTheWaitingScreenReopensThePageUnlessDisabled(t *testing.T) {
	opened := quiet(t)
	p := authtest.New(t)
	client := clientFor(p)
	s := NewSignIn()
	cmd := s.Begin(context.Background(), client, "")
	s, _ = s.Update(context.Background(), run(cmd)[0], client)
	*opened = nil
	_, again := s.Update(context.Background(), enter, client)
	run(again)
	if len(*opened) != 1 {
		t.Errorf("enter opened %v", *opened)
	}
	if got := keysOf(s); !strings.Contains(got, "open the page again") {
		t.Errorf("keys = %q", got)
	}

	t.Setenv("NAN_NO_BROWSER", "1")
	if got := keysOf(s); strings.Contains(got, "open the page again") {
		t.Errorf("NAN_NO_BROWSER set and the footer still offers to open it: %q", got)
	}
}

func keysOf(s SignIn) string {
	var b strings.Builder
	for _, k := range s.Keys() {
		b.WriteString(k[0] + " " + k[1] + "; ")
	}
	return b.String()
}

// The notice about a credential the dashboard cannot use is its own line on
// every screen, not something the next screen clears.
func TestTheNoticeSurvivesEveryStep(t *testing.T) {
	quiet(t)
	p := authtest.New(t)
	client := clientFor(p)
	s := NewSignIn()
	cmd := s.Begin(context.Background(), client, TokenOnlyNotice)
	if !strings.Contains(prose(s.View("", "", "")), "works for nan run and nan runs only") {
		t.Error("the notice is missing while the code is being asked for")
	}
	s = waiting(t, s, client, cmd)
	if !strings.Contains(prose(s.View("", "", "")), "works for nan run and nan runs only") {
		t.Errorf("the notice was cleared by the request starting:\n%s", s.View("", "", ""))
	}
}

func TestAnUnsupportedPlatformIsSaidAndNotPolled(t *testing.T) {
	quiet(t)
	p := authtest.New(t)
	p.Legacy = true
	client := clientFor(p)
	s := NewSignIn()
	s = feed(t, s, client, s.Begin(context.Background(), client, ""))
	view := prose(s.View("", "", ""))
	if !strings.Contains(view, "does not support signing in from the CLI") || p.Polls() != 0 {
		t.Errorf("polls=%d view:\n%s", p.Polls(), view)
	}
}

// typed is the keys of a string followed by enter, for the key field.
func typed(text string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(text)+1)
	for _, r := range text {
		msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return append(msgs, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// waiting takes the flow from a start command to the waiting screen and lets
// the commands that screen launches (the polling, the browser) run briefly
// before stopping the polling: a platform that stays pending would otherwise
// keep the polling command from ever returning.
func waiting(t *testing.T, s SignIn, client *api.Client, start tea.Cmd) SignIn {
	t.Helper()
	msgs := run(start)
	if len(msgs) != 1 {
		t.Fatalf("the start command produced %d messages", len(msgs))
	}
	s, cmd := s.Update(context.Background(), msgs[0], client)
	if s.stage != signInWaiting {
		t.Fatalf("not waiting: stage %d, view:\n%s", s.stage, s.View("", "", ""))
	}
	stop := s.cancel
	t.Cleanup(stop)
	if cmd == nil {
		return s
	}
	var wg sync.WaitGroup
	var launch func(tea.Cmd)
	launch = func(c tea.Cmd) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if batch, ok := c().(tea.BatchMsg); ok {
				for _, inner := range batch {
					launch(inner)
				}
			}
		}()
	}
	launch(cmd)
	time.AfterFunc(100*time.Millisecond, stop)
	wg.Wait()
	return s
}

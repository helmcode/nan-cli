// Package flow holds the two interactive flows the dashboard runs: signing
// in, and pointing this machine's tools at NaN.
//
// They live apart from the dashboard because they are flows of their own,
// and a second copy is how two screens that are supposed to be the same
// program start disagreeing about what an expired code says. Each is a small
// Bubble Tea model - Init, Update, View - so a parent embeds it, forwards
// messages while it is active, and reads the result.
package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/browser"
	"github.com/helmcode/nan-cli/internal/ui"
)

type signInStage int

const (
	signInOff signInStage = iota
	// signInReady: nothing has been asked of the platform yet. The member
	// presses enter to start, so opening the dashboard without a session does
	// not create a device request, spend the start rate limit or steal focus
	// with a browser tab.
	signInReady
	// signInStarting: the platform has been asked for a code.
	signInStarting
	// signInWaiting: the code is on screen and the platform is being polled.
	signInWaiting
	// signInFailed: the start, the wait or the approval ended in an error.
	signInFailed
	signInDone
)

// SignIn is the browser-approved device flow: start a request, show a code
// and a page, and poll until the member approves it there. The browser that
// approves satisfies the password-less sign-in and any two-factor code, so
// this flow never asks for either. nan.builders has no password, so this is
// the first thing anybody meets and the only thing that works before it is
// done.
type SignIn struct {
	stage signInStage
	err   error
	// notice is said above everything, as its own line, for a machine that
	// already holds a credential the dashboard cannot use.
	notice string

	// device is the request in flight. Its DeviceCode is a bearer credential:
	// never rendered, logged or put in an error.
	device *api.Device
	// opened is whether the page was tried in a browser for this request.
	opened bool
	// attempt goes up on every start and every cancel, so a late answer from
	// an attempt that was left is dropped. cancel stops the polling.
	attempt int
	cancel  context.CancelFunc

	// Token is the session the platform issued, once the flow is done.
	Token string
}

// deviceStartedMsg and deviceDoneMsg are this flow's own messages. A parent
// forwards everything while the flow is active, so they never need to be
// exported.
type deviceStartedMsg struct {
	attempt int
	device  *api.Device
	err     error
}

type deviceDoneMsg struct {
	attempt int
	token   string
	err     error
}

// NewSignIn returns a flow that is not running yet.
func NewSignIn() SignIn { return SignIn{} }

// Active reports whether the flow owns the screen.
func (s SignIn) Active() bool { return s.stage != signInOff && s.stage != signInDone }

// Done reports whether it finished and left a session behind.
func (s SignIn) Done() bool { return s.stage == signInDone }

// TokenOnlyNotice is what the sign-in says on a machine that has a platform
// token and no session: the token is real and working, and still not
// something the dashboard can use, which is otherwise a mystery.
const TokenOnlyNotice = "the platform token on this machine works for nan run and nan runs only; " +
	"the dashboard needs the browser sign-in"

// Start opens the flow on its first screen and asks nothing of the platform:
// the member presses enter to sign in. notice, when there is one, is said
// above. It is for the places the dashboard opens it on its own.
func (s *SignIn) Start(notice string) tea.Cmd {
	s.stop()
	*s = SignIn{stage: signInReady, notice: notice, attempt: s.attempt}
	return nil
}

// Begin opens the flow and starts the request at once. It is for a member who
// asked to sign in (the s key, the card, the menu): that is consent to the
// request and to the browser tab.
func (s *SignIn) Begin(ctx context.Context, client *api.Client, notice string) tea.Cmd {
	s.Start(notice)
	return s.begin(ctx, client)
}

// begin starts (or restarts) the device request.
func (s *SignIn) begin(ctx context.Context, client *api.Client) tea.Cmd {
	s.stop()
	s.attempt++
	s.stage = signInStarting
	s.err = nil
	s.device = nil
	s.opened = false
	attempt := s.attempt
	return func() tea.Msg {
		d, err := client.StartDevice(ctx)
		return deviceStartedMsg{attempt: attempt, device: d, err: err}
	}
}

// stop ends the polling of the request in flight, if there is one.
func (s *SignIn) stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// Resize is kept for the parent's resize pass: this flow has no form to fit.
func (s *SignIn) Resize(int, int) {}

// Keys is what the footer offers on the screen the flow is on.
func (s SignIn) Keys() [][2]string {
	switch s.stage {
	case signInReady:
		return [][2]string{{"enter", "sign in with your browser"}, {"esc", "not now"}}
	case signInWaiting:
		keys := [][2]string{}
		if !browser.Disabled() {
			keys = append(keys, [2]string{"enter", "open the page again"})
		}
		return append(keys, [2]string{"esc", "cancel"})
	case signInFailed:
		return [][2]string{{"enter", "try again"}, {"esc", "close"}}
	}
	return [][2]string{{"esc", "cancel"}}
}

// Update drives the flow. The parent forwards every message while Active.
func (s SignIn) Update(ctx context.Context, msg tea.Msg, client *api.Client) (SignIn, tea.Cmd) {
	switch msg := msg.(type) {
	case deviceStartedMsg:
		// A late answer from an attempt that was cancelled or restarted.
		if msg.attempt != s.attempt || s.stage != signInStarting {
			return s, nil
		}
		if msg.err != nil {
			s.stage = signInFailed
			s.err = startError(msg.err)
			return s, nil
		}
		s.stage = signInWaiting
		s.device = msg.device
		waitCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		cmds := []tea.Cmd{waitForApproval(waitCtx, client, msg.attempt, msg.device)}
		if browser.ShouldOpen() {
			s.opened = true
			cmds = append(cmds, openPage(msg.device.VerificationURL))
		}
		return s, tea.Batch(cmds...)

	case deviceDoneMsg:
		if msg.attempt != s.attempt || s.stage != signInWaiting {
			return s, nil
		}
		s.stop()
		if msg.err != nil {
			s.stage = signInFailed
			s.err = waitError(msg.err)
			return s, nil
		}
		s.err = nil
		s.Token = msg.token
		s.device = nil
		s.stage = signInDone
		return s, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			s.stop()
			s.attempt++
			return SignIn{attempt: s.attempt}, nil
		case "enter":
			switch s.stage {
			case signInReady, signInFailed:
				cmd := s.begin(ctx, client)
				return s, cmd
			case signInWaiting:
				if !browser.Disabled() && s.device != nil {
					s.opened = true
					return s, openPage(s.device.VerificationURL)
				}
			}
		}
	}
	return s, nil
}

// waitForApproval polls until the platform has an answer. The session token
// comes back in the message and nowhere else.
func waitForApproval(ctx context.Context, client *api.Client, attempt int, d *api.Device) tea.Cmd {
	return func() tea.Msg {
		token, err := client.WaitForApproval(ctx, d)
		return deviceDoneMsg{attempt: attempt, token: token, err: err}
	}
}

// openPage tries the browser. Best effort: the address is on screen anyway.
func openPage(url string) tea.Cmd {
	return func() tea.Msg {
		_ = browser.Open(url)
		return nil
	}
}

// startError and waitError say the sign-in's end in the dashboard's own
// words; the sentinels carry none.
func startError(err error) error {
	if errors.Is(err, api.ErrDeviceUnsupported) {
		return fmt.Errorf("%w - try again later", err)
	}
	return err
}

func waitError(err error) error {
	switch {
	case errors.Is(err, api.ErrDeviceExpired):
		return fmt.Errorf("%w - press enter for a new code", err)
	case errors.Is(err, api.ErrDeviceDenied):
		return fmt.Errorf("%w - press enter if you want a new code", err)
	}
	return err
}

// Label is what the loader should say for the step the flow is on. The
// parent owns the animation - one loader for the whole program, so it does
// not restart every time a screen changes - and asks the flow what it is
// waiting for.
func (s SignIn) Label() string {
	switch s.stage {
	case signInStarting:
		return "Asking for a code"
	case signInWaiting:
		return "Waiting for approval"
	default:
		return "Loading"
	}
}

// View renders the flow. `spinner` is the parent's rendered loader, so the
// whole program animates as one thing.
func (s SignIn) View(indent, spinner string, apiHost string) string {
	var b strings.Builder
	if s.notice != "" {
		// Its own line, on every screen: the machine that holds a token the
		// dashboard cannot use needs to read why, whichever step it is on.
		b.WriteString(indent + ui.Warn(s.notice) + "\n\n")
	}

	switch s.stage {
	case signInReady:
		b.WriteString(indent + ui.Value("Sign in to nan.builders") + "\n")
		b.WriteString(indent + ui.Faint("Your browser approves the sign-in: no password and no link to paste.") + "\n\n")
		b.WriteString(indent + "Press enter to sign in with your browser.\n")

	case signInStarting:
		b.WriteString(indent + spinner + "\n")

	case signInWaiting:
		s.waiting(&b, indent, spinner)
	}

	if s.err != nil {
		b.WriteString("\n" + indent + ui.Error(s.err) + "\n")
	}
	if apiHost != "" && s.stage != signInOff {
		b.WriteString("\n" + indent + ui.Faint("Platform: "+apiHost+" - not the real nan.builders.") + "\n")
	}
	return b.String()
}

// waiting is the code, the page it is typed on, and how long it lasts.
func (s SignIn) waiting(b *strings.Builder, indent, spinner string) {
	d := s.device
	if d == nil {
		return
	}
	b.WriteString(indent + "Your sign-in code:  " + ui.Value(d.UserCode) + "\n\n")
	b.WriteString(indent + "Open " + d.VerificationURL + ", sign in if asked,\n")
	b.WriteString(indent + "and type the code there.\n")
	if s.opened {
		// An attempt, not a fact: a browser that starts and fails to show
		// the page cannot be told from one that worked.
		b.WriteString(indent + ui.Faint("(Tried to open it in your browser.)") + "\n")
	}
	b.WriteString("\n" + indent + spinner + ui.Faint(expiresIn(d.ExpiresAt)) + "\n")
}

// expiresIn is the countdown shown next to the wait.
func expiresIn(at time.Time) string {
	left := time.Until(at)
	if left <= 0 {
		return "  expiring"
	}
	return fmt.Sprintf("  expires in %d min", int((left+time.Minute-1)/time.Minute))
}

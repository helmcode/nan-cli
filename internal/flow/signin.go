// Package flow holds the two interactive flows the dashboard runs: signing
// in, and pointing this machine's tools at NaN.
//
// They live apart from the dashboard because they are flows of their own,
// and a second copy is how two screens that are supposed to be the same
// program start disagreeing about what an expired link says. Each is a small
// Bubble Tea model - Init, Update, View - so a parent embeds it, forwards
// messages while it is active, and reads the result.
package flow

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/ui"
)

type signInStage int

const (
	signInOff signInStage = iota
	signInEmail
	signInSending
	signInLink
	signInVerifying
	signInDone
)

// SignIn is the magic-link flow: ask for an address, send a link, take it
// back. nan.builders has no password, so this is the first thing anybody
// meets and the only thing that works before it is done.
type SignIn struct {
	stage signInStage
	form  *huh.Form
	email string
	err   error
	// notice is said above the address, for a machine that already holds a
	// credential the dashboard cannot use.
	notice string

	// Token is the session the platform issued, once the flow is done.
	Token string
}

// linkSent and signedIn are this flow's own messages. A parent forwards
// everything while the flow is active, so they never need to be exported.
type linkSentMsg struct{ err error }

type signedInMsg struct {
	token string
	err   error
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
	"the dashboard needs the email sign-in"

// Start opens the flow on the email step. notice, when there is one, is said
// above the address.
func (s *SignIn) Start(notice string) tea.Cmd {
	*s = SignIn{stage: signInEmail, notice: notice}
	s.form = emailForm(s.email)
	return s.form.Init()
}

// The keys the fields are read back by.
//
// By KEY, never by binding a pointer with huh's Value(&x). Bubble Tea hands
// Update a copy of the model and keeps what it returns, so a pointer into one
// of the model's fields points into a copy that has already been discarded:
// the form writes the address there and the model that is actually running
// reads its own field and finds it empty. The form itself is a pointer and
// survives the copying, so asking IT is the only reading that stays true.
const (
	fieldEmail = "email"
	fieldLink  = "link"
)

func emailForm(previous string) *huh.Form {
	// Seeded, not bound: the value is read back by key (see fieldEmail), and
	// this local only fills the field when somebody comes back to fix a
	// typo in the address they already typed.
	value := previous
	return ui.NewForm(huh.NewGroup(
		huh.NewInput().
			Key(fieldEmail).
			Value(&value).
			Title("Sign in to nan.builders").
			Description("Your email address - a link goes there. nan.builders has no password.").
			Placeholder("you@example.com").
			// Validated in the field, so a typo is caught before an email is
			// spent on it rather than after.
			Validate(api.ValidateEmail),
	))
}

func linkForm(client *api.Client) *huh.Form {
	return ui.NewForm(huh.NewGroup(
		huh.NewInput().
			Key(fieldLink).
			Title("Paste the link").
			Description("It works once - do not open it in your browser first, the browser would spend it.").
			Placeholder("https://nan.builders/...?token=...").
			// Dots, not the link: it carries a credential, and a screen is
			// shared, recorded and screenshotted more often than not.
			EchoMode(huh.EchoModePassword).
			// The host check runs here, so a link that is not ours is refused
			// in the field rather than after its token has been posted.
			Validate(func(v string) error {
				_, err := client.TokenFromLink(v)
				return err
			}),
	))
}

// Resize rewraps the open form, if there is one, for a terminal of w by h.
func (s *SignIn) Resize(w, h int) { ui.FitForm(s.form, w, h) }

// Update drives the flow. The parent forwards every message while Active.
func (s SignIn) Update(ctx context.Context, msg tea.Msg, client *api.Client) (SignIn, tea.Cmd) {
	switch msg := msg.(type) {
	case linkSentMsg:
		if msg.err != nil {
			s.err = msg.err
			s.stage = signInEmail
			s.form = emailForm(s.email)
			return s, s.form.Init()
		}
		s.err = nil
		s.stage = signInLink
		s.form = linkForm(client)
		return s, s.form.Init()

	case signedInMsg:
		if msg.err != nil {
			// Back to the link, not to the start: the email has been spent
			// and asking for another to fix a bad paste is a worse flow.
			s.err = msg.err
			s.stage = signInLink
			s.form = linkForm(client)
			return s, s.form.Init()
		}
		s.err = nil
		s.Token = msg.token
		s.stage = signInDone
		return s, nil

	case tea.KeyPressMsg:
		if msg.String() == "esc" {
			// From the link step, esc goes back to the address rather than
			// out of the sign-in altogether. The one thing this flow cannot
			// tell you is whether the address was right - the platform says
			// "sent" for every address, on purpose, so that it cannot be
			// used to find out who has an account - and a mistyped one is
			// the likeliest reason a link never arrives.
			if s.stage == signInLink {
				s.stage = signInEmail
				s.err = nil
				s.form = emailForm(s.email)
				return s, s.form.Init()
			}
			return SignIn{}, nil
		}
	}

	if s.form == nil || s.stage == signInSending || s.stage == signInVerifying {
		return s, nil
	}

	form, cmd := s.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		s.form = f
	}

	switch s.form.State {
	case huh.StateAborted:
		return SignIn{}, nil
	case huh.StateCompleted:
		switch s.stage {
		case signInEmail:
			value := strings.TrimSpace(s.form.GetString(fieldEmail))
			s.email = value
			s.stage = signInSending
			return s, requestLink(ctx, client, value)
		case signInLink:
			token, err := client.TokenFromLink(strings.TrimSpace(s.form.GetString(fieldLink)))
			if err != nil {
				s.err = err
				s.form = linkForm(client)
				return s, s.form.Init()
			}
			s.stage = signInVerifying
			return s, verify(ctx, client, token)
		}
	}
	return s, cmd
}

func requestLink(ctx context.Context, client *api.Client, email string) tea.Cmd {
	return func() tea.Msg {
		return linkSentMsg{err: client.RequestMagicLink(ctx, email)}
	}
}

func verify(ctx context.Context, client *api.Client, token string) tea.Cmd {
	return func() tea.Msg {
		session, err := client.Verify(ctx, token)
		return signedInMsg{token: session, err: err}
	}
}

// Label is what the loader should say for the step the flow is on. The
// parent owns the animation — one loader for the whole program, so it does
// not restart every time a screen changes — and asks the flow what it is
// waiting for.
func (s SignIn) Label() string {
	switch s.stage {
	case signInSending:
		return "Sending the link"
	case signInVerifying:
		return "Checking the link"
	default:
		return "Loading"
	}
}

// View renders the flow. `spinner` is the parent's rendered loader, so the
// whole program animates as one thing.
func (s SignIn) View(indent, spinner string, apiHost string) string {
	var b strings.Builder

	switch s.stage {
	case signInEmail:
		if s.notice != "" {
			b.WriteString(indent + ui.Warn(s.notice) + "\n\n")
		}
		b.WriteString(s.form.View() + "\n")

	case signInSending:
		b.WriteString(indent + spinner + "\n")

	case signInLink:
		s.linkSent(&b, indent, apiHost)

		b.WriteString("\n" + s.form.View() + "\n")

	case signInVerifying:
		b.WriteString(indent + spinner + "\n")
	}

	if s.err != nil {
		b.WriteString("\n" + indent + ui.Error(s.err) + "\n")
	}
	return b.String()
}

// linkSent is what the link step says once a real email is on its way.
func (s SignIn) linkSent(b *strings.Builder, indent, apiHost string) {
	b.WriteString(indent + ui.Success("A link is on its way to "+s.email) + "\n")
	// Said plainly, because nothing downstream can: the platform answers
	// the same for an address with no account behind it.
	// Two short lines, not one long one: this screen is drawn outside the
	// panes, so nothing clips it, and a line the terminal has to wrap is
	// a row the inline renderer never counted.
	b.WriteString(indent + ui.Faint("Copy it out of the email without opening it: it works once, and expires") + "\n")
	b.WriteString(indent + ui.Faint(api.LinkValidity+" after it is sent. It only arrives if that address has an account.") + "\n")
	b.WriteString(indent + ui.Faint("Nothing after a minute? Check spam, or esc to fix the address.") + "\n")
	// Which platform, when it is not the real one. "A link is on its
	// way" with no host on it is unfalsifiable: production answers 202
	// to any address, so a shell that has lost NAN_API_URL gives the
	// identical line and no email.
	if apiHost != "" {
		b.WriteString(indent + ui.Faint("Sent to "+apiHost+" - not the real nan.builders.") + "\n")
	}
}

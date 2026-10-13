package flow

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nxssie/nan-cli/internal/api"
)

// Bubble Tea hands Update a COPY of the model and keeps what it returns, so a
// pointer into one of the model's fields — which is how huh's Value(&x) binds
// — points into a copy that has already been discarded. The form writes the
// typed address there; the copy that is actually running reads its own field
// and finds it empty.
//
// That is not a subtle failure. It sent {"email":""} to the control plane,
// which answers invalid_request, which this CLI renders as "this CLI sent
// something the control plane could not read — please report it". Somebody
// typing their address correctly was told the program was broken. It was.

// drive is the part of the Bubble Tea runtime these flows need: run the
// command an update returned, and feed its message back in.
//
// Commands run on their own goroutine with a deadline, exactly because the
// runtime does not wait for them either. Every keystroke into a text field
// returns the cursor's blink — a tick half a second out — and waiting on
// those in turn is thirteen seconds of test for a form with an email in it.
func drive(t *testing.T, s SignIn, client *api.Client, msgs ...tea.Msg) SignIn {
	t.Helper()
	ctx := context.Background()

	queue := append([]tea.Msg{}, msgs...)
	for i := 0; len(queue) > 0 && i < 200; i++ {
		msg := queue[0]
		queue = queue[1:]

		var cmd tea.Cmd
		s, cmd = s.Update(ctx, msg, client)

		// Stop at the first stage that would talk to the network: what these
		// tests check is what the flow decided to send, not the sending.
		if s.stage == signInSending || s.stage == signInVerifying || cmd == nil {
			if s.stage == signInSending || s.stage == signInVerifying {
				return s
			}
			continue
		}

		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		select {
		case m := <-done:
			if m != nil {
				queue = append(queue, m)
			}
		case <-time.After(40 * time.Millisecond):
			// A timer, not an answer. The runtime would deliver it later;
			// nothing here is waiting on one.
		}
	}
	return s
}

func typed(text string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(text)+1)
	for _, r := range text {
		msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return append(msgs, tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestTheTypedEmailSurvivesTheModelBeingCopied(t *testing.T) {
	client := api.New("")

	s := NewSignIn()
	s.Start("")
	s = drive(t, s, client, typed("dev@acme.com")...)

	if s.stage != signInSending {
		t.Fatalf("the flow is at stage %d after submitting an address, want signInSending", s.stage)
	}
	if s.email != "dev@acme.com" {
		t.Fatalf("the flow kept %q as the address to send to, want %q - "+
			"an empty one reaches the control plane as invalid_request", s.email, "dev@acme.com")
	}
}

// The pasted link was bound the same way and failed the same way: read as
// empty, and posted as empty.
func TestThePastedLinkSurvivesTheModelBeingCopied(t *testing.T) {
	client := api.New("")

	s := NewSignIn()
	s.Start("")
	s = drive(t, s, client, typed("dev@acme.com")...)

	// The link step opens when the send comes back.
	s = drive(t, s, client, linkSentMsg{})
	if s.stage != signInLink {
		t.Fatalf("stage is %d after the link was sent, want signInLink", s.stage)
	}

	s = drive(t, s, client, typed("https://nan.builders/auth/verify?token=abc123")...)
	if s.stage != signInVerifying {
		t.Fatalf("stage is %d after pasting a good link, want signInVerifying - "+
			"a link read as empty is refused in the field instead of being spent", s.stage)
	}
}

// An address that is not one never reaches the network: the field refuses it,
// so no email is spent on a typo and the control plane is not asked a
// question with an obvious answer.
func TestAnAddressThatIsNotOneNeverLeaves(t *testing.T) {
	client := api.New("")

	for _, bad := range []string{"notanemail", "y@y", "", "  "} {
		s := NewSignIn()
		s.Start("")
		s = drive(t, s, client, typed(bad)...)
		if s.stage == signInSending {
			t.Errorf("the flow tried to send a link to %q", bad)
		}
	}
}

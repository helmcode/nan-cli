package flow

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The Setup flow had the same binding bug as the sign-in: huh wrote the typed
// key into a copy of the model that Bubble Tea had already thrown away, so
// what reached the check was an empty string. An empty key is refused by the
// inference API, so a correctly pasted key was reported as a bad one.

func driveSetup(t *testing.T, s Setup, msgs ...tea.Msg) Setup {
	t.Helper()
	ctx := context.Background()

	queue := append([]tea.Msg{}, msgs...)
	for i := 0; len(queue) > 0 && i < 200; i++ {
		msg := queue[0]
		queue = queue[1:]

		var cmd tea.Cmd
		s, cmd = s.Update(ctx, msg, nil)

		// Stop before anything that would reach the network or write a file.
		if s.stage == setupChecking || s.stage == setupApplying {
			return s
		}
		if cmd == nil {
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
			// The cursor's blink. Nothing here waits on a timer.
		}
	}
	return s
}

func TestThePastedKeySurvivesTheModelBeingCopied(t *testing.T) {
	var s Setup
	s.Start("", "")
	s = driveSetup(t, s, typed("sk-abc123")...)

	if s.stage != setupChecking {
		t.Fatalf("the flow is at stage %d after submitting a key, want setupChecking", s.stage)
	}
	if s.key != "sk-abc123" {
		t.Fatalf("the flow kept %q as the key to check, want %q - "+
			"an empty one is refused and reported as a bad key", s.key, "sk-abc123")
	}
}

// Re-running Setup on a machine that already has a key opens on it, so it is
// a confirmation rather than a retype — and pressing enter straight away has
// to submit THAT key, not an empty one.
func TestTheExistingKeyIsSeededAndSubmittable(t *testing.T) {
	var s Setup
	s.Start("sk-alreadyhere", "")
	s = driveSetup(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})

	if s.stage != setupChecking {
		t.Fatalf("stage is %d after accepting the saved key, want setupChecking", s.stage)
	}
	if s.key != "sk-alreadyhere" {
		t.Errorf("the saved key came through as %q", s.key)
	}
}

// An empty key never reaches the network: there is nothing to check and the
// answer would be a misleading "that key is not valid".
func TestAnEmptyKeyNeverLeaves(t *testing.T) {
	var s Setup
	s.Start("", "")
	s = driveSetup(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})

	if s.stage == setupChecking {
		t.Error("the flow tried to check an empty key")
	}
}

package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	catalog "github.com/helmcode/nan-cli/internal/models"
	"github.com/helmcode/nan-cli/internal/tools"
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
		s, cmd = s.Update(ctx, msg, nil, nil)

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

// When no model list could be had, the summary says nothing was written and
// what to do next - not "Key saved." with the tools silently left as they were.
func TestSetupSaysNothingWasWrittenWhenThereIsNoModelList(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"no models":  {catalog.ErrNoModels, "lists no models"},
		"unreadable": {errors.New(`Get "http://x/v1/models": dial tcp: refused`), "Check your connection"},
	} {
		t.Run(name, func(t *testing.T) {
			s := Setup{stage: setupApplying, Key: "sk-x"}
			s, _ = s.Update(context.Background(), appliedMsg{err: c.err}, nil, nil)
			if !s.Finished() {
				t.Fatalf("stage %d, want done", s.stage)
			}
			view := s.View("", "")
			for _, want := range []string{"nothing was written", c.want, "Setup again"} {
				if !strings.Contains(view, want) {
					t.Errorf("view lacks %q:\n%s", want, view)
				}
			}
			if strings.Contains(view, "dial tcp") || strings.Contains(view, "Key saved") {
				t.Errorf("a raw error, or a success line, reached the member:\n%s", view)
			}
		})
	}
}

// With no session and a key the cluster does not answer for, the command that
// runs off the event loop reports the failure instead of writing.
func TestApplyToolsReportsAMissingModelListInsteadOfWriting(t *testing.T) {
	t.Setenv("NAN_INFERENCE_URL", "http://127.0.0.1:1/v1")
	msg := applyTools(context.Background(), nil, "sk-x", map[string]bool{})()
	got, ok := msg.(appliedMsg)
	if !ok || got.err == nil {
		t.Fatalf("got %#v, want an appliedMsg carrying the error", msg)
	}
	if len(got.result.Written) != 0 || len(got.result.Failed) != 0 {
		t.Errorf("tools were touched: %+v", got.result)
	}
}

// The closing step names the command the write decided on, not the static
// text that is only true when Codex is aimed at the cluster by default.
func TestNextStepsNameTheProfileForAMembersOwnProvider(t *testing.T) {
	r := tools.Result{
		Written: []string{"Codex"},
		Steps:   map[string]tools.Step{"Codex": {Open: "codex --profile nan-glm53-flash", Next: "your own provider stays the default"}},
	}
	var b strings.Builder
	writeNextSteps(&b, "", r)
	got := b.String()
	if !strings.Contains(got, "codex --profile nan-glm53-flash") || strings.Contains(got, "already aimed") {
		t.Errorf("unexpected next steps:\n%s", got)
	}

	b.Reset()
	writeNextSteps(&b, "", tools.Result{Written: []string{"Codex"}})
	if !strings.Contains(b.String(), "already aimed at the cluster") {
		t.Errorf("the static step was lost:\n%s", b.String())
	}
}

package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/tools"
	"github.com/nxssie/nan-cli/internal/ui"
)

type setupStage int

const (
	setupOff setupStage = iota
	setupKey
	setupChecking
	setupTools
	setupApplying
	setupDone
)

// SkippableKey is whether the flow is on the first run's key step, where esc
// skips rather than cancels - so a footer can say so.
func (s Setup) SkippableKey() bool { return s.onboarding && s.stage == setupKey }

// Setup points the coding tools on this machine at NaN.
//
// Three steps, and the middle one is the point: the key is CHECKED against
// the cluster before a byte is written. A key that fails there is one
// message; the same key written into five configs is five tools that start,
// answer, and fail with a 401 somewhere deep in a session.
type Setup struct {
	stage setupStage
	form  *huh.Form
	key   string
	err   error

	// hint is what the key step says about the member's key on the
	// platform: that they have one and where to copy it from, or that they
	// have none yet.
	hint string
	// models is how many the cluster serves this key, once it is checked.
	models int
	// unchecked is a key that could not be checked because the cluster
	// could not be reached: let through, and said so.
	unchecked error
	// onboarding is the first run, straight after signing in, where the
	// screen has to explain why it is asking before it asks.
	onboarding bool
	// noTools is set when this machine has none of the supported tools, so
	// the summary can tell that apart from none being picked.
	noTools bool
	// guide is how the first run ends: how to get around the dashboard.
	guide Guide
	// skipped is a first run left with esc. It still ends on the guide:
	// somebody who skipped the key is exactly who is about to be left
	// alone with the dashboard.
	skipped bool

	// Result is what the last apply did.
	Result tools.Result

	// Key and Tools are what the parent should save once the flow is done.
	Key   string
	Tools map[string]bool
	// TourRequested is set when a first run closed on "take the tour".
	TourRequested bool
}

// Guide is what the dashboard hands a first run to end on.
type Guide struct {
	// Keys is a list of shortcuts, key and what it does, shown as is.
	Keys [][2]string
	// Tour offers a guided tour of the dashboard under the list. The
	// dashboard runs it; the flow only asks whether to.
	Tour bool
}

func (g Guide) empty() bool { return len(g.Keys) == 0 && !g.Tour }

type keyCheckedMsg struct {
	models int
	err    error
}

type appliedMsg struct{ result tools.Result }

// NewSetup returns a flow that is not running yet.
func NewSetup() Setup { return Setup{} }

// Active reports whether the flow owns the screen.
func (s Setup) Active() bool { return s.stage != setupOff }

// Finished reports whether it has something to report and is waiting to be
// dismissed.
func (s Setup) Finished() bool { return s.stage == setupDone }

// KeyHint is what the key step says about the member's key on the platform,
// from what the platform will tell about it: never the key itself, which is
// handed over once when it is created.
func KeyHint(status *api.KeyStatus) string {
	switch {
	case status == nil:
		return ""
	case status.Exists:
		name := status.Alias
		if name == "" {
			name = status.Name
		}
		if name != "" {
			return "Your account has a key (" + name + ") - copy it from cloud.nan.builders."
		}
		return "Your account has a key - copy it from cloud.nan.builders."
	}
	return "This account has no key yet - create one at cloud.nan.builders."
}

// Start opens the flow, pre-filled with whatever key is already saved so
// that re-running it is a confirmation rather than a retype.
func (s *Setup) Start(existing, hint string) tea.Cmd {
	*s = Setup{stage: setupKey, key: existing, hint: hint}
	s.form = keyForm(existing, hint, false)
	return s.form.Init()
}

// Onboard is Start for the first run: the same flow, opened straight after
// signing in when this machine has no key, with a screen that says why.
//
// Skippable - esc leaves it. A first run that cannot be dismissed turns "I
// only wanted to look around" into a dead end.
func (s *Setup) Onboard(hint string, guide Guide) tea.Cmd {
	*s = Setup{stage: setupKey, hint: hint, onboarding: true, guide: guide}
	s.form = keyForm("", hint, true)
	return s.form.Init()
}

// The keys the fields are read back by.
//
// By KEY, never by reading the field this form was bound to with Value(&x).
// Bubble Tea hands Update a copy of the model and keeps what it returns, so
// a pointer into one of the model's fields points into a copy that has
// already been discarded. The form itself is a pointer and survives the
// copying, so asking IT is the only reading that stays true. The Value
// binding below only seeds the field with what is already saved.
const (
	fieldKey   = "key"
	fieldTools = "tools"
)

func keyForm(existing, hint string, onboarding bool) *huh.Form {
	title := "Your NaN API key"
	desc := "Paste the key your tools will use. It is checked against the cluster before " +
		"anything is written, and it stays on this machine. If anyone has seen it, replace " +
		"it at cloud.nan.builders and paste the new one here."
	empty := "paste the key, or esc to leave"
	if onboarding {
		// Optional, and said so: the dashboard works without a key. What
		// the key is FOR is the part people skip past, so it comes first.
		title = "Signed in. One more step, if you want it: connect this machine"
		desc = "An API key lets this set up the coding tools on this machine - " +
			tools.List() + " - so they talk to NaN without you editing their " +
			"configs. Paste one: it is checked, and written into the ones you pick.\n\n" +
			"Not now? esc skips it. The dashboard works without a key, and you can " +
			"add one whenever you like: 6 Harnesses, or `nan setup`."
		empty = "paste the key, or esc to skip it for now"
	}
	if hint != "" {
		desc += "\n\n" + hint
	}
	value := existing
	return ui.NewForm(huh.NewGroup(
		huh.NewInput().
			Key(fieldKey).
			Title(title).
			Description(desc).
			Placeholder("paste your NaN API key here").
			// Dots, not the key: a terminal is shared and screenshotted.
			EchoMode(huh.EchoModePassword).
			CharLimit(512).
			Validate(func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("%s", empty)
				}
				return nil
			}).
			Value(&value),
	))
}

// toolForm is the second step: which of the installed tools to point at
// NaN. Unticking one is meaningful: it takes NaN back out of it.
func (s *Setup) toolForm(saved map[string]bool) tea.Cmd {
	installed := tools.Installed(tools.Detect())
	if len(installed) == 0 {
		// Nothing to configure is a finished state, not an error: the key is
		// good and there is simply no tool on this machine yet.
		s.stage = setupDone
		s.noTools = true
		s.Key = s.key
		return nil
	}

	options := make([]huh.Option[string], 0, len(installed))
	for _, st := range installed {
		label := st.Name
		if st.Configured {
			label += " · configured with NaN"
		}
		options = append(options, huh.NewOption(label, st.Name).Selected(tools.Enabled(saved, st.Name)))
	}

	s.stage = setupTools
	// A Picker rather than a bare MultiSelect, which shows no options at all
	// under a description - see ui.Picker.
	s.form = ui.NewForm(huh.NewGroup(
		ui.NewPicker(
			"Which tools should use NaN?",
			"Only the tools installed here are listed. Unticking one takes NaN back out of its config.",
			options,
		).Key(fieldTools),
	))
	return s.form.Init()
}

// Resize rewraps the open form, if there is one, for a terminal of w by h.
func (s *Setup) Resize(w, h int) { ui.FitForm(s.form, w, h) }

// leave is esc. Out of the flow, except on a first run with a guide to show,
// which ends on the guide instead. Nothing is saved either way.
func (s Setup) leave() Setup {
	if !s.onboarding || s.guide.empty() {
		return Setup{}
	}
	s.stage, s.skipped, s.Key, s.Tools = setupDone, true, "", nil
	s.err, s.models, s.unchecked = nil, 0, nil
	return s
}

// offersTour is whether this summary ends on the question "take the tour?".
func (s Setup) offersTour() bool { return s.onboarding && s.guide.Tour }

// AsksAboutTour is whether the screen is that question right now, so a
// footer can offer its two answers rather than keys that do nothing there.
func (s Setup) AsksAboutTour() bool { return s.stage == setupDone && s.offersTour() }

// Update drives the flow. `saved` is which tools are ticked, so the picker
// opens on what is true.
func (s Setup) Update(ctx context.Context, msg tea.Msg, saved map[string]bool) (Setup, tea.Cmd) {
	switch msg := msg.(type) {
	case keyCheckedMsg:
		if msg.err != nil && errors.Is(msg.err, api.ErrKeyRefused) {
			// Back to the key: one the cluster refused is almost always a
			// paste that lost a character, and writing it anywhere would
			// only move the failure into every tool.
			s.err = fmt.Errorf("the cluster refused this key - %w", msg.err)
			s.stage = setupKey
			s.form = keyForm("", s.hint, s.onboarding)
			return s, s.form.Init()
		}
		// A key that could not be checked because the cluster could not be
		// reached is let through: offline is not a wrong key, and refusing
		// it would leave somebody on a train unable to set up at all.
		s.err, s.unchecked, s.models = nil, msg.err, msg.models
		s.Key = s.key
		cmd := s.toolForm(saved)
		return s, cmd

	case appliedMsg:
		s.stage = setupDone
		s.Result = msg.result
		return s, nil

	case tea.KeyPressMsg:
		if s.stage == setupDone {
			// The dismissing key hands back what to save. The dashboard
			// saves once the flow is no longer Active, which is only after
			// this key.
			out := Setup{Key: s.Key, Tools: s.Tools, Result: s.Result}
			if !s.offersTour() {
				return out, nil // any key
			}
			// An offer is a question, so only its two answers close it.
			switch msg.String() {
			case "enter":
				out.TourRequested = true
				return out, nil
			case "s", "esc":
				return out, nil
			}
			return s, nil
		}
		if msg.String() == "esc" {
			return s.leave(), nil
		}
	}

	if s.form == nil || s.stage == setupChecking || s.stage == setupApplying {
		return s, nil
	}

	form, cmd := s.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		s.form = f
	}

	switch s.form.State {
	case huh.StateAborted:
		return s.leave(), nil
	case huh.StateCompleted:
		switch s.stage {
		case setupKey:
			s.key = strings.TrimSpace(s.form.GetString(fieldKey))
			s.stage = setupChecking
			return s, checkKey(ctx, s.key)
		case setupTools:
			chosen, _ := s.form.Get(fieldTools).([]string)
			s.Tools = tickedFrom(chosen)
			s.stage = setupApplying
			return s, applyTools(s.Key, s.Tools)
		}
	}
	return s, cmd
}

// tickedFrom is every installed tool, ticked or not. The unticked ones are
// saved too: a tool nobody has unticked is ticked, so forgetting one would
// tick it again on the next run.
func tickedFrom(chosen []string) map[string]bool {
	ticked := map[string]bool{}
	for _, st := range tools.Installed(tools.Detect()) {
		ticked[st.Name] = false
	}
	for _, name := range chosen {
		ticked[name] = true
	}
	return ticked
}

// checkKey asks the cluster which models the key can call, which is the only
// thing that can say a key is valid.
func checkKey(ctx context.Context, key string) tea.Cmd {
	return func() tea.Msg {
		models, err := api.ListModels(ctx, key)
		return keyCheckedMsg{models: len(models), err: err}
	}
}

// applyTools runs off the event loop. With Hermes in the list it is several
// processes and several seconds, and run inside a key handler that is a
// dashboard frozen with no repaint and no key accepted.
func applyTools(key string, ticked map[string]bool) tea.Cmd {
	return func() tea.Msg {
		return appliedMsg{result: tools.Apply(key, ticked)}
	}
}

// Label is what the loader should say for the step the flow is on.
func (s Setup) Label() string {
	switch s.stage {
	case setupChecking:
		return "Checking it against the cluster"
	case setupApplying:
		// Said because it is the slow one: Hermes is configured by running
		// it, not by writing a file.
		return "Writing the configs - a few seconds, Hermes is asked rather than written"
	default:
		return "Loading"
	}
}

// View renders the flow.
func (s Setup) View(indent, spinner string) string {
	var b strings.Builder

	switch s.stage {
	case setupKey, setupTools:
		if s.stage == setupTools {
			switch {
			case s.unchecked != nil:
				b.WriteString(indent + s.uncheckedLine() + "\n\n")
			case s.models > 0:
				b.WriteString(indent + ui.Success(fmt.Sprintf("Key accepted by the cluster · %d models", s.models)) + "\n\n")
			}
		}
		b.WriteString(s.form.View() + "\n")

	case setupChecking, setupApplying:
		b.WriteString(indent + spinner + "\n")

	case setupDone:
		s.viewDone(&b, indent)
	}

	if s.err != nil {
		b.WriteString("\n" + indent + ui.Error(s.err) + "\n")
	}
	return b.String()
}

func (s Setup) viewDone(b *strings.Builder, indent string) {
	if s.unchecked != nil {
		b.WriteString(indent + s.uncheckedLine() + "\n\n")
	}
	r := s.Result
	switch {
	case len(r.Written) > 0 || r.Removed > 0 || len(r.Failed) > 0:
		b.WriteString(indent + ui.Success(r.Summary()) + "\n")
	case s.Key != "":
		b.WriteString(indent + ui.Success("Key saved.") + "\n\n")
		// Two different situations, and saying the first when it is the
		// second tells somebody their tools are missing when they simply
		// did not tick any.
		if s.noTools {
			b.WriteString(indent + ui.Faint("None of "+tools.List()+" is installed on this machine yet.") + "\n")
		} else {
			b.WriteString(indent + ui.Faint("No tool was ticked, so nothing was written.") + "\n")
		}
	}
	if len(r.Written) > 0 {
		writeNextSteps(b, indent, r.Written)
	}
	writeFailures(b, indent, r.Failed)

	if s.skipped {
		b.WriteString(indent + ui.Value("Skipped: no key on this machine yet.") + "\n")
		b.WriteString(indent + ui.Faint("The tools here cannot reach NaN until they have one. Add it whenever") + "\n")
		b.WriteString(indent + ui.Faint("you like: 6 Harnesses, then enter - or run nan setup.") + "\n")
	}

	switch {
	case s.onboarding && len(s.guide.Keys) > 0:
		b.WriteString("\n" + indent + ui.Eyebrow("Getting around") + "\n\n")
		rows := make([][2]string, len(s.guide.Keys))
		for i, r := range s.guide.Keys {
			rows[i] = [2]string{ui.Value(r[0]), r[1]}
		}
		b.WriteString(ui.KVBlock(rows, indent) + "\n")
		if s.offersTour() {
			b.WriteString("\n" + indent + ui.Eyebrow("Quick tour") + "\n\n")
			b.WriteString(indent + "A minute through the dashboard, on your own data. Each step moves on\n")
			b.WriteString(indent + "when you do what it says.\n\n")
			b.WriteString(indent + ui.Accent("enter") + ui.Faint(" take the tour") + "     " +
				ui.Accent("s") + ui.Faint(" skip it") + "\n")
			b.WriteString(indent + ui.Faint("Press t in the dashboard to take it later.") + "\n")
		} else {
			b.WriteString("\n" + indent + ui.Faint("any key to start") + "\n")
		}
	default:
		b.WriteString("\n" + indent + ui.Faint("any key to go back") + "\n")
	}
}

func (s Setup) uncheckedLine() string {
	return ui.Warn("The key could not be checked (" + s.unchecked.Error() + "); it is used anyway.")
}

// writeNextSteps says how to use each tool just configured. "4 added"
// answers what was done and not the question a member is left holding: and
// now what.
func writeNextSteps(b *strings.Builder, indent string, written []string) {
	b.WriteString("\n" + indent + ui.Eyebrow("Now open them") + "\n\n")
	steps := map[string]tools.Tool{}
	for _, t := range tools.Registry() {
		steps[t.Name] = t
	}
	rows := make([][2]string, 0, len(written))
	for _, name := range written {
		if t, ok := steps[name]; ok && t.Open != "" {
			rows = append(rows, [2]string{ui.Mono(t.Open), t.NextStep})
		}
	}
	b.WriteString(ui.KVBlock(rows, indent) + "\n")
}

// writeFailures names each tool that could not be configured, by the first
// line of what went wrong, and what to do about it.
func writeFailures(b *strings.Builder, indent string, failed []tools.Failure) {
	if len(failed) == 0 {
		return
	}
	b.WriteString("\n")
	for _, f := range failed {
		b.WriteString(indent + ui.Error(fmt.Errorf("%s: %s", f.Tool, f.FirstLine())) + "\n")
	}
	b.WriteString(indent + ui.Faint("The rest went in. Fix that one and run setup again, or carry on without it -") + "\n")
	b.WriteString(indent + ui.Faint("its page on nan.builders has the manual steps.") + "\n")
}

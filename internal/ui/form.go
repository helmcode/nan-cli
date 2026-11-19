package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Forms and prompts, in the brand's colours.
//
// Everything a person types at this CLI goes through huh rather than through
// bufio.Scanner: the email at sign-in, the link pasted back, the key in
// Setup, the tools it applies to, and every confirmation before something
// irreversible. A hand-rolled prompt has no validation, no cursor, no way
// back, and reimplements badly what a form library already does — and it
// looks like a different program from the dashboard two commands away.

// FormTheme is huh dressed as Helmcode.
//
// Built on ThemeBase rather than ThemeCharm, because ThemeCharm is another
// brand's palette and the point is not to ship it. The focused field's thick
// left border is kept and coloured with the accent, which is exactly the
// brandbook's terminal-block treatment: a surface with a 3px accent rule down
// its left edge.
func FormTheme() huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		t := NewTheme(isDark)
		s := huh.ThemeBase(isDark)

		s.Focused.Base = s.Focused.Base.BorderForeground(t.Accent)
		s.Focused.Card = s.Focused.Base
		s.Focused.Title = lipgloss.NewStyle().Foreground(t.Text).Bold(true)
		s.Focused.Description = lipgloss.NewStyle().Foreground(t.TextDim)
		s.Focused.ErrorIndicator = s.Focused.ErrorIndicator.Foreground(t.Red)
		s.Focused.ErrorMessage = s.Focused.ErrorMessage.Foreground(t.Red)

		s.Focused.SelectSelector = s.Focused.SelectSelector.Foreground(t.AccentText)
		s.Focused.MultiSelectSelector = s.Focused.MultiSelectSelector.Foreground(t.AccentText)
		s.Focused.SelectedOption = lipgloss.NewStyle().Foreground(t.AccentText)
		s.Focused.SelectedPrefix = s.Focused.SelectedPrefix.Foreground(t.AccentText)
		s.Focused.UnselectedOption = lipgloss.NewStyle().Foreground(t.TextDim)
		s.Focused.UnselectedPrefix = s.Focused.UnselectedPrefix.Foreground(t.TextFaint)
		s.Focused.Option = lipgloss.NewStyle().Foreground(t.Text)

		s.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(t.Accent)
		s.Focused.TextInput.Placeholder = lipgloss.NewStyle().Foreground(t.TextFaint)
		s.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(t.AccentText)
		s.Focused.TextInput.Text = lipgloss.NewStyle().Foreground(t.Text)

		// The one filled moment: the button you are about to press.
		s.Focused.FocusedButton = s.Focused.FocusedButton.
			Foreground(lipgloss.Color("#0a0a0a")).Background(t.Accent)
		s.Focused.BlurredButton = s.Focused.BlurredButton.
			Foreground(t.TextDim).Background(t.Surface)

		// Blurred keeps the same shapes with the border hidden, so a field
		// does not move by a column when it takes focus.
		blurred := s.Focused
		blurred.Base = s.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
		blurred.Card = blurred.Base
		blurred.Title = lipgloss.NewStyle().Foreground(t.TextDim)
		blurred.TextInput.Prompt = lipgloss.NewStyle().Foreground(t.TextFaint)
		blurred.TextInput.Text = lipgloss.NewStyle().Foreground(t.TextDim)
		blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
		blurred.NextIndicator = lipgloss.NewStyle()
		blurred.PrevIndicator = lipgloss.NewStyle()
		s.Blurred = blurred

		return s
	})
}

// NewForm builds a form already wearing the theme and sized to the terminal,
// so no caller has to remember to do either. Help is off: every form here is
// one field, and the dashboard's own footer says what the keys do.
//
// The width is not cosmetic. huh leaves a field's text input at width zero
// unless the form is given one, and bubbles' text input draws only the first
// character of its placeholder at width zero - so "you@example.com" arrives
// as a lone "y" sitting under the cursor, which reads as a stray keystroke
// rather than as a hint, and the prompt stops saying what it wants.
//
// Nor is the height. huh sizes a group when it is built, before it has a
// width, so it measures every title and description as one line and then
// draws them wrapped: a description that wraps pushes the input out of the
// bottom of its own box. It measures again on a window-size message, and a
// form embedded in the dashboard never sees one. Fitting it here, once, is
// that message.
func NewForm(groups ...*huh.Group) *huh.Form {
	f := huh.NewForm(groups...).WithTheme(FormTheme()).WithShowHelp(false)
	w, h := TerminalSize()
	FitForm(f, w, h)
	return f
}

// FitForm resizes a form to a terminal of w by h, rewrapping its text.
//
// huh wraps titles and descriptions itself, at the width it is given, so the
// text is passed to it as plain sentences and this is all a resize takes.
// A dashboard calls it on every WindowSizeMsg while a form is up; a form run
// on its own gets that message from its own program.
func FitForm(f *huh.Form, w, h int) {
	if f == nil || w <= 0 || h <= 0 {
		return
	}
	f.WithWidth(FormWidthFor(w))
	f.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// Picker is a MultiSelect that keeps itself tall enough to show its options.
//
// Not optional. huh's MultiSelect, given a description and no height, draws
// the title and the description and none of the options: it sizes its list
// from the options alone, then takes the title and the description out of
// that. Setup's tool picker came out as a question with nothing to tick, in
// the dashboard and in `helmcode setup` alike.
//
// A fixed height is not the fix either, because the description wraps to a
// different number of lines at every width: too short and an option falls
// off the bottom, too tall and a gap opens under the last one. So the height
// is worked out again every time huh hands the field a new width, which is
// the one moment the answer changes.
type Picker struct {
	*huh.MultiSelect[string]
	title, description string
	options            int
}

// NewPicker builds a Picker. Key and Value are on it rather than left to the
// embedded field, whose own versions return the bare MultiSelect and would
// drop the wrapper on the floor.
func NewPicker(title, description string, options []huh.Option[string]) *Picker {
	return &Picker{
		MultiSelect: huh.NewMultiSelect[string]().
			Title(title).
			Description(description).
			Options(options...),
		title:       title,
		description: description,
		options:     len(options),
	}
}

// Key sets the key the form reads the answer back by.
func (p *Picker) Key(key string) *Picker {
	p.MultiSelect.Key(key)
	return p
}

// Value binds the answer to v.
func (p *Picker) Value(v *[]string) *Picker {
	p.MultiSelect.Value(v)
	return p
}

// WithWidth is where huh tells the field its width, so it is where the
// height is settled.
func (p *Picker) WithWidth(width int) huh.Field {
	p.Height(ChoiceHeight(p.title, p.description, p.options, width))
	p.MultiSelect.WithWidth(width)
	return p
}

// Update keeps the wrapper: huh stores whatever a field's Update returns, and
// the embedded one returns itself.
func (p *Picker) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	_, cmd := p.MultiSelect.Update(msg)
	return p, cmd
}

// ChoiceHeight is the height a MultiSelect needs at a given field width: its
// title and its description as huh will wrap them, and one row per option.
// Measured, that is exactly it, with no margin.
func ChoiceHeight(title, description string, options, width int) int {
	text := width - FormTheme().Theme(true).Focused.Base.GetHorizontalFrameSize()
	if text < 1 {
		text = 1
	}
	h := options
	for _, s := range []string{title, description} {
		if s != "" {
			// The same call, with the same break characters, huh wraps with.
			h += lipgloss.Height(lipgloss.Wrap(s, text, ",.-; "))
		}
	}
	return h
}

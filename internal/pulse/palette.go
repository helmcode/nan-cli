package pulse

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"github.com/nxssie/nan-cli/internal/ui"
)

// The palette: one box that finds anything.
//
// Type what you are thinking of - "glm", "costs", "setup" - and the thing
// itself comes back, whatever kind it is. Actions live in the same list, so
// "setup" finds the setup and nobody has to learn a key for something they
// do once a month.

// action is something the palette does rather than somewhere it goes.
type action int

const (
	actNone action = iota
	actSetup
	actSignIn
	actSignOut
	actReload
	actQuit
)

// entry is one thing that can be found.
type entry struct {
	kind  string // what it is, shown as a tag: model, area, do
	name  string // what it is called, and what is matched
	hint  string // one faint fact, so two similar names can be told apart
	open  target
	in    area
	do    action
	match string // name plus kind, lower-cased, for matching
}

// entries is everything the palette can find.
func (m model) entries() []entry {
	s := m.snap
	var out []entry
	add := func(e entry) {
		e.match = strings.ToLower(e.name + " " + e.kind)
		out = append(out, e)
	}
	for _, mi := range s.sortedModels() {
		hint := mi.kind
		if s.usage != nil {
			if n := tokensIn(s.usage.Last30d, mi.id); n > 0 {
				hint += " · " + ui.Tokens(n) + " tokens in 30 days"
			}
		}
		add(entry{kind: "model", name: mi.id, hint: hint, open: target{kind: cardModel, id: mi.id}, in: areaModels})
	}
	for a := area(0); a < areaCount; a++ {
		add(entry{kind: "area", name: a.title(), in: a})
	}
	add(entry{kind: "do", name: "Set up your tools", hint: "API key and tools", do: actSetup})
	if m.session.SignedIn() {
		add(entry{kind: "do", name: "Sign out", hint: "also takes the key out of the tools", do: actSignOut})
	} else {
		add(entry{kind: "do", name: "Sign in", hint: "a link goes to your email", do: actSignIn})
	}
	add(entry{kind: "do", name: "Reload everything", do: actReload})
	add(entry{kind: "do", name: "Quit", do: actQuit})
	return out
}

// palette is the open palette: a text input and what it matches.
type palette struct {
	input   textinput.Model
	all     []entry
	shown   []entry
	cursor  int
	visible bool
}

func newPalette() palette {
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "a model, an area, or something to do"
	t := ui.Current()
	st := in.Styles()
	st.Focused.Prompt = lipgloss.NewStyle().Foreground(t.AccentText).Bold(true)
	st.Focused.Text = lipgloss.NewStyle().Foreground(t.Text)
	st.Focused.Placeholder = lipgloss.NewStyle().Foreground(t.TextFaint)
	in.SetStyles(st)
	return palette{input: in}
}

// open shows the palette over s, empty.
func (p *palette) open(all []entry) {
	p.all = all
	p.input.SetValue("")
	p.input.Focus()
	p.visible = true
	p.filter()
}

func (p *palette) close() {
	p.visible = false
	p.input.Blur()
}

// filter recomputes what the query matches. Fuzzy, so "glm" finds glm5.3 and
// "mfig" finds marta.figueroa; an empty query lists everything as it came.
func (p *palette) filter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		p.shown = p.all
	} else {
		words := make([]string, len(p.all))
		for i, e := range p.all {
			words[i] = e.match
		}
		matches := fuzzy.Find(q, words)
		p.shown = make([]entry, 0, len(matches))
		for _, m := range matches {
			p.shown = append(p.shown, p.all[m.Index])
		}
	}
	if p.cursor >= len(p.shown) {
		p.cursor = max(len(p.shown)-1, 0)
	}
}

// move steps the cursor, wrapping.
func (p *palette) move(delta int) {
	if len(p.shown) == 0 {
		return
	}
	p.cursor = (p.cursor + delta + len(p.shown)) % len(p.shown)
}

// selected is the entry under the cursor.
func (p palette) selected() (entry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.shown) {
		return entry{}, false
	}
	return p.shown[p.cursor], true
}

package pulse

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// The keymap. The verbs are one letter each and mean the same thing in every
// area; which of them the footer offers depends on where you are, read from
// the same list the menu and the handler use.
//
// s signs in and o signs out, as they always have here; e opens Setup from
// anywhere, because "paste your key" is what a member is told to do from
// whichever area they are looking at.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Enter    key.Binding
	Back     key.Binding
	Area     key.Binding
	NextArea key.Binding
	PrevArea key.Binding
	Find     key.Binding
	Reload   key.Binding
	SignIn   key.Binding
	SignOut  key.Binding
	Setup    key.Binding
	Tour     key.Binding
	Help     key.Binding
	Quit     key.Binding

	Copy key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "move")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		Enter:    key.NewBinding(key.WithKeys("enter", "right", "l"), key.WithHelp("enter", "actions")),
		Back:     key.NewBinding(key.WithKeys("esc", "left", "h"), key.WithHelp("esc", "back")),
		Area:     key.NewBinding(key.WithKeys("1", "2", "3", "4", "5", "6", "7"), key.WithHelp("1-7", "areas")),
		NextArea: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next area")),
		PrevArea: key.NewBinding(key.WithKeys("shift+tab")),
		Find:     key.NewBinding(key.WithKeys("/", "ctrl+k"), key.WithHelp("/", "find")),
		Reload:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
		SignIn:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sign in")),
		SignOut:  key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "sign out")),
		Setup:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "set up")),
		Tour:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "tour")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "all keys")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		Copy: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy")),
	}
}

// verbFor is the verb a key press asks for, if any. e is not here: it is
// Setup from anywhere, a global key rather than a row's verb.
func (k keyMap) verbFor(msg tea.KeyPressMsg) verb {
	if key.Matches(msg, k.Copy) {
		return verbCopy
	}
	return verbNone
}

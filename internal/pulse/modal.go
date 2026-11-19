package pulse

import (
	"charm.land/bubbles/v2/key"
)

// Verbs, and the modals they open.
//
// One letter per verb and the same letter everywhere, the way lazygit does
// it. Enter on a row opens a menu of exactly what that row allows, with the
// letters beside them, so the letters are learned by using the menu rather
// than by reading about them.

type verb int

const (
	verbNone verb = iota
	verbDetails
	verbEdit
	verbCopy
)

// menuItem is a line in the enter menu.
type menuItem struct {
	label string
	key   string
	verb  verb
}

// verbs is what can be done in an area, on its selected row. The one list
// the menu, the footer and the key handler all read, so a shortcut that is
// offered and a shortcut that works are the same thing.
func (m model) verbs(a area, r *row) []menuItem {
	var out []menuItem
	add := func(label, k string, v verb) { out = append(out, menuItem{label: label, key: k, verb: v}) }

	switch a {
	case areaUsage, areaModels:
		if r != nil {
			add("Details", "enter", verbDetails)
			add("Copy the id", "y", verbCopy)
		}
	case areaHarness:
		add("Set up the tools", "e", verbEdit)
	case areaHome:
		if r != nil && r.newModel != "" {
			add("Add it to your tools", "e", verbEdit)
		}
	}
	return out
}

// has reports whether a verb is available here.
func (m model) has(v verb) bool {
	for _, it := range m.verbs(m.area, m.selectedRow()) {
		if it.verb == v {
			return true
		}
	}
	return false
}

// ── modals ───────────────────────────────────────────────────────────────

type modalKind int

const (
	modalNone modalKind = iota
	modalMenu
	modalLink
)

type modal struct {
	kind modalKind

	// menu
	items  []menuItem
	cursor int

	// link
	url    string
	title  string
	note   string
	copied bool
}

func (m *model) openMenu() {
	items := m.verbs(m.area, m.selectedRow())
	if len(items) == 0 {
		return
	}
	m.modal = modal{kind: modalMenu, items: items}
}

// menuKeys are the keys inside the enter menu.
var menuKeys = struct{ up, down, pick, close key.Binding }{
	up:    key.NewBinding(key.WithKeys("up", "k")),
	down:  key.NewBinding(key.WithKeys("down", "j")),
	pick:  key.NewBinding(key.WithKeys("enter")),
	close: key.NewBinding(key.WithKeys("esc", "q")),
}

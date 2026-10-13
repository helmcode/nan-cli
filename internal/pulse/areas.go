package pulse

import (
	"fmt"
	"sort"

	"github.com/nxssie/nan-cli/internal/ui"
)

// Areas: the inventory, always on screen.
//
// Every management TUI people keep open - lazygit, lazydocker, k9s - answers
// "what is there, and where" the same way: the list of places is the
// navigation, it is always visible, and each place has a number. So here:
// seven areas in a row of tabs across the top, 1 to 7, as nan has always
// had them, and whatever is selected underneath.

type area int

const (
	areaHome area = iota
	areaProfile
	areaUsage
	areaModels
	areaCosts
	areaHarness
	areaAbout
	areaCount
)

func (a area) title() string {
	return [...]string{"Home", "Profile", "Usage", "Models", "Costs", "Harnesses", "About"}[a]
}

// needsSession is whether an area is empty without the email sign-in.
// Models is not: a key alone can ask the cluster what it serves.
func (a area) needsSession() bool {
	return a == areaProfile || a == areaUsage || a == areaCosts
}

// row is one selectable line in an area: what it shows, what it is, and
// what can be done to it.
type row struct {
	cells []string
	// id identifies the thing, so a selection survives a reload and a
	// signal can land on it.
	id string
	// what the row is: at most one of these is set.
	model    string
	field    string
	sig      *signal
	quick    *quickAction
	newModel string
	// dim draws the row faint: a model with no traffic.
	dim bool
}

// quickAction is a Home shortcut into another area, or into something to do.
type quickAction struct {
	label string
	hint  string
	in    area
	do    action
}

// columns are an area's table headings and their share of the width.
type column struct {
	title string
	// width is a fixed width, or 0 to take what is left.
	width int
	right bool
}

func (a area) columns() []column {
	switch a {
	case areaProfile:
		return []column{{title: "Field", width: 22}, {title: "Value"}}
	case areaUsage:
		return []column{{title: "Model"}, {title: "24 hours", width: 9, right: true}, {title: "30 days", width: 9, right: true}, {title: "All time", width: 9, right: true}, {title: "Share · 30 days", width: 18}}
	case areaModels:
		return []column{{title: "Model"}, {title: "Kind", width: 20}, {title: "30 days", width: 9, right: true}, {title: "24 hours", width: 9, right: true}}
	}
	return nil
}

// rows builds an area's selectable lines from the snapshot.
func (m model) rows(a area) []row {
	s := m.snap
	switch a {
	case areaHome:
		var out []row
		for i := range m.sigs {
			sg := m.sigs[i]
			out = append(out, row{id: "sig:" + sg.say, sig: &sg})
		}
		for _, name := range m.newModels {
			out = append(out, row{id: "new:" + name, newModel: name})
		}
		for _, q := range m.quickActions() {
			out = append(out, row{id: "do:" + q.label, quick: &q})
		}
		return out

	case areaProfile:
		keys := make([]string, 0, len(s.me))
		for k, v := range s.me {
			// A nested object is the platform's own structure, not a fact
			// about the member; it would print as a Go map.
			if _, nested := v.(map[string]any); nested {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]row, 0, len(keys))
		for _, k := range keys {
			out = append(out, row{id: "field:" + k, field: k, cells: []string{humanKey(k), censor(k, fmt.Sprintf("%v", s.me[k]))}})
		}
		return out

	case areaUsage:
		if s.usage == nil {
			return nil
		}
		total30 := float64(s.usage.Last30d.TotalTokens)
		var out []row
		for _, name := range s.usedModels() {
			t24, t30, all := tokensIn(s.usage.Last24h, name), tokensIn(s.usage.Last30d, name), tokensIn(s.usage.AllTime, name)
			share := ""
			if total30 > 0 && t30 > 0 {
				f := t30 / total30
				share = ui.Bar(f*m.reveal, 10, false) + " " + ui.Percent(f)
			}
			out = append(out, row{
				id: name, model: name,
				cells: []string{name, tokensOrDash(t24), tokensOrDash(t30), tokensOrDash(all), share},
				dim:   t30 == 0,
			})
		}
		return out

	case areaModels:
		var last24, last30 float64
		var out []row
		for _, mi := range s.sortedModels() {
			if s.usage != nil {
				last24, last30 = tokensIn(s.usage.Last24h, mi.id), tokensIn(s.usage.Last30d, mi.id)
			}
			out = append(out, row{
				id: mi.id, model: mi.id,
				cells: []string{mi.id, kindBadge(mi.kind), tokensOrDash(last30), tokensOrDash(last24)},
				dim:   last30 == 0 && last24 == 0,
			})
		}
		return out
	}
	return nil
}

func tokensOrDash(n float64) string {
	if n == 0 {
		return "-"
	}
	return ui.Tokens(n)
}

// quickActions are Home's shortcuts, for what people come here to do.
func (m model) quickActions() []quickAction {
	var out []quickAction
	if !m.session.SignedIn() {
		out = append(out, quickAction{label: "Sign in", hint: "s, a link goes to your email", do: actSignIn})
	}
	out = append(out,
		quickAction{label: "Set up your tools", hint: "e, the key and the tools that use it", in: areaHarness, do: actSetup},
		quickAction{label: "See what you have spent", hint: "3", in: areaUsage},
		quickAction{label: "Compare it with other providers", hint: "5", in: areaCosts},
	)
	return out
}

// areaOf is where a signal's or a search result's target lives.
func areaOf(t target) area {
	switch t.kind {
	case cardModel:
		return areaModels
	case cardUsage:
		return areaUsage
	case cardHarness:
		return areaHarness
	case cardProfile:
		return areaProfile
	}
	return areaHome
}

// count is the number shown beside an area in its tab.
func (m model) count(a area) string {
	s := m.snap
	switch a {
	case areaHome:
		if n := len(m.sigs); n > 0 {
			return fmt.Sprint(n)
		}
	case areaModels:
		if n := len(s.models); n > 0 {
			return fmt.Sprint(n)
		}
	case areaHarness:
		installed := s.installedTools()
		configured := 0
		for _, t := range installed {
			if t.Configured {
				configured++
			}
		}
		if len(installed) > 0 {
			return fmt.Sprintf("%d/%d", configured, len(installed))
		}
	}
	return ""
}

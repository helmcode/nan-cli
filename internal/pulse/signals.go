package pulse

import (
	"errors"
	"sort"
	"strings"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/build"
	"github.com/helmcode/nan-cli/internal/runs"
	"github.com/helmcode/nan-cli/internal/tools"
)

// Signals: what needs the member, worked out from the snapshot.
//
// Home leads with these because a fresh machine used to land on a page that
// explained the arrow keys and then answered every other area with
// `unauthorized`: nothing said "sign in", and the command that does it is a
// subcommand you have to already know about. Each signal says what is wrong
// in a line and goes, on enter, to where it is fixed.

type severity int

const (
	sevInfo severity = iota
	sevWarn
	sevAlarm
)

// target is something a signal or a search lands on.
type target struct {
	kind cardKind
	id   string
}

type signal struct {
	sev  severity
	say  string
	goTo target
}

// signals is every signal in the snapshot, the worst first.
func (m model) signals() []signal {
	s := m.snap
	var out []signal
	add := func(sev severity, say string, to target) {
		out = append(out, signal{sev: sev, say: say, goTo: to})
	}

	// A load that failed is said first and as what to do, with enter to
	// try again; the areas it left empty say the same.
	switch {
	case errors.Is(s.err, api.ErrUnreachable):
		add(sevAlarm, "Cannot reach nan.builders: check the connection", target{kind: cardReload})
	case s.err != nil:
		add(sevWarn, "Part of this did not load: "+runs.SanitizeLine(s.err.Error()), target{kind: cardReload})
	}

	switch {
	case m.expiredNotice:
		add(sevAlarm, "Your session has expired: press s to sign in again", target{kind: cardSignIn})
	case !m.session.SignedIn():
		add(sevWarn, "Not signed in: Profile, Usage and Costs stay empty until you are", target{kind: cardSignIn})
	}

	switch {
	case m.session.APIKey == "":
		add(sevWarn, "No API key on this machine: the tools here cannot reach NaN", target{kind: cardHarness})
	case s.keyRefused:
		add(sevAlarm, "The cluster refused the API key on this machine: paste the current one", target{kind: cardHarness})
	default:
		var waiting []string
		for _, t := range s.installedTools() {
			if !t.Configured && tools.Enabled(m.session.EnabledTools, t.Name) {
				waiting = append(waiting, t.Name)
			}
		}
		if len(waiting) > 0 {
			add(sevInfo, strings.Join(waiting, ", ")+" "+plural(len(waiting), "is", "are")+" installed and not pointed at NaN", target{kind: cardHarness})
		}
	}

	// A membership that ended keeps its apps and data until a date. Profile
	// says when; Home is where it is noticed before the day comes.
	if s.me != nil && s.me.GraceUntil != "" {
		add(sevWarn, "Your membership has ended: apps and data are kept until "+day(s.me.GraceUntil), target{kind: cardProfile})
	}

	if v := m.upd.Available(); v != "" {
		add(sevWarn, build.Name+" "+v+" is out; you have "+build.Version, target{kind: cardUpdate})
	}

	sort.SliceStable(out, func(a, b int) bool { return out[a].sev > out[b].sev })
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

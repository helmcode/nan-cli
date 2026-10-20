package pulse

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/nxssie/nan-cli/internal/ui"
)

// The tour: a minute through the dashboard, on the member's own data. Each
// step says one thing to do and moves on when it is done, so the keys are
// learned by pressing them rather than by reading about them.

type tourStep struct {
	say  string
	done func(m model) bool
}

var tourSteps = []tourStep{
	{
		say:  "Press tab to go to the next area",
		done: func(m model) bool { return m.area != areaHome },
	},
	{
		say:  "Press 4 to jump straight to Models",
		done: func(m model) bool { return m.area == areaModels },
	},
	{
		say:  "Move with ↓, then press enter to see what you can do with that row",
		done: func(m model) bool { return m.modal.kind == modalMenu },
	},
	{
		say:  "esc closes it. Now press / and type a few letters of anything",
		done: func(m model) bool { return m.pal.visible && m.pal.input.Value() != "" },
	},
	{
		say:  "enter goes to it. Then press 1 to come back Home",
		done: func(m model) bool { return m.area == areaHome && !m.pal.visible },
	},
}

// tourFinished is the step after the last: the closing line.
var tourFinished = len(tourSteps) + 1

func (m model) touring() bool { return m.tour > 0 }

func (m *model) startTour() {
	m.goTo(areaHome)
	m.tour = 1
}

func (m *model) stopTour() { m.tour = 0 }

// advanceTour moves on when the current step has been done.
func (m *model) advanceTour() {
	if m.tour > 0 && m.tour <= len(tourSteps) && tourSteps[m.tour-1].done(*m) {
		m.tour++
	}
}

func (m model) renderTour() string {
	t := ui.Current()
	badge := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(t.Accent).Bold(true).Padding(0, 1)
	say := lipgloss.NewStyle().Foreground(t.Text)
	if m.tour >= tourFinished {
		return badge.Render("TOUR") + "  " +
			say.Render("Done. ? shows every key, t takes the tour again.") + "   " + ui.Faint("any key to close")
	}
	return badge.Render(fmt.Sprintf("TOUR %d/%d", m.tour, len(tourSteps))) + "  " +
		say.Render(tourSteps[m.tour-1].say) + "   " + ui.Accent("x") + ui.Faint(" skip tour")
}

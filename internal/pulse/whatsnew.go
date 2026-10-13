package pulse

import (
	"time"

	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/update"
)

// What changed since the last run: a model the key can call that it could
// not before, and a newer release of this CLI.

// takeNote compares a load with what was remembered: new models are found,
// and the latest release recorded.
func (m *model) takeNote(s snapshot) {
	if !s.keyRefused && len(s.models) > 0 {
		if found := m.upd.SeeModels(s.modelIDs()); len(found) > 0 {
			m.newModels = append([]string(nil), found...)
		}
	}
	if s.askedRelease {
		m.upd.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		if s.release != nil {
			m.upd.Latest = s.release.Version
		}
	}
	_ = m.upd.Save()
}

// seeNewModels is the new models having been looked at: they stay marked
// for this visit and are not announced again next time.
func (m *model) seeNewModels() {
	if len(m.newModels) == 0 {
		return
	}
	m.upd.MarkSeen()
	_ = m.upd.Save()
}

// updateModal shows the command that updates this copy, to copy and run.
func (m *model) updateModal() {
	v := m.upd.Available()
	if v == "" {
		return
	}
	m.modal = modal{
		kind:  modalLink,
		title: "Update to " + v,
		url:   update.Command(),
		note:  "You have " + build.Version + ". Run this in a terminal, then start " + build.Name + " again.",
	}
}

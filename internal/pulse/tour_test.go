package pulse

import (
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/update"
)

// footer is the last line of the screen.
func footer(m model) string { return lastLines(screen(m), 1) }

// The tour is taken by doing what each step says: every step moves on when
// its key is pressed, the footer says the step, and the last one closes on
// the next key.
func TestTheTourMovesOnAsEachStepIsDone(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m = press(t, m, keyOf("t"))
	keys := [][]string{
		{"tab"},           // 1: the next area
		{"4"},             // 2: Models
		{"enter"},         // 3: a row's menu
		{"esc", "/", "g"}, // 4: find, typed
		{"enter", "1"},    // 5: back Home
	}
	for i, step := range keys {
		if !strings.Contains(footer(m), tourSteps[i].say[:20]) {
			t.Fatalf("step %d: the footer does not say it:\n%s", i+1, footer(m))
		}
		for _, k := range step {
			m = press(t, m, keyOf(k))
		}
		if m.tour != i+2 {
			t.Fatalf("after doing step %d the tour is at %d", i+1, m.tour)
		}
	}
	if !strings.Contains(footer(m), "Done") {
		t.Errorf("the tour does not close with its last line:\n%s", footer(m))
	}
	m = press(t, m, keyOf("down"))
	if m.touring() {
		t.Error("the closing line stayed after the next key")
	}
}

// x leaves the tour at any step, except while typing a search, where it is
// a letter.
func TestXSkipsTheTourButNotWhileSearching(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m = press(t, m, keyOf("t"))
	m = press(t, m, keyOf("x"))
	if m.touring() {
		t.Error("x did not skip the tour")
	}

	m = press(t, m, keyOf("t"))
	m.tour = 4
	m = press(t, m, keyOf("/"))
	m = press(t, m, keyOf("x"))
	if !m.touring() || m.pal.input.Value() != "x" {
		t.Errorf("x while searching: touring=%v, typed %q", m.touring(), m.pal.input.Value())
	}
}

// A model the key could not call before is said on Home, once: looking at
// it marks it seen, and the next load does not announce it again.
func TestANewModelIsAnnouncedOnce(t *testing.T) {
	m, _ := demoModel(t, 120, 40)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m.upd = update.State{}

	first := demoSnapshot()
	m.takeNote(first) // the first look records, and announces nothing
	if len(m.newModels) != 0 {
		t.Fatalf("the first look announced %v", m.newModels)
	}

	next := demoSnapshot()
	next.models = append(next.models, modelInfo{id: "glm6-preview", kind: "chat"})
	m.takeNote(next)
	m.snap = next
	m.sigs = m.signals()
	if out := screen(press(t, m, keyOf("1"))); !strings.Contains(out, "glm6-preview") {
		t.Errorf("Home does not show the new model:\n%s", out)
	}

	m.seeNewModels()
	again := update.Load()
	if got := again.SeeModels(next.modelIDs()); len(got) != 0 {
		t.Errorf("a model already seen is announced again: %v", got)
	}
}

// A newer release is remembered from the load and its card gives the
// command that updates.
func TestANewerReleaseOffersTheCommand(t *testing.T) {
	m, _ := demoModel(t, 120, 40)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m.upd = update.State{}
	s := demoSnapshot()
	s.askedRelease = true
	s.release = &api.Release{Version: "99.0.0"}
	m.takeNote(s)
	if m.upd.Available() != "99.0.0" || m.upd.CheckedAt == "" {
		t.Fatalf("the release was not recorded: %+v", m.upd)
	}
	m.updateModal()
	if m.modal.url != update.Command() || !strings.Contains(m.modal.note, build.Version) {
		t.Errorf("the update card is %+v", m.modal)
	}
}

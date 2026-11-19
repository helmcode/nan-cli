package pulse

import (
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

// lastLines is the bottom of the screen, where the footer is.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// At the smallest size the dashboard draws, the footer is cut from the
// right. `? all keys` and `q quit` sat at its end and were what went, on
// every area, so the way to every other key was never on screen.
func TestTheFooterKeepsHelpAndQuitAtTheMinimumWidth(t *testing.T) {
	for _, area := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		m, _ := demoModel(t, minW, minH)
		m = press(t, m, keyOf(area))
		foot := lastLines(screen(m), 1)
		for _, want := range []string{"? all keys", "q quit"} {
			if !strings.Contains(foot, want) {
				t.Errorf("area %s at %dx%d: the footer %q lacks %q", area, minW, minH, foot, want)
			}
		}
	}
}

// `?` used to add three keys to the same single line, past its cut, so it
// changed nothing on screen. It lays every key out on as many lines as it
// takes.
func TestHelpShowsTheKeysTheLineHadNoRoomFor(t *testing.T) {
	m, _ := demoModel(t, minW, minH)
	m = press(t, m, keyOf("?"))
	foot := lastLines(screen(m), 3)
	for _, want := range []string{"r reload", "t tour", "1-7", "q quit"} {
		if !strings.Contains(foot, want) {
			t.Errorf("with ? the footer %q lacks %q", foot, want)
		}
	}
}

// What Harnesses is opened for is which tools here already use NaN. The list
// came after a four-step explanation and fell off the bottom of an 80x24
// terminal, empty state included.
func TestHarnessesShowsTheToolsOnASmallTerminal(t *testing.T) {
	m, _ := demoModel(t, 80, 24)
	m = press(t, m, keyOf("6"))
	out := screen(m)
	if !strings.Contains(out, "O N   T H I S   M A C H I N E") {
		t.Fatalf("the tool list is not on an 80x24 screen:\n%s", out)
	}
	// The demo has no tool on PATH, so the empty state is what shows.
	if !strings.Contains(out, "None of the supported tools is installed") {
		t.Errorf("the empty state is not on an 80x24 screen:\n%s", out)
	}
}

// A form inside the dashboard sits at the margin like everything around it.
// It was drawn from column 0, its rule flush with the terminal's edge.
func TestFormsSitAtTheMargin(t *testing.T) {
	m, _ := demoModel(t, 80, 24)
	m = press(t, m, keyOf("e"))
	seen := false
	for _, l := range strings.Split(screen(m), "\n") {
		if strings.Contains(l, "┃") {
			seen = true
			if !strings.HasPrefix(l, "  ┃") {
				t.Errorf("a form line is not at the margin: %q", l)
			}
		}
	}
	if !seen {
		t.Fatal("no form on screen after e")
	}
}

// Model names go into table cells through Truncate and Pad, which keep
// escape sequences, so they are cleaned when they arrive.
func TestUsageModelNamesLoseEscapeSequences(t *testing.T) {
	u := cleanUsage(&api.Usage{Last30d: api.Period{ByModel: []api.ModelUsage{{Model: "glm\x1b[2J\x1b]0;owned\x07"}}}})
	if got := u.Last30d.ByModel[0].Model; strings.ContainsRune(got, 0x1b) {
		t.Errorf("a usage model name kept an escape: %q", got)
	}
}

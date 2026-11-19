package pulse

import (
	"fmt"
	"strings"
	"testing"

	"github.com/helmcode/nan-cli/internal/api"
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

// With the network down, Home said `dial tcp: lookup nan.builders: no such
// host` above "Nothing needs you right now". It says what is wrong and what
// to do, enter tries again, and the areas the load left empty say the same.
func TestANetworkThatDidNotAnswerIsSaidAsWhatToDo(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m.snap.err = fmt.Errorf("%w: dial tcp: lookup nan.builders: no such host", api.ErrUnreachable)
	m.snap.usage, m.snap.models = nil, nil
	m.sigs = m.signals()

	home := screen(m)
	if !strings.Contains(home, "Cannot reach nan.builders") || !strings.Contains(home, "try again") {
		t.Errorf("Home does not say the network is down and how to retry:\n%s", home)
	}
	if strings.Contains(home, "dial tcp") || strings.Contains(home, "Nothing needs you") {
		t.Errorf("Home shows the Go error or says all is well:\n%s", home)
	}
	for _, area := range []string{"3", "4"} {
		if out := screen(press(t, m, keyOf(area))); !strings.Contains(out, "cannot reach nan.builders") {
			t.Errorf("area %s is empty with nothing said:\n%s", area, out)
		}
	}

	// enter on the signal loads again.
	if m.sigs[0].goTo.kind != cardReload {
		t.Fatalf("the first signal goes to %v, not a reload", m.sigs[0].goTo)
	}
	if _, cmd := m.follow(m.sigs[0].goTo); cmd == nil {
		t.Error("enter on the network signal did not try again")
	}
}

// A membership that ended keeps its apps and data until a date; Profile says
// when, and Home is where it is seen before the day comes.
func TestAnEndedMembershipIsOnHome(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	if out := screen(m); !strings.Contains(out, "Your membership has ended") || !strings.Contains(out, "20 Nov 2026") {
		t.Errorf("Home does not say the membership ended:\n%s", out)
	}
	m.snap.me.GraceUntil = ""
	m.sigs = m.signals()
	if out := screen(m); strings.Contains(out, "membership has ended") {
		t.Errorf("a live membership is said to have ended:\n%s", out)
	}
}

// The part before the @ is cut short, as before; the domain stays, because
// it is what says which account this is.
func TestProfileKeepsTheEmailDomain(t *testing.T) {
	for in, want := range map[string]string{
		"someone@example.com": "someon...@example.com",
		"ab@example.com":      "...@example.com",
		"no-at-sign":          "no-at-...",
	} {
		if got := shortenEmail(in); got != want {
			t.Errorf("shortenEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

// Below the size the dashboard draws at, it says how to get out, and q does.
func TestATerminalTooSmallSaysHowToQuit(t *testing.T) {
	m, _ := demoModel(t, 60, 20)
	out := screen(m)
	if !strings.Contains(out, "q quits") || strings.Contains(out, "...") {
		t.Errorf("the too-small screen:\n%s", out)
	}
	m = press(t, m, keyOf("q"))
	if !m.quitting {
		t.Error("q does not quit a terminal that is too small")
	}
}

// An alarm and a warning on Home differ by shape, not only by colour.
func TestSignalSeveritiesDifferWithoutColour(t *testing.T) {
	m, _ := demoModel(t, 120, 40)
	m.snap.keyRefused = true // an alarm; the ended membership is a warning
	m.sigs = m.signals()
	var alarm, warn string
	for _, l := range strings.Split(screen(m), "\n") {
		switch {
		case strings.Contains(l, "refused the API key"):
			alarm = l
		case strings.Contains(l, "membership has ended"):
			warn = l
		}
	}
	if !strings.Contains(alarm, sevMark[sevAlarm]) || !strings.Contains(warn, sevMark[sevWarn]) || sevMark[sevAlarm] == sevMark[sevWarn] {
		t.Errorf("alarm %q and warning %q are not told apart by shape", alarm, warn)
	}
}

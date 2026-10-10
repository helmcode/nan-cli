package runs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

func TestSanitizeStripsEscapesAndControls(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain text untouched", "hello\n\tworld", "hello\n\tworld"},
		{"colour", "\x1b[31mred\x1b[0m", "red"},
		{"cursor movement", "a\x1b[2Ab\x1b[Kc", "abc"},
		{"window title (OSC, BEL)", "x\x1b]0;pwned\x07y", "xy"},
		{"clipboard write (OSC 52, ST)", "x\x1b]52;c;ZXZpbA==\x1b\\y", "xy"},
		{"hyperlink", "\x1b]8;;http://evil\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"unterminated OSC eats the rest", "ok\x1b]0;title", "ok"},
		{"two-byte escape", "a\x1bcb", "ab"},
		{"lone ESC at the end", "a\x1b", "a"},
		{"carriage return rewrites the line", "safe\rEVIL", "safeEVIL"},
		{"backspace and bell", "ab\x08\x07c", "abc"},
		{"NUL and DEL", "a\x00b\x7fc", "abc"},
		{"C1 CSI", "a\u009b31mb", "a31mb"},
		{"bidi override", "a‮b⁦c", "abc"},
		{"invalid UTF-8", "a\xffb", "a�b"},
		{"non-ASCII text survives", "¡Hola, señor! 日本 🚀", "¡Hola, señor! 日本 🚀"},
	} {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("%s: Sanitize(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSanitizeLineKeepsOneLine(t *testing.T) {
	if got := SanitizeLine("a\nb\tc\x1b[1m"); got != "a b c" {
		t.Errorf("got %q", got)
	}
}

func ev(typ string, data any) api.Event {
	raw, _ := json.Marshal(data)
	return api.Event{V: 1, Type: typ, Data: raw}
}

// Every field of every event type is guest text, so every one of them goes
// through the sanitiser - not just the assistant's message.
func TestRendererNeverPrintsAnEscape(t *testing.T) {
	const bad = "\x1b]0;pwned\x07\x1b[2J\r"
	var out bytes.Buffer
	r := &Renderer{Out: &out}
	for _, e := range []api.Event{
		ev("started", map[string]any{"agent": "pi" + bad, "agent_version": bad, "model": bad, "cwd": bad, "branch": bad}),
		ev("log", map[string]any{"stream": "agent_stderr", "text": "line" + bad}),
		ev("message", map[string]any{"role": "assistant", "text": "hi" + bad, "final": true}),
		ev("tool_call", map[string]any{"id": "1", "name": "bash" + bad, "args_preview": bad}),
		ev("tool_result", map[string]any{"id": "1", "name": bad, "is_error": true, "output_preview": bad}),
		ev("artifact", map[string]any{"path": "artifacts/r.md" + bad, "bytes": 3}),
		ev("error", map[string]any{"code": bad, "message": bad}),
		ev("truncated", map[string]any{"reason": bad}),
	} {
		r.Event(e)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07\r") {
		t.Errorf("control bytes reached the terminal: %q", out.String())
	}
	for _, want := range []string{"started pi", "stderr │ line", "hi\n", "→ bash", "← ", "error", "artifact artifacts/r.md (3 bytes)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %q", want, out.String())
		}
	}
}

func TestRendererJoinsMessageChunksAndBreaksBeforeTheNextEvent(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{Out: &out}
	r.Event(ev("message", map[string]any{"text": "Hel"}))
	r.Event(ev("message", map[string]any{"text": "lo"}))
	r.Event(ev("tool_call", map[string]any{"name": "read", "args_preview": "{}"}))
	r.Event(ev("message", map[string]any{"text": "done", "final": true}))
	r.Event(ev("log", map[string]any{"stream": "nan-run", "text": "a\nb\n"}))
	want := "Hello\n→ read {}\ndone\nnan-run │ a\nnan-run │ b\n"
	if out.String() != want {
		t.Errorf("got %q\nwant %q", out.String(), want)
	}
}

func TestExitCodeFollowsTheTable(t *testing.T) {
	code := func(s string) *string { return &s }
	for _, tc := range []struct {
		state string
		err   *string
		want  int
		ok    bool
	}{
		{"succeeded", nil, 0, true},
		{"failed", code("agent_failed"), 1, true},
		{"failed", nil, 1, true},
		{"timed_out", code("timed_out"), 2, true},
		{"cancelled", code("cancelled"), 3, true},
		{"failed", code("config_error"), 4, true},
		{"failed", code("agent_not_installed"), 4, true},
		{"failed", code("no_inference_key"), 4, true},
		{"running", nil, 0, false},
		{"queued", nil, 0, false},
	} {
		got, ok := ExitCode(&api.Run{State: tc.state, ErrorCode: tc.err})
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s/%v: got %d,%v want %d,%v", tc.state, tc.err, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSafeJSONEscapesWhatTerminalsActOn(t *testing.T) {
	out, err := SafeJSON(map[string]string{"t": "a\x1b[31m\u009b‮b"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(out), "\x1b\u009b‮") {
		t.Errorf("raw control in JSON: %q", out)
	}
	var back map[string]string
	if err := json.Unmarshal(out, &back); err != nil || back["t"] != "a\x1b[31m\u009b‮b" {
		t.Errorf("did not round-trip: %v %q", err, back["t"])
	}
}

func TestOutcomeIsOneSanitisedLine(t *testing.T) {
	ec := "agent_failed"
	got := Outcome(&api.Run{ID: "r1", State: "timed_out", ErrorCode: &ec, Result: &api.RunResult{Branch: "nan-run/x\x1b[2J", ChangedFiles: 2}})
	if got != "run r1 timed out (agent_failed) · branch nan-run/x · 2 files changed" {
		t.Errorf("got %q", got)
	}
}

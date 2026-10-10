// Package runs turns the events of a run into lines on a terminal.
//
// Everything a run produces comes out of the member's workspace, where the
// agent - and anything that got into its context - can write whatever bytes
// it likes. Printed raw, an escape sequence in that text can move the cursor,
// rewrite lines already on screen, set the window title, or on some terminals
// write to the clipboard. So no guest string reaches the terminal without
// going through Sanitize.
package runs

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/nxssie/nan-cli/internal/api"
)

// Sanitize removes ANSI escape sequences and control characters from text
// that came from a run, keeping newlines and tabs. Invalid UTF-8 becomes
// U+FFFD, carriage returns are dropped (a bare one rewinds the line), and so
// are the bidirectional overrides that make text read differently from what
// it is.
func Sanitize(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		if r == 0x1b {
			i += escapeLen(s[i:])
			continue
		}
		i += size
		if unsafeRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeLine is Sanitize for a value that has to stay on one line, such as
// a cell of a table: line breaks and tabs become spaces.
func SanitizeLine(s string) string {
	s = Sanitize(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
		return true
	}
	return false
}

// escapeLen is how many bytes the escape sequence at the start of s takes,
// ESC included. An unterminated sequence takes the rest of the string: what
// follows an open OSC is the sequence's payload, not text.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters and intermediates, then a final byte 0x40-0x7e
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: up to BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default: // a two-byte sequence: ESC and one character
		_, size := utf8.DecodeRuneInString(s[1:])
		return 1 + size
	}
}

// Truncate shortens s to at most n runes, marking the cut.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

// Renderer prints a run's events as text. It remembers whether the last
// thing printed left the cursor mid-line, because assistant text arrives in
// chunks and the next event must not be glued onto the end of one.
type Renderer struct {
	Out     io.Writer
	midLine bool
}

func (r *Renderer) newline() {
	if r.midLine {
		fmt.Fprintln(r.Out)
		r.midLine = false
	}
}

func (r *Renderer) line(format string, args ...any) {
	r.newline()
	fmt.Fprintf(r.Out, format+"\n", args...)
}

// prefixed prints text one line at a time, each behind the same label, so a
// multi-line log entry stays attributable.
func (r *Renderer) prefixed(label, text string) {
	r.newline()
	text = strings.TrimRight(Sanitize(text), "\n")
	for _, l := range strings.Split(text, "\n") {
		fmt.Fprintf(r.Out, "%s │ %s\n", label, l)
	}
}

// Event prints one event. Event types this version does not know are skipped
// rather than dumped: a newer platform may send them, and the raw payload is
// still there for `nan runs logs --json`.
func (r *Renderer) Event(ev api.Event) {
	switch ev.Type {
	case "started":
		var d struct {
			Agent        string `json:"agent"`
			AgentVersion string `json:"agent_version"`
			Model        string `json:"model"`
			Cwd          string `json:"cwd"`
			Branch       string `json:"branch"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		parts := []string{"started " + SanitizeLine(strings.TrimSpace(d.Agent+" "+d.AgentVersion))}
		if d.Model != "" {
			parts = append(parts, "model "+SanitizeLine(d.Model))
		}
		if d.Cwd != "" {
			parts = append(parts, "in "+SanitizeLine(d.Cwd))
		}
		if d.Branch != "" {
			parts = append(parts, "on branch "+SanitizeLine(d.Branch))
		}
		r.line("── %s", strings.Join(parts, " · "))
	case "log":
		var d struct {
			Stream string `json:"stream"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		label := "log"
		switch d.Stream {
		case "agent_stdout":
			label = "stdout"
		case "agent_stderr":
			label = "stderr"
		case "nan-run":
			label = "nan-run"
		}
		r.prefixed(label, d.Text)
	case "message":
		var d struct {
			Text  string `json:"text"`
			Final bool   `json:"final"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		text := Sanitize(d.Text)
		if text != "" {
			fmt.Fprint(r.Out, text)
			r.midLine = !strings.HasSuffix(text, "\n")
		}
		if d.Final {
			r.newline()
		}
	case "tool_call":
		var d struct {
			Name        string `json:"name"`
			ArgsPreview string `json:"args_preview"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		r.line("→ %s %s", SanitizeLine(d.Name), Truncate(SanitizeLine(d.ArgsPreview), 160))
	case "tool_result":
		var d struct {
			Name          string `json:"name"`
			IsError       bool   `json:"is_error"`
			OutputPreview string `json:"output_preview"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		status := "ok"
		if d.IsError {
			status = "error"
		}
		preview := Truncate(SanitizeLine(strings.TrimSpace(d.OutputPreview)), 160)
		r.line("← %s %s %s", SanitizeLine(d.Name), status, preview)
	case "artifact":
		var d struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		r.line("── artifact %s (%d bytes)", SanitizeLine(d.Path), d.Bytes)
	case "error":
		var d struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		r.line("── error %s: %s", SanitizeLine(d.Code), SanitizeLine(d.Message))
	case "truncated":
		var d struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		r.line("── output truncated (%s): the run went on, its log did not", SanitizeLine(d.Reason))
	case "finished":
		// The end of the stream carries the authoritative outcome; Finish
		// prints it, so this would only say it twice.
		r.newline()
	}
}

// Flush ends a line left open by assistant text.
func (r *Renderer) Flush() { r.newline() }

// ExitCode is the process exit code for a run, as `nan run` documents it.
// A run that has not finished yet has none; ok is false.
func ExitCode(run *api.Run) (code int, ok bool) {
	switch run.State {
	case api.StateSucceeded:
		return 0, true
	case api.StateTimedOut:
		return 2, true
	case api.StateCancelled:
		return 3, true
	case api.StateFailed:
		if run.ErrorCode != nil {
			switch *run.ErrorCode {
			case "config_error", "agent_not_installed", "no_inference_key":
				return 4, true
			}
		}
		return 1, true
	}
	return 0, false
}

// Outcome is a one-line description of how a run ended, for stderr.
func Outcome(run *api.Run) string {
	var b strings.Builder
	b.WriteString("run " + SanitizeLine(run.ID) + " " + strings.ReplaceAll(SanitizeLine(run.State), "_", " "))
	if run.ErrorCode != nil && *run.ErrorCode != "" {
		b.WriteString(" (" + SanitizeLine(*run.ErrorCode) + ")")
	}
	if run.Result != nil {
		if run.Result.Branch != "" {
			b.WriteString(" · branch " + SanitizeLine(run.Result.Branch))
		}
		if run.Result.ChangedFiles > 0 {
			fmt.Fprintf(&b, " · %d files changed", run.Result.ChangedFiles)
		}
	}
	return b.String()
}

// SafeJSON marshals v for --json output. encoding/json already escapes C0
// controls, ESC included; it leaves C1 controls and the bidi overrides as raw
// UTF-8, which some terminals still act on when the output is read on one. It
// also writes them as \u escapes, which every JSON parser reads back as the
// same string.
func SafeJSON(v any, indent bool) ([]byte, error) {
	var out []byte
	var err error
	if indent {
		out, err = json.MarshalIndent(v, "", "  ")
	} else {
		out, err = json.Marshal(v)
	}
	if err != nil {
		return nil, err
	}
	if !strings.ContainsFunc(string(out), func(r rune) bool { return r >= 0x80 && unsafeRune(r) }) {
		return out, nil
	}
	var b strings.Builder
	for _, r := range string(out) {
		if r >= 0x80 && unsafeRune(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return []byte(b.String()), nil
}

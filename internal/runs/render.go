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
	"time"
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
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

// NameAndID shows a named object in a detail view: "name (id)" when the
// name is known, so the id stays there to copy, and the id alone when it is
// not. Both are sanitised.
func NameAndID(name, id string) string {
	name, id = SanitizeLine(name), SanitizeLine(id)
	switch {
	case name == "":
		return id
	case id == "" || name == id:
		return name
	}
	return name + " (" + id + ")"
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
	case "approval_required":
		var d struct {
			ProposalSHA256 string   `json:"proposal_sha256"`
			Kinds          []string `json:"kinds"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		kinds := make([]string, 0, len(d.Kinds))
		for _, k := range d.Kinds {
			kinds = append(kinds, SanitizeLine(k))
		}
		r.line("── approval required for: %s (proposal %s)", strings.Join(kinds, ", "), Truncate(SanitizeLine(d.ProposalSHA256), 13))
	case "action":
		var d struct {
			Index   int    `json:"index"`
			Kind    string `json:"kind"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		msg := ""
		if m := Truncate(SanitizeLine(strings.TrimSpace(d.Message)), 200); m != "" {
			msg = ": " + m
		}
		r.line("── action %d %s %s%s", d.Index+1, SanitizeLine(d.Kind), SanitizeLine(d.Status), msg)
	case "finished":
		// The end of the stream carries the authoritative outcome; Finish
		// prints it, so this would only say it twice.
		r.newline()
	}
}

// Flush ends a line left open by assistant text.
func (r *Renderer) Flush() { r.newline() }

// ExitAwaitingApproval is the exit code for a run whose agent is done and
// whose proposed actions wait for the member's approval. The run is not
// over, but nothing more happens until the member decides, so a script
// waiting on it has its answer.
const ExitAwaitingApproval = 10

// ExitCode is the process exit code for a run, as `nan run` documents it.
// A run that has not finished yet has none; ok is false.
func ExitCode(run *api.Run) (code int, ok bool) {
	switch run.State {
	case api.StateSucceeded:
		return 0, true
	case api.StateAwaitingApproval:
		return ExitAwaitingApproval, true
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
	if run.State == api.StateAwaitingApproval {
		b.WriteString("\n" + ApprovalHint(run))
	}
	return b.String()
}

// PortalURL is the member portal, where approving, writing secrets and
// creating or editing automations happen.
const PortalURL = "https://cloud.nan.builders"

// AutomationsURL is the portal's list of automations.
const AutomationsURL = PortalURL + "/automations"

// ApprovalsURL is the portal's approvals inbox, a tab of the automations page.
const ApprovalsURL = AutomationsURL + "?tab=approvals"

// NewAutomationURL is where an automation is created.
const NewAutomationURL = AutomationsURL + "/new"

// AutomationURL is one automation's page in the portal, or the list when
// the id is not well-formed.
func AutomationURL(id string) string {
	if !isUUID(id) {
		return AutomationsURL
	}
	return AutomationsURL + "/" + strings.ToLower(id)
}

// RunURL is a run's page in the portal, where its proposal is approved.
// The workspace id is part of the path; without a well-formed one this
// falls back to the approvals inbox.
func RunURL(run *api.Run) string {
	if !isUUID(run.ID) || !isUUID(run.Workspace.ID) {
		return ApprovalsURL
	}
	return PortalURL + "/workspaces/" + strings.ToLower(run.Workspace.ID) + "/runs/" + strings.ToLower(run.ID)
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// ApprovalHint tells a member what to do with a run awaiting approval:
// where to read the proposal, where to approve it, and how to turn it down.
func ApprovalHint(run *api.Run) string {
	return "  review:  nan runs proposal " + SanitizeLine(run.ID) + "\n" + approveLine(run) + "\n" + rejectLine(run)
}

func approveLine(run *api.Run) string {
	return "  approve: in the portal, " + RunURL(run) + " (approving needs a browser sign-in)"
}

func rejectLine(run *api.Run) string {
	return "  reject:  nan runs reject " + SanitizeLine(run.ID)
}

// jsonUnsafe is what encoding/json leaves raw and a terminal may act on.
func jsonUnsafe(r rune) bool { return r == 0x7f || (r >= 0x80 && unsafeRune(r)) }

// SafeJSON marshals v for --json output. encoding/json already escapes C0
// controls, ESC included; it leaves C1 controls and the bidi overrides as raw
// UTF-8, which some terminals still act on when the output is read on one,
// and it passes invalid UTF-8 in a json.RawMessage through untouched. This
// writes all of those as \u escapes, which every JSON parser reads back.
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
	if utf8.Valid(out) && !strings.ContainsFunc(string(out), jsonUnsafe) {
		return out, nil
	}
	// Invalid UTF-8 can only be inside a string here - json.RawMessage
	// carries a guest's bytes through verbatim - and a lone 0x9b is a
	// C1 CSI to a terminal not in UTF-8 mode. It goes out as U+FFFD.
	var b strings.Builder
	for i := 0; i < len(out); {
		r, size := utf8.DecodeRune(out[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString(`\ufffd`)
		case jsonUnsafe(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return []byte(b.String()), nil
}

// Proposal prints a run's proposal as plain text: what the agent proposed
// to do, which of it applies automatically and which waits for the member,
// and where to decide. Every string in a proposal except the action kinds
// was written by the agent; it goes through Sanitize like any other text
// from a run, so a body cannot carry an escape sequence to the terminal.
func Proposal(w io.Writer, run *api.Run) {
	a := run.Approval
	id := SanitizeLine(run.ID)
	row := func(k, v string) { fmt.Fprintf(w, "%-12s%s\n", k, v) }

	row("run", id+" ("+strings.ReplaceAll(SanitizeLine(run.State), "_", " ")+")")
	if run.Automation != nil {
		row("automation", NameAndID(run.Automation.Name, run.Automation.ID))
	}
	state := SanitizeLine(a.State)
	switch a.State {
	case api.ApprovalPending:
		state = "waiting for your approval"
		if a.ExpiresAt != nil {
			state += ", expires " + localTime(*a.ExpiresAt)
		}
	case api.ApprovalAuto:
		state = "applied automatically (no action needed approval)"
	}
	row("approval", state)
	if a.RejectReason != nil && *a.RejectReason != "" {
		row("reason", Truncate(SanitizeLine(*a.RejectReason), 200))
	}
	row("sha256", SanitizeLine(a.ProposalSHA256))

	var p api.Proposal
	if a.Proposal != nil {
		p = *a.Proposal
	}
	if p.Base != nil {
		base := SanitizeLine(*p.Base)
		if p.MergeBase != nil {
			base += " (merge base " + Truncate(SanitizeLine(*p.MergeBase), 13) + ")"
		}
		row("base", base)
	}
	ds := a.Diffstat
	if ds == nil {
		ds = p.Diffstat
	}
	if ds != nil {
		row("changes", fmt.Sprintf("%d files, +%d -%d", ds.FilesChanged, ds.Insertions, ds.Deletions))
	}

	from := -1
	if a.ApprovalFrom != nil {
		from = *a.ApprovalFrom
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "actions:")
	if len(p.Actions) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for i, act := range p.Actions {
		mode := "automatic"
		if from >= 0 && i >= from {
			mode = "needs approval"
		}
		fmt.Fprintf(w, "  %d. %s  [%s]\n", i+1, actionLine(act), mode)
		if act.CommitMessage != "" {
			indented(w, "commit message", act.CommitMessage)
		}
		if act.Body != "" {
			indented(w, "body", act.Body)
		}
	}

	if ds != nil && len(ds.Files) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "files:")
		for _, f := range ds.Files {
			change := fmt.Sprintf("+%d -%d", f.Added, f.Deleted)
			if f.Binary {
				change = "binary"
			}
			fmt.Fprintf(w, "  %-12s %s\n", change, SanitizeLine(f.Path))
		}
		if ds.Truncated {
			fmt.Fprintln(w, "  … (more files than the summary lists)")
		}
	}

	fmt.Fprintln(w)
	if a.HasDiff {
		fmt.Fprintf(w, "The full diff is stored: nan runs proposal %s --diff\n", id)
	}
	if a.DiffMasked {
		fmt.Fprintln(w, "The diff contains the value of one of your secrets (shown here as [masked]); approving pushes it to your repository.")
	}
	if a.State == api.ApprovalPending && run.State == api.StateAwaitingApproval {
		fmt.Fprintln(w, approveLine(run))
		fmt.Fprintln(w, rejectLine(run))
	}
}

func actionLine(a api.ProposalAction) string {
	s := SanitizeLine
	ref := func() string {
		if a.Number > 0 {
			return fmt.Sprintf("%s#%d", s(a.Repo), a.Number)
		}
		return s(a.Repo)
	}
	switch a.Kind {
	case "git_push":
		line := "git_push  " + s(a.Branch) + " onto " + s(a.Base)
		if a.Commit != "" {
			line += " (commit " + Truncate(s(a.Commit), 13) + ")"
		}
		if a.Force {
			line += " FORCE"
		}
		return line
	case "github_create_pr":
		line := fmt.Sprintf("github_create_pr  %s  %s -> %s  %q", s(a.Repo), s(a.Head), s(a.Base), Truncate(s(a.Title), 120))
		if a.Draft {
			line += " (draft)"
		}
		return line
	case "github_pr_review":
		return "github_pr_review  " + ref() + "  " + s(a.Event)
	case "github_add_labels":
		labels := make([]string, len(a.Labels))
		for i, l := range a.Labels {
			labels[i] = s(l)
		}
		return "github_add_labels  " + ref() + "  " + strings.Join(labels, ", ")
	case "github_pr_comment", "github_issue_comment":
		return s(a.Kind) + "  " + ref()
	}
	return s(a.Kind) + "  " + ref()
}

func indented(w io.Writer, title, text string) {
	text = strings.TrimRight(Sanitize(text), "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintf(w, "     %s:\n", title)
	for _, l := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "       %s\n", l)
	}
}

// Diff prints a proposal's diff, sanitised: it is text the agent produced.
func Diff(w io.Writer, diff string) {
	text := Sanitize(diff)
	fmt.Fprint(w, text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(w)
	}
}

func localTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return SanitizeLine(ts)
	}
	return t.Local().Format("2006-01-02 15:04")
}

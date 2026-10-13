package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"

	"github.com/nxssie/nan-cli/internal/ui"
)

// How anything gets printed.
//
// A CLI ends up in a pipe, a file, a cron job and a CI log, and every one of
// those wants something different from the terminal a person is looking at.
// There are three audiences here and one place that knows about all three:
//
//   - a terminal, which gets the styled output;
//   - a pipe, which gets the same words with the colour taken out;
//   - a script that asked for --json, which gets the control plane's own
//     shapes and none of the prose.
//
// Lip Gloss v2 is a pure value type with no renderer attached, so unlike v1
// it does NOT strip colour on its own when nothing is watching — escape
// sequences go wherever you send them. colorprofile.Writer is what closes
// that: it downsamples to what the destination can take, which for a pipe is
// nothing at all, and it honours NO_COLOR and CLICOLOR_FORCE on the way.

// Printer writes a command's output.
type Printer struct {
	out    io.Writer
	errOut io.Writer
	asJSON bool
	tty    bool
	width  int
}

// NewPrinter wires a printer to the real terminal.
//
// --no-color sets the writer's profile directly rather than adding NO_COLOR
// to the environment it is shown. Both look equivalent and are not:
// CLICOLOR_FORCE beats NO_COLOR inside colorprofile, so a shell that forces
// colour on would have silently ignored the flag. A flag somebody typed
// beats a variable somebody's shell profile set years ago.
func NewPrinter(asJSON, noColor bool) *Printer {
	tty := term.IsTerminal(os.Stdout.Fd())

	// One place decides this, in ui, because the forms need the same answer
	// and the two used to work it out separately.
	width := ui.TerminalWidth()
	out := colorprofile.NewWriter(os.Stdout, os.Environ())
	errOut := colorprofile.NewWriter(os.Stderr, os.Environ())
	if noColor {
		out.Profile = colorprofile.NoTTY
		errOut.Profile = colorprofile.NoTTY
	}

	return &Printer{
		out:    out,
		errOut: errOut,
		asJSON: asJSON,
		tty:    tty,
		width:  width,
	}
}

// JSONMode reports whether the caller asked for machine-readable output.
// Commands check it before building anything a person would read.
func (p *Printer) JSONMode() bool { return p.asJSON }

// Interactive reports whether a person is watching. It gates the things
// that only make sense in front of one: a prompt, a dashboard, a banner.
func (p *Printer) Interactive() bool { return p.tty }

// Width is the terminal's, or a sane fixed width when there isn't one.
func (p *Printer) Width() int { return p.width }

// JSON writes a value as indented JSON and nothing else. No banner, no
// styling, no trailing prose — whatever is on stdout in this mode has to
// survive being piped into jq.
func (p *Printer) JSON(v any) error {
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Line writes one line.
func (p *Printer) Line(s string) { _, _ = fmt.Fprintln(p.out, s) }

// Prompt writes a line with no newline after it, for a question waiting on
// an answer.
func (p *Printer) Prompt(format string, args ...any) {
	_, _ = fmt.Fprintf(p.out, indent+format, args...)
}

// Blank writes an empty line, for the air between blocks.
func (p *Printer) Blank() { _, _ = fmt.Fprintln(p.out) }

// Block writes a pre-rendered multi-line block as-is.
func (p *Printer) Block(s string) { _, _ = fmt.Fprint(p.out, s) }

// Section is a heading: the brand's eyebrow, with air above it.
func (p *Printer) Section(title string) {
	p.Blank()
	p.Line(indent + ui.Eyebrow(title))
	p.Blank()
}

// KV prints a run of label/value rows lined up on a shared column.
func (p *Printer) KV(rows [][2]string) {
	p.Block(ui.KVBlock(rows, indent))
}

// Table prints a table, or the given sentence when there are no rows.
//
// The empty case is a sentence rather than an empty table on purpose: bare
// headers with nothing under them read as a failure to load, and "no API
// keys yet" is both true and actionable.
func (p *Printer) Table(t ui.Table, empty string) {
	t.Indent = indent
	// The table has to be told the width or it sizes to its content and
	// runs off the side of the terminal. It was not being told, so every
	// table here overflowed on anything narrower than its widest row —
	// which on a real organisation is most terminals.
	if t.Width == 0 {
		t.Width = p.width - len(indent)*2
	}
	p.Block(ui.TableOrEmpty(t, empty))
}

// Success reports that a command changed something. It, Warn and Note are the
// three things a command says about what it just did.
func (p *Printer) Success(format string, args ...any) {
	p.Line(indent + ui.Success(fmt.Sprintf(format, args...)))
}

func (p *Printer) Warn(format string, args ...any) {
	p.Line(indent + ui.Warn(fmt.Sprintf(format, args...)))
}

func (p *Printer) Note(format string, args ...any) {
	p.Line(indent + ui.Faint(fmt.Sprintf(format, args...)))
}

// Bar renders a progress bar sized to the terminal, so it fills a wide one
// and still fits a narrow one.
func (p *Printer) Bar(fraction float64, highlight bool) string {
	width := p.width / 3
	if width > 40 {
		width = 40
	}
	if width < 10 {
		width = 10
	}
	return ui.Bar(fraction, width, highlight)
}

// LevelBar is Bar in the usage scale, green to red, for a share of an
// allowance: how spent it is reads off the colour.
func (p *Printer) LevelBar(fraction float64) string {
	width := min(max(p.width/3, 10), 40)
	return ui.LevelBar(fraction, width)
}

// indent is the left margin everything shares. Two columns: enough that the
// output is not welded to the edge of the terminal, little enough that it
// does not look like a quotation.
const indent = "  "

package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/x/term"
)

// ErrorHandler prints a failed command's error, framed like the rest of
// fang's output but with the message exactly as it was written.
//
// fang's own handler title-cases the first word and adds a full stop. The
// first word is often a value - a run id, a flag, a tool's name - and
// "not-an-id" printed as "Not-An-Id" is a different id from the one the
// member typed. `--detach` and a Ctrl-C that left a run going end with a
// code and nothing to say, so those print nothing at all.
func ErrorHandler(w io.Writer, styles fang.Styles, err error) {
	if Silent(err) {
		return
	}
	msg := err.Error()
	// To a pipe or a file: the message and nothing else, for whatever is
	// reading it.
	if f, ok := w.(term.File); ok && !term.IsTerminal(f.Fd()) {
		_, _ = fmt.Fprintln(w, msg)
		return
	}
	if !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, "?") && !strings.HasSuffix(msg, "!") {
		msg += "."
	}
	_, _ = fmt.Fprintln(w, styles.ErrorHeader.String())
	_, _ = fmt.Fprintln(w, styles.ErrorText.UnsetTransform().Render(msg))
	_, _ = fmt.Fprintln(w)
}

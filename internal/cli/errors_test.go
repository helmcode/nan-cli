package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"charm.land/fang/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// The message is printed as written: its first word is often a value, and
// a run id title-cased is a different id.
func TestErrorsArePrintedAsWritten(t *testing.T) {
	var out bytes.Buffer
	styles := fang.Styles{}
	ErrorHandler(&out, styles, errors.New(`"not-an-id" is not a run id`))
	if got := ansi.Strip(out.String()); !strings.Contains(got, `"not-an-id" is not a run id.`) {
		t.Errorf("printed %q", got)
	}

	out.Reset()
	ErrorHandler(&out, styles, &ExitError{Code: exitDetached})
	if out.Len() != 0 {
		t.Errorf("a silent exit printed %q", out.String())
	}
}

// fang hands the handler os.Stderr wrapped in a colorprofile.Writer, so a
// check for a terminal on the writer itself never saw the pipe behind it and
// every error went to scripts framed, with a full stop added. main prints the
// bare message there, and so does this.
func TestErrorsToAPipeAreTheBareMessage(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ErrorHandler(colorprofile.NewWriter(w, nil), fang.Styles{}, errors.New("not logged in - run: nan auth login"))
	_ = w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := "not logged in - run: nan auth login\n"; string(got) != want {
		t.Errorf("to a pipe the error printed %q, want %q", got, want)
	}
}

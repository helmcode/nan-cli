package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/fang"
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

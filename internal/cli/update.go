package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/ui"
	"github.com/nxssie/nan-cli/internal/update"
)

// After a command, one line when a newer release is out - what gh and most
// CLIs do - with the command that updates this copy.
//
// On stderr, and only to a person: never with --json, never into a pipe,
// never under a script. Asked for at most once a day (update.CheckEvery),
// and then with a short timeout, so a slow or missing endpoint costs a run
// a second at worst and usually nothing. The dashboard says the same in its
// own header, so it is not repeated when the dashboard was what ran.

// updateCheckTimeout bounds the one request this adds, on the days it adds one.
const updateCheckTimeout = 1500 * time.Millisecond

// noticeUpdate is run after every command.
func noticeUpdate(ctx context.Context, a *app, dashboard bool) {
	if a == nil || dashboard || a.p.JSONMode() || !a.p.Interactive() || !term.IsTerminal(os.Stderr.Fd()) {
		return
	}
	st := update.Load()
	if st.DueForCheck(time.Now()) {
		ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
		release, err := api.LatestRelease(ctx)
		cancel()
		if err == nil {
			// Checked, whatever the answer: "nothing newer" is an answer, and
			// asking again on the next run would make every run pay for it.
			st.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			if release != nil {
				st.Latest = release.Version
			}
			_ = st.Save()
		}
	}
	if v := st.Available(); v != "" {
		_, _ = fmt.Fprintf(a.p.errOut, "\n%s%s %s\n%s%s\n", indent,
			ui.Accent(build.Name+" "+v+" is out"), ui.Faint("(you have "+build.Version+"). Update with:"),
			indent+"  ", ui.Value(update.Command()))
	}
}

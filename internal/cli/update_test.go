package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/update"
)

// A check that failed is still a check. Only a successful one used to be
// recorded, so a machine with no network asked again after every command and
// waited out the timeout each time.
func TestAFailedUpdateCheckWaitsADayToo(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	st := update.State{Latest: "0.2.0"}
	recordCheck(&st, now, nil, errors.New("could not reach github.com"))
	if st.DueForCheck(now.Add(time.Hour)) {
		t.Error("an hour after a failed check, the next command checks again")
	}
	if st.Latest != "0.2.0" {
		t.Errorf("a failed check forgot the newest release seen: %q", st.Latest)
	}
}

func TestASuccessfulUpdateCheckKeepsTheAnswer(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	var st update.State
	recordCheck(&st, now, &api.Release{Version: "0.2.1"}, nil)
	if st.Latest != "0.2.1" || st.DueForCheck(now.Add(time.Hour)) {
		t.Errorf("after a successful check: %+v", st)
	}
}

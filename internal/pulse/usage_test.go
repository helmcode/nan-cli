package pulse

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/helmcode/nan-cli/internal/api"
)

// withMonth gives the demo a month of daily usage (every seventh day quiet)
// and a credits member with a pack.
func withMonth(m model) model {
	now := time.Now().UTC()
	for i := range 30 {
		if i%7 == 3 {
			continue
		}
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		m.snap.usage.Daily = append(m.snap.usage.Daily,
			api.DayUsage{Date: d, Model: "glm5.3-flash", InputTokens: api.Count(1e6 * float64(30-i))},
			api.DayUsage{Date: d, Model: "deepseek-v4-flash", OutputTokens: 1000})
	}
	exp := now.AddDate(0, 2, 0).Format(time.RFC3339)
	m.snap.credits = &api.Credits{
		Enabled: true, PeriodEnd: now.AddDate(0, 0, 12).Format(time.RFC3339),
		AllowanceCredits: 500, UsedCredits: 312.4, RemainingCredits: 187.6,
		PackCredits: 50, PackExpiresAt: &exp,
	}
	return m
}

// The chart is thirty days, oldest first, every model of a day summed, and
// a day with no traffic is a zero rather than missing, so a quiet day shows
// as a gap and not as the days either side of it closing up.
func TestDailyIsThirtyDaysWithTheQuietOnesAsZero(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m = withMonth(m)
	now := time.Now()
	dates, totals := m.snap.daily(now)
	if len(dates) != chartDays || len(totals) != chartDays {
		t.Fatalf("%d days, want %d", len(dates), chartDays)
	}
	if today := now.UTC().Format("2006-01-02"); dates[chartDays-1] != today {
		t.Errorf("the last day is %s, want today %s", dates[chartDays-1], today)
	}
	if got, want := totals[chartDays-1], 30e6+1000; got != want {
		t.Errorf("today is %v tokens, want both models summed (%v)", got, want)
	}
	if totals[chartDays-1-3] != 0 {
		t.Errorf("a quiet day is %v, want 0", totals[chartDays-1-3])
	}
}

// On an 80x24 terminal Usage shows the credits, the bought ones, the chart
// and the models, all of it.
func TestUsageShowsCreditsAndTheMonthOnASmallTerminal(t *testing.T) {
	m, _ := demoModel(t, 80, 24)
	m = withMonth(m)
	out := screen(press(t, m, keyOf("3")))
	for _, want := range []string{"Credits", "312.4 / 500 used", "187.6 left", "resets", "+ 50 bought", "tokens per day", "busiest", "glm5.3-flash", "deepseek-v4-flash"} {
		if !strings.Contains(out, want) {
			t.Errorf("Usage at 80x24 lacks %q:\n%s", want, out)
		}
	}
}

// The models come before the chart: on a terminal too short for both, the
// chart is what goes.
func TestTheChartGivesWayToTheModels(t *testing.T) {
	m, _ := demoModel(t, 80, 20)
	m = withMonth(m)
	out := screen(press(t, m, keyOf("3")))
	if strings.Contains(out, "tokens per day") {
		t.Errorf("the chart is drawn on a terminal with no room for it:\n%s", out)
	}
	if !strings.Contains(out, "glm5.3-flash") {
		t.Errorf("the models are not on screen:\n%s", out)
	}
}

// A member not on credits sees no credits line; one whose credits are on
// hold is told why requests are refused.
func TestCreditsAreSaidOnlyWhenThereAreAny(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m.snap.credits = &api.Credits{Enabled: false}
	if out := screen(press(t, m, keyOf("3"))); strings.Contains(out, "Credits") {
		t.Errorf("a member not on credits sees a credits line:\n%s", out)
	}
	m.snap.credits = &api.Credits{Enabled: true, Frozen: true, AllowanceCredits: 0}
	if out := screen(press(t, m, keyOf("3"))); !strings.Contains(out, "on hold") {
		t.Errorf("frozen credits are not said:\n%s", out)
	}
}

// Profile shows the Discord name, which the member chose to be seen by,
// whole; an account with none linked says where to link one.
func TestProfileShowsTheDiscordName(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m.snap.discord = &api.Discord{Linked: true, Username: "nan_member"}
	if out := screen(press(t, m, keyOf("2"))); !strings.Contains(out, "nan_member") {
		t.Errorf("the Discord name is not on Profile:\n%s", out)
	}
	m.snap.discord = &api.Discord{Linked: false}
	if out := screen(press(t, m, keyOf("2"))); !strings.Contains(out, "not linked") {
		t.Errorf("an unlinked account is not said:\n%s", out)
	}
	m.snap.discord = nil
	for _, r := range m.rows(areaProfile) {
		if r.id == "field:discord" {
			t.Error("a Discord row with nothing read")
		}
	}
}

// Names from these answers reach table cells, which keep escapes.
func TestCreditsAndDiscordNamesLoseEscapes(t *testing.T) {
	d := cleanDiscord(&api.Discord{Linked: true, Username: "me\x1b]0;x\x07"})
	c := cleanCredits(&api.Credits{Models: []api.ModelCredits{{Model: "m\x1b[2J", DisplayName: "M\x1b[2J"}}})
	u := cleanUsage(&api.Usage{Daily: []api.DayUsage{{Model: "d\x1b[2J"}}})
	for _, s := range []string{d.Username, c.Models[0].Model, c.Models[0].DisplayName, u.Daily[0].Model} {
		if strings.ContainsRune(s, 0x1b) {
			t.Errorf("%q kept an escape", s)
		}
	}
}

// On credits, the table says what each model cost this period, where it
// otherwise has all-time tokens, and amounts read as the web writes them.
func TestUsageSaysWhatEachModelCostInCredits(t *testing.T) {
	m, _ := demoModel(t, 100, 30)
	m.snap.credits = &api.Credits{
		Enabled: true, AllowanceCredits: 20000, UsedCredits: 455.2, RemainingCredits: 19544.8,
		CacheSavedCredits: 12396.7,
		Models:            []api.ModelCredits{{Model: "glm5.3-flash", CreditsThisMonth: 1396.9}},
	}
	out := screen(press(t, m, keyOf("3")))
	for _, want := range []string{"Credits", "1,396.9", "19,544.8 left", "/ 20,000 used", "cache saved 12,396.7"} {
		if !strings.Contains(out, want) {
			t.Errorf("Usage lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "All time") {
		t.Errorf("a member on credits still sees all-time tokens per model:\n%s", out)
	}
	m.snap.credits = nil
	if out := screen(press(t, m, keyOf("3"))); !strings.Contains(out, "All time") {
		t.Errorf("a member not on credits lost all-time tokens per model:\n%s", out)
	}
}

func TestCreditsAreWrittenAsTheWebWritesThem(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 50: "50", 455.2: "455.2", 19544.8: "19,544.8", 1234567: "1,234,567", -1500.5: "-1,500.5"} {
		if got := credits(in); got != want {
			t.Errorf("credits(%v) = %q, want %q", in, got, want)
		}
	}
}

// The credits bar spans the width it is given, ends on an eighth of a cell,
// and shows any use at all: 455 of 20,000 is a sliver, not an empty track.
func TestTheCreditsBarShowsWhatWasUsed(t *testing.T) {
	for _, c := range []struct {
		frac float64
		used bool
		fill string
	}{
		{0, false, ""},
		{0.0001, true, "▏"},
		{455.2 / 20000, true, "▉"}, // 7.3 eighths of 40 cells
		{1, true, strings.Repeat("█", 40)},
	} {
		bar := ansi.Strip(creditsBar(c.frac, c.used, 40))
		if got := ansi.StringWidth(bar); got != 40 {
			t.Errorf("%v: the bar is %d wide, want 40", c.frac, got)
		}
		fill := strings.TrimRight(bar, "░")
		if c.fill == "" && fill != "" || c.fill != "" && !strings.HasPrefix(fill, c.fill) {
			t.Errorf("%v: filled %q, want it to start with %q", c.frac, fill, c.fill)
		}
	}
}

// A table whose rows all fit shows them all: it used to keep a line for
// "1-1 of 2" whether or not anything was left out, and show one row less.
func TestATableShowsEveryRowThatFits(t *testing.T) {
	rows := []row{{id: "a", cells: []string{"a"}}, {id: "b", cells: []string{"b"}}}
	out := ansi.Strip(table([]column{{title: "Model"}}, rows, 0, 40, 4, motion{reveal: 1}))
	if !strings.Contains(out, "b") || strings.Contains(out, "of 2") {
		t.Errorf("two rows in four lines:\n%s", out)
	}
}

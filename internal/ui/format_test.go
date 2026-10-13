package ui

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTokens(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.00K"},
		{1250, "1.25K"},
		{412_400_000, "412.4M"},
		// Trailing zeros are KEPT. Dropping them was the old behaviour and
		// it made a figure lie about its own precision: 4,040,000,000 came
		// out as "4B", indistinguishable from a round four billion.
		{12_000_000, "12.0M"},
		{2_500_000_000, "2.50B"},
		{4_040_000_000, "4.04B"},
		{4_120_000_000, "4.12B"},
		// Two decimals below ten, one above: no range shows less than it
		// used to, and the small end - where one decimal spans a hundred
		// million tokens - shows more.
		{41_230_000_000, "41.2B"},
		{412_300_000_000, "412.3B"},
		{-1500, "-1.50K"},
	}
	for _, c := range cases {
		if got := Tokens(c.in); got != c.want {
			t.Errorf("Tokens(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTokensSurvivesNonsense(t *testing.T) {
	// A control plane sending a NaN should produce an em-dash, not the
	// literal "NaN" in the middle of a table.
	if got := Tokens(nan()); got != "-" {
		t.Errorf("Tokens(NaN) = %q, want an em-dash", got)
	}
}

func nan() float64 { var z float64; return z / z } //nolint:staticcheck // deliberate NaN

func TestCountGroups(t *testing.T) {
	cases := map[float64]string{0: "0", 999: "999", 1000: "1,000", 18422: "18,422", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := Count(in); got != want {
			t.Errorf("Count(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		ts   string
		want string
	}{
		{"", "never"},
		{now.Add(-30 * time.Second).Format(time.RFC3339), "just now"},
		{now.Add(-5 * time.Minute).Format(time.RFC3339), "5 minutes ago"},
		{now.Add(-1 * time.Hour).Format(time.RFC3339), "1 hour ago"},
		{now.Add(-48 * time.Hour).Format(time.RFC3339), "2 days ago"},
		// A clock skewed forward must not produce "in 3 minutes ago".
		{now.Add(10 * time.Minute).Format(time.RFC3339), "just now"},
	}
	for _, c := range cases {
		if got := Ago(c.ts); got != c.want {
			t.Errorf("Ago(%q) = %q, want %q", c.ts, got, c.want)
		}
	}
}

func TestAgoPassesThroughGarbage(t *testing.T) {
	// The raw value is more use to somebody reporting a bug than a shrug.
	if got := Ago("not a timestamp"); got != "not a timestamp" {
		t.Errorf("Ago(garbage) = %q, want it passed through", got)
	}
}

func TestUntil(t *testing.T) {
	now := time.Now().UTC()
	if got := Until(now.Add(-time.Hour).Format(time.RFC3339)); got != "expired" {
		t.Errorf("a past deadline should be %q, got %q", "expired", got)
	}
	if got := Until(now.Add(23 * time.Hour).Format(time.RFC3339)); got != "in 22 hours" && got != "in 23 hours" {
		t.Errorf("Until(+23h) = %q", got)
	}
}

func TestLatency(t *testing.T) {
	// Seconds carry the same precision as Tokens: "1.8s" and "1.84s" cost
	// one character apart and only one of them shows a figure moving.
	cases := map[float64]string{0: "-", 420: "420ms", 999: "999ms", 1840: "1.84s", 12_300: "12.3s"}
	for in, want := range cases {
		if got := Latency(in); got != want {
			t.Errorf("Latency(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateMeasuresDisplayWidth(t *testing.T) {
	SetTheme(NewTheme(true))
	if got := Truncate("production-api", 40); got != "production-api" {
		t.Errorf("a string that fits must come back untouched, got %q", got)
	}
	got := Truncate("a-very-long-api-key-name-indeed", 10)
	if len([]rune(got)) != 10 {
		t.Errorf("Truncate to 10 gave %q (%d runes)", got, len([]rune(got)))
	}
	if got[len(got)-len("…"):] != "…" {
		t.Errorf("a truncated string should end in an ellipsis, got %q", got)
	}
}

// Paths on screen are shortened to `~`, which is both shorter and one less
// thing to redact out of a screenshot. Anything outside the home directory is
// left alone: a wrong abbreviation is worse than none.
func TestHomePathShortensOnlyWhatIsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // where Windows keeps it

	cases := []struct{ in, want string }{
		{filepath.Join(home, ".config", "helmcode", "session.json"), filepath.Join("~", ".config", "helmcode", "session.json")},
		{home, "~"},
		{"/etc/helmcode.json", "/etc/helmcode.json"},
		// A directory that merely STARTS with the home path is a different
		// directory, and abbreviating it would name a file that is not there.
		{filepath.Join(home+"-backup", "config.json"), filepath.Join(home+"-backup", "config.json")},
		{"", ""},
	}
	for _, c := range cases {
		if got := HomePath(c.in); got != c.want {
			t.Errorf("HomePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The platform sends a dash when there is nothing to compare with. That is
// no delta, and must not reach the screen as an em dash.
func TestADashDeltaIsNoDelta(t *testing.T) {
	em, en := string(rune(0x2014)), string(rune(0x2013)) // not literals: see dashes_test.go
	for _, d := range []string{em, en, "-", " " + em + " "} {
		if got := Delta(d); got != "" {
			t.Errorf("Delta(%q) = %q, want nothing", d, got)
		}
	}
}

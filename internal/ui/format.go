package ui

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Turning the control plane's figures into something a person reads at a
// glance. These live here rather than in each screen because a token count
// that is abbreviated one way in the dashboard and another in `usage` is the
// same bug as two different shades of indigo.

// Tokens abbreviates a count: 4.12B, 41.2B, 412.4M. Below a thousand the
// exact number is short enough to read and interesting enough to want, so it
// is printed in full.
//
// Three figures, and the trailing zeros are KEPT. An earlier version showed
// one decimal and dropped it when it was a zero, on the reasoning that
// "12.0M" reads as a rounding artefact where "12M" reads as a measurement.
// That is true of 12.0 and false of everything near it: 4,040,000,000 came
// out as "4B", which does not look abbreviated at all — it looks like a
// round number, and a reader has no way to tell the two apart. Showing
// "4.04B" costs three characters and stops the figure lying about its own
// precision.
func Tokens(n float64) string {
	switch {
	case math.IsNaN(n) || math.IsInf(n, 0):
		return "-"
	case n < 0:
		return "-" + Tokens(-n)
	case n >= 1e9:
		return precise(n/1e9) + "B"
	case n >= 1e6:
		return precise(n/1e6) + "M"
	case n >= 1e3:
		return precise(n/1e3) + "K"
	default:
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
}

// precise renders a value in [1, 1000) with always at least one decimal, and
// two below ten.
//
// Not "three significant figures", which was the first attempt: it turns
// 412.4M into "412M" and loses a digit this screen used to show. The rule is
// that no range gets less precision than it had, and the small end — where a
// single decimal is coarsest, because 4.1 covers a span of a hundred million
// tokens — gets one more.
func precise(v float64) string {
	if v < 10 {
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// Count formats a plain integer with thousands separators.
func Count(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return "-"
	}
	return group(strconv.FormatFloat(math.Round(n), 'f', 0, 64))
}

func group(digits string) string {
	neg := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")
	var parts []string
	for len(digits) > 3 {
		parts = append([]string{digits[len(digits)-3:]}, parts...)
		digits = digits[:len(digits)-3]
	}
	parts = append([]string{digits}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}

// Date renders an RFC 3339 timestamp as a day. Anything unparseable is
// passed through untouched: the raw value is more use to somebody reporting
// a bug than a shrug is.
func Date(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		if ts == "" {
			return "-"
		}
		return ts
	}
	return t.Local().Format("2006-01-02")
}

// Ago is how long ago something was, in the roughest unit that is still
// true. A key's last use is a question of "recently or not", and an exact
// timestamp makes that harder to answer rather than easier.
func Ago(ts string) string {
	if ts == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < 0:
		// A clock skewed forwards, or a timestamp from the future. Saying
		// "in 3 minutes" about the past is worse than saying "just now".
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	case d < 365*24*time.Hour:
		return plural(int(d.Hours()/24/30), "month") + " ago"
	default:
		return plural(int(d.Hours()/24/365), "year") + " ago"
	}
}

// Until is Ago's counterpart, for a deadline that has not passed yet.
func Until(ts string) string {
	if ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	switch {
	case d < time.Hour:
		return "in " + plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return "in " + plural(int(d.Hours()), "hour")
	default:
		return "in " + plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// Latency renders milliseconds the way a latency is discussed: under a
// second in ms, above it in seconds.
func Latency(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	if ms < 1000 {
		return strconv.FormatFloat(math.Round(ms), 'f', 0, 64) + "ms"
	}
	// Same three significant figures as Tokens, for the same reason: "1.8s"
	// and "1.84s" cost one character apart and only one of them tells you
	// whether the figure moved.
	return precise(ms/1000) + "s"
}

// HomePath shortens a path under the user's home directory to `~/…`.
//
// For display only — never for anything that will be opened, because `~` is
// the shell's expansion and not the filesystem's. Paths on these screens are
// there to be recognised ("yes, that is my Crush config") and to be pasted
// into an editor, and both work better at a third of the length. It also
// keeps the home directory out of anything somebody screenshots.
//
// Anything that is not under the home directory, or a home that cannot be
// determined, comes back untouched: a wrong abbreviation is worse than none.
func HomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, found := strings.CutPrefix(path, home+string(os.PathSeparator)); found {
		return "~" + string(os.PathSeparator) + rest
	}
	return path
}

// When parses a control-plane timestamp for ordering.
//
// The zero time for anything unparseable or absent, which sorts before every
// real date — so "never used" collects at one end of a column rather than
// scattering through it. Display code keeps its own formatting; this exists
// only so a column of dates sorts as dates and not as the strings they were
// rendered into.
func When(ts string) time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Percent renders a fraction as a percentage, with a decimal below ten so a
// cap at 4.8% does not read as the same thing as one at 5%.
func Percent(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "-"
	}
	p := f * 100
	if p < 10 {
		return strconv.FormatFloat(p, 'f', 1, 64) + "%"
	}
	return strconv.FormatFloat(p, 'f', 0, 64) + "%"
}

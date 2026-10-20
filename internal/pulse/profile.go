package pulse

import "time"

// shorten keeps the first six characters of an identifier: enough to
// recognise it, not enough to use it. A screen is shared, recorded and
// screenshotted more often than not.
func shorten(value string) string {
	const keep = 6
	runes := []rune(value)
	if len(runes) <= keep {
		return "..."
	}
	return string(runes[:keep]) + "..."
}

// day is an RFC 3339 time as a date, or the text as it came if it is not one.
func day(value string) string {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.Local().Format("2 Jan 2006")
	}
	return value
}

package pulse

import "strings"

// Profile shows the member's record as the platform sends it, and these two
// decide how a field reads: its name in words, and its value shortened when
// it identifies somebody.

// sensitivePatterns are the field names whose values are cut short. A
// screen is shared, recorded and screenshotted more often than not, and an
// id or an address is the part of a profile nobody needs to see whole.
var sensitivePatterns = []string{
	"id", "token", "secret", "key", "password",
	"email", "mail", "session", "auth", "hash",
	"discord", "guild", "user",
}

func isSensitive(key string) bool {
	lower := strings.ToLower(key)
	for _, p := range sensitivePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// censor keeps the first six characters of a sensitive value: enough to
// recognise it, not enough to use it.
func censor(key, value string) string {
	if !isSensitive(key) {
		return value
	}
	const keep = 6
	runes := []rune(value)
	if len(runes) <= keep {
		return "..."
	}
	return string(runes[:keep]) + "..."
}

// humanKey turns a field name into words: "userUUID" is "User UUID", not
// "User U U I D". A run of capitals is one word, and it ends where a
// lowercase letter starts a new one.
func humanKey(s string) string {
	runes := []rune(s)
	var out []rune
	for i, r := range runes {
		if i > 0 && isUpper(r) {
			startsWord := !isUpper(runes[i-1])
			endsRun := i+1 < len(runes) && !isUpper(runes[i+1])
			if startsWord || endsRun {
				out = append(out, ' ')
			}
		}
		out = append(out, r)
	}
	s = string(out)
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

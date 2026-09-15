package auth

import (
	"strings"
	"testing"
)

// A sign-in link is a credential in a URL, and this is the only place that
// decides what counts as one. A link from anywhere else carries a token this
// exchange cannot spend, so the answer is no before anything is posted
// anywhere - and the refusal names the host rather than echoing the link,
// because the message is rendered in the panel and pasted into issues.
func TestOnlyNanLinksAreAccepted(t *testing.T) {
	for _, c := range []struct {
		name   string
		pasted string
		want   string
	}{
		{"the platform", "https://nan.builders/auth/verify?token=abc", "abc"},
		{"a subdomain of it", "https://cloud-api.nan.builders/api/auth/login/verify?token=abc", "abc"},
		{"the host in caps", "https://NAN.BUILDERS/auth/verify?token=abc", "abc"},
		{"a bare token", "abc", "abc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := TokenFromLink(c.pasted)
			if err != nil {
				t.Fatalf("refused one of ours: %v", err)
			}
			if got != c.want {
				t.Errorf("token = %q, want %q", got, c.want)
			}
		})
	}

	for _, c := range []struct {
		name   string
		pasted string
	}{
		{"somebody else's domain", "https://nan.builders.evil.example/verify?token=abc"},
		{"a lookalike", "https://nan-builders.example/verify?token=abc"},
		{"a name ours is a suffix of", "https://notnan.builders/verify?token=abc"},
		{"plain http on an unrelated host", "http://localhost:8080/verify?token=abc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			token, err := TokenFromLink(c.pasted)
			if err == nil {
				t.Fatalf("took a token from %s", c.pasted)
			}
			if token != "" {
				t.Errorf("returned %q anyway", token)
			}
		})
	}
}

// The error is shown on screen and copied into issues, so it says which host
// it got rather than handing the whole link - and whatever is in its query -
// along with it.
func TestLinkErrorsDoNotEchoTheLink(t *testing.T) {
	const secret = "a-token-nobody-should-see"
	for _, pasted := range []string{
		"https://nan.builders/auth/verify#token=" + secret,
		"https://elsewhere.example/verify?token=" + secret,
	} {
		if _, err := TokenFromLink(pasted); err == nil {
			t.Fatalf("accepted %s", pasted)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("the error repeats the token: %v", err)
		}
	}
}

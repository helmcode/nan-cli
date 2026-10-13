package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
			got, err := New("").TokenFromLink(c.pasted)
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
			token, err := New("").TokenFromLink(c.pasted)
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
		if _, err := New("").TokenFromLink(pasted); err == nil {
			t.Fatalf("accepted %s", pasted)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("the error repeats the token: %v", err)
		}
	}
}

// A link that expired and a link somebody already spent arrive here as the same
// slug, and the member on the other end of it is the one whose email turned up
// late. The answer has to name the clock and what to do about it, or the only
// thing left to try is pasting the same dead link again. A reason from a newer
// platform than this build still gets said, rather than swallowed.
func TestRefusedLinkSaysWhatToDoAboutIt(t *testing.T) {
	for _, c := range []struct {
		name   string
		reason string
		want   string
	}{
		{"expired or already spent", "invalid_link", "15 minutes"},
		{"incomplete", "missing_token", "most recent email"},
		{"a reason this build does not know", "a_reason_from_tomorrow", "a reason from tomorrow"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := linkReasonMessage(c.reason)
			if !strings.Contains(got, c.want) {
				t.Errorf("linkReasonMessage(%q) = %q, want it to mention %q", c.reason, got, c.want)
			}
		})
	}
}

// The verify POST declares this client as the CLI (AGENT-AUTOMATIONS-PHASE2
// §11.3), so the session is recorded as a CLI session, never as a browser
// one.
func TestVerifyDeclaresTheCLI(t *testing.T) {
	var path string
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		form = r.PostForm
		http.SetCookie(w, &http.Cookie{Name: "nan_session", Value: "sess"})
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}))
	defer srv.Close()

	got, err := New("", WithBaseURL(srv.URL)).Verify(context.Background(), "tok")
	if err != nil || got != "sess" {
		t.Fatalf("%q %v", got, err)
	}
	if path != "/api/auth/login/verify" {
		t.Errorf("posted to %s", path)
	}
	if form.Get("token") != "tok" || form.Get("client") != "cli" || form.Has("flow") {
		t.Errorf("form = %v", form)
	}
}

// A refused link is followed by the platform's reason, not by a status code.
func TestVerifyReadsTheReasonOffTheRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/access-denied?reason=invalid_link", http.StatusSeeOther)
	}))
	defer srv.Close()

	_, err := New("", WithBaseURL(srv.URL)).Verify(context.Background(), "tok")
	if err == nil || !strings.Contains(err.Error(), "15 minutes") {
		t.Errorf("a spent link reads as %v", err)
	}
}

// Asking for a link: what is sent, and the one refusal worth its own words.
func TestRequestingALink(t *testing.T) {
	var body string
	status := http.StatusAccepted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = r.URL.Path + " " + string(raw)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := New("", WithBaseURL(srv.URL))

	if err := c.RequestMagicLink(context.Background(), "ana@acme.com"); err != nil {
		t.Fatal(err)
	}
	if body != `/api/auth/login/request {"email":"ana@acme.com"}` {
		t.Errorf("sent %s", body)
	}
	status = http.StatusTooManyRequests
	if err := c.RequestMagicLink(context.Background(), "ana@acme.com"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("a 429 reads as %v", err)
	}
}

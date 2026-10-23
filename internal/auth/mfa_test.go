package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/auth/authtest"
)

// fakePlatform starts the contract's fake and points the sign-in at it.
func fakePlatform(t *testing.T) *authtest.Platform {
	t.Helper()
	p := authtest.New(t)
	old := SignInBaseURL
	SignInBaseURL = p.URL
	t.Cleanup(func() { SignInBaseURL = old })
	return p
}

// challengeFor runs the link exchange against an account with a factor and
// returns the challenge it comes back with.
func challengeFor(t *testing.T, p *authtest.Platform) *MFARequiredError {
	t.Helper()
	p.Factor = true
	_, err := ExchangeToken(authtest.LinkToken)
	var ch *MFARequiredError
	if !errors.As(err, &ch) {
		t.Fatalf("ExchangeToken = %v, want a two-factor challenge", err)
	}
	return ch
}

// An account without a factor signs in exactly as before: one POST, the
// session from its cookie. The POST now says this build can answer a
// challenge, and that is the only thing that changed.
func TestNoFactorSignsInAsBefore(t *testing.T) {
	p := fakePlatform(t)
	got, err := ExchangeToken(authtest.LinkToken)
	if err != nil || got != authtest.Session {
		t.Fatalf("ExchangeToken = %q, %v", got, err)
	}
	forms := p.Verifies()
	if len(forms) != 1 {
		t.Fatalf("%d verify POSTs", len(forms))
	}
	f := forms[0]
	if f.Get("token") != authtest.LinkToken || f.Get("client") != "cli" || f.Get("mfa") != "1" || f.Has("flow") {
		t.Errorf("verify form = %v", f)
	}
	if n := len(p.Challenges()); n != 0 {
		t.Errorf("%d challenge POSTs for an account without a factor", n)
	}
}

// The new CLI announces it can answer, so a platform that follows the
// contract never sends it the "update nan-cli" refusal that older builds get.
func TestTheNewCLINeverMeetsTheUpdateRefusal(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	if ch == nil || p.Verifies()[0].Get("mfa") != "1" {
		t.Fatal("no challenge")
	}
	// And should it meet it anyway, it says what to do in a sentence.
	msg := linkReasonMessage("mfa_enabled_update_nan_cli")
	for _, want := range []string{"two-factor", "update", "new link"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not mention %q", msg, want)
		}
	}
}

func TestRightCodeSignsIn(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)

	in, err := ch.Complete(" 246 810 ")
	if err != nil {
		t.Fatal(err)
	}
	if in.Session != authtest.Session || in.UsedRecoveryCode {
		t.Errorf("SignIn = %+v", in)
	}

	reqs := p.Challenges()
	if len(reqs) != 1 {
		t.Fatalf("%d challenge POSTs", len(reqs))
	}
	r := reqs[0]
	if r.Cookie != authtest.Pending {
		t.Errorf("challenge cookie = %q", r.Cookie)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	// Not a browser, and must not pass for one: the platform lets an
	// Origin-less request through only because the cookie proves who opened
	// the challenge.
	for _, h := range []string{"Origin", "Sec-Fetch-Site", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			t.Errorf("sent %s: %q", h, v)
		}
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil || len(body) != 1 || body["code"] != "246810" {
		t.Errorf("body = %s", r.Body)
	}
}

func TestWrongCodeThenRight(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)

	_, err := ch.Complete("111111")
	var refused *MFAError
	if !errors.As(err, &refused) || refused.Code != "mfa_code_invalid" {
		t.Fatalf("err = %v", err)
	}
	if !Retryable(err) {
		t.Error("a wrong code ends the sign-in")
	}
	if !strings.Contains(err.Error(), "4 attempts left") {
		t.Errorf("%q does not say how many tries are left", err)
	}

	in, err := ch.Complete(authtest.Code)
	if err != nil || in.Session != authtest.Session {
		t.Fatalf("second try: %+v %v", in, err)
	}
}

// A recovery code is read the way the platform reads it - any case, with or
// without hyphens and spaces - and sent as one, and the answer says how many
// are left.
func TestRecoveryCodeSignsInAndCountsWhatIsLeft(t *testing.T) {
	for _, typed := range []string{"q7wa-z2xd-pl4r-6wze", "Q7WA Z2XD PL4R 6WZE", "q7waz2xdpl4r6wze"} {
		t.Run(typed, func(t *testing.T) {
			p := fakePlatform(t)
			p.RecoveryCodes = []string{"aaaa-bbbb-cccc-dddd", "q7wa-z2xd-pl4r-6wze"}
			ch := challengeFor(t, p)

			in, err := ch.Complete(typed)
			if err != nil {
				t.Fatal(err)
			}
			if !in.UsedRecoveryCode || in.RecoveryCodesLeft != 1 || in.Session != authtest.Session {
				t.Errorf("SignIn = %+v", in)
			}
			var body map[string]string
			_ = json.Unmarshal([]byte(p.Challenges()[0].Body), &body)
			if len(body) != 1 || body["recovery_code"] != "q7wa-z2xd-pl4r-6wze" {
				t.Errorf("body = %s", p.Challenges()[0].Body)
			}
		})
	}
}

func TestUsedRecoveryCodeIsRefused(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	_, err := ch.Complete("zzzz-zzzz-zzzz-zzzz")
	var refused *MFAError
	if !errors.As(err, &refused) || refused.Code != "mfa_recovery_code_invalid" || !Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "already used") {
		t.Errorf("%q", err)
	}
}

// The same 30-second code twice: the platform keeps one use per step, and the
// member is told to wait for the next.
func TestCodeAlreadyUsed(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	if _, err := ch.Complete(authtest.Code); err != nil {
		t.Fatal(err)
	}
	p.NewLink()
	ch = challengeFor(t, p)
	_, err := ch.Complete(authtest.Code)
	var refused *MFAError
	if !errors.As(err, &refused) || refused.Code != "mfa_code_already_used" || !Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "wait for the next one") {
		t.Errorf("%q", err)
	}
}

// Five wrong codes end the sign-in, and the last answer says so.
func TestFifthWrongCodeEndsTheSignIn(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	var err error
	for i := 0; i < 5; i++ {
		_, err = ch.Complete("000000")
	}
	var refused *MFAError
	if !errors.As(err, &refused) || refused.Code != "mfa_challenge_exhausted" || Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "new link") {
		t.Errorf("%q", err)
	}
}

func TestExpiredChallenge(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	p.Expire()
	_, err := ch.Complete(authtest.Code)
	var refused *MFAError
	if !errors.As(err, &refused) || refused.Code != "mfa_challenge_expired" || Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "10 minutes") || !strings.Contains(err.Error(), "new link") {
		t.Errorf("%q", err)
	}
}

// A lock is said with its end, by the platform's clock and the seconds it
// counts, and it ends this sign-in.
func TestLockedAccount(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	until := time.Now().Add(14*time.Minute + 30*time.Second)
	p.Lock(until)

	_, err := ch.Complete(authtest.Code)
	var refused *MFAError
	if !errors.As(err, &refused) || refused.Code != "mfa_locked" || Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	if refused.Status != http.StatusTooManyRequests || refused.RetryAfter < 14*time.Minute {
		t.Errorf("refusal = %+v", refused)
	}
	msg := err.Error()
	for _, want := range []string{"paused", until.Local().Format("15:04"), "15 minutes from now"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not say %q", msg, want)
		}
	}
}

// Every refusal in the contract, as the platform sends it: whether the same
// sign-in can take another answer, and what the member is told.
func TestEveryRefusalInTheContract(t *testing.T) {
	for _, c := range []struct {
		name      string
		refusal   authtest.Refusal
		code      string
		retryable bool
		want      []string
	}{
		{"wrong code", r(400, `{"error":"mfa_code_invalid","message":"x","attempts_remaining":1}`), "mfa_code_invalid", true, []string{"not right", "1 attempt left"}},
		{"wrong recovery code", r(400, `{"error":"mfa_recovery_code_invalid","message":"x","attempts_remaining":3}`), "mfa_recovery_code_invalid", true, []string{"recovery code", "3 attempts left"}},
		{"replayed code", r(400, `{"error":"mfa_code_already_used","message":"x","attempts_remaining":2}`), "mfa_code_already_used", true, []string{"next one", "2 attempts left"}},
		{"unreadable", r(400, `{"error":"invalid_request","message":"x"}`), "invalid_request", true, []string{"6-digit"}},
		{"exhausted", r(403, `{"error":"mfa_challenge_exhausted","message":"x"}`), "mfa_challenge_exhausted", false, []string{"too many wrong codes", "new link"}},
		{"expired", r(403, `{"error":"mfa_challenge_expired","message":"x"}`), "mfa_challenge_expired", false, []string{"timed out", "new link"}},
		{"used", r(403, `{"error":"mfa_challenge_used","message":"x"}`), "mfa_challenge_used", false, []string{"already over", "new link"}},
		{"missing", r(403, `{"error":"mfa_challenge_missing","message":"x"}`), "mfa_challenge_missing", false, []string{"no record", "new link"}},
		{"refused account", r(403, `{"error":"login_refused","message":"x"}`), "login_refused", false, []string{"refused the sign-in", "new link"}},
		{"locked, body only", r(429, `{"error":"mfa_locked","message":"x","retry_after":600}`), "mfa_locked", false, []string{"paused for 10 minutes"}},
		{"rate limited", rh(429, `{"error":"rate_limited","message":"x","retry_after":60}`, "Retry-After", "60"), "rate_limited", false, []string{"wait 1 minute", "new link"}},
		{"rate limited, header only", rh(429, `{"error":"rate_limited","message":"x"}`, "Retry-After", "45"), "rate_limited", false, []string{"wait 45 seconds"}},
		{"another origin", r(403, `{"error":"forbidden_origin"}`), "forbidden_origin", false, []string{"forbidden origin", "HTTP 403"}},
		{"server error", r(500, `{"error":"internal_error","message":"x"}`), "internal_error", false, []string{"could not check the code", "HTTP 500"}},
		{"two-factor turned off", r(404, "404 page not found\n"), "", false, []string{"HTTP 404", "new link"}},
		{"not our body", r(502, "<html>"+authtest.Pending+"\x1b[31m</html>"), "", false, []string{"HTTP 502"}},
		{"a code that is not one", r(403, `{"error":"Bad \u001b[2J`+authtest.Pending+`"}`), "", false, []string{"HTTP 403"}},
		{"a code from tomorrow", r(403, `{"error":"mfa_something_new","message":"x"}`), "mfa_something_new", false, []string{"mfa something new"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := fakePlatform(t)
			ch := challengeFor(t, p)
			p.Next = &c.refusal
			_, err := ch.Complete(authtest.Code)
			var refused *MFAError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v (%T)", err, err)
			}
			if refused.Code != c.code {
				t.Errorf("code = %q, want %q", refused.Code, c.code)
			}
			if Retryable(err) != c.retryable {
				t.Errorf("Retryable = %v, want %v", !c.retryable, c.retryable)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q does not say %q", err, want)
				}
			}
			if strings.Contains(err.Error(), authtest.Pending) || strings.ContainsRune(err.Error(), '\x1b') {
				t.Errorf("the message carries what the body did: %q", err)
			}
		})
	}
}

func r(status int, body string) authtest.Refusal {
	return authtest.Refusal{Status: status, Body: body, Header: map[string]string{"Content-Type": "application/json"}}
}

func rh(status int, body, k, v string) authtest.Refusal {
	out := r(status, body)
	out.Header[k] = v
	return out
}

// Something that is neither a code nor a recovery code is refused here, and
// spends nothing: no request, no attempt.
func TestNotACodeIsNeverSent(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)
	for _, typed := range []string{"", "12345", "1234567", "abcdef", "q7wa-z2xd-pl4r", "q7wa-z2xd-pl4r-6wze-xxxx", "246810\x00", "２４６８１０",
		// Sixteen characters, but not the alphabet recovery codes are made
		// in: RFC 4648 base32 has no 0, 1, 8 or 9.
		"q7wa-z2xd-pl4r-6wz0", "q7wa-z2xd-pl4r-6wz1", "Q7WA Z2XD PL4R 6WZ8", "q7waz2xdpl4r6wz9"} {
		if _, err := ch.Complete(typed); !errors.Is(err, ErrNotACode) || !Retryable(err) {
			t.Errorf("Complete(%q) = %v", typed, err)
		}
	}
	if n := len(p.Challenges()); n != 0 {
		t.Errorf("%d requests for input that is not a code", n)
	}
}

func TestNormalizeAnswer(t *testing.T) {
	for _, c := range []struct{ in, field, value string }{
		{"123456", "code", "123456"},
		{" 123 456 ", "code", "123456"},
		{"123-456", "code", "123456"},
		{"q7wa-z2xd-pl4r-6wze", "recovery_code", "q7wa-z2xd-pl4r-6wze"},
		{"Q7WAZ2XDPL4R6WZE", "recovery_code", "q7wa-z2xd-pl4r-6wze"},
		{"q7wa z2xd\tpl4r 6wze", "recovery_code", "q7wa-z2xd-pl4r-6wze"},
	} {
		field, value, err := normalizeAnswer(c.in)
		if err != nil || field != c.field || value != c.value {
			t.Errorf("normalizeAnswer(%q) = %q %q %v", c.in, field, value, err)
		}
	}
}

// A redirect that asks for the code without the challenge to answer it with
// is a dead end, and says so instead of prompting for nothing.
func TestMFAReasonWithoutTheCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, authtest.FrontendURL+"/mfa?reason=mfa_required", http.StatusFound)
	}))
	defer srv.Close()
	old := SignInBaseURL
	SignInBaseURL = srv.URL
	defer func() { SignInBaseURL = old }()

	_, err := ExchangeToken(authtest.LinkToken)
	var ch *MFARequiredError
	if err == nil || errors.As(err, &ch) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "new link") {
		t.Errorf("%q", err)
	}
}

// The challenge cookie is a credential for ten minutes. However the
// challenge is printed or logged, the value is not in it.
func TestTheChallengeNeverPrintsItsCookie(t *testing.T) {
	p := fakePlatform(t)
	ch := challengeFor(t, p)

	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x"} {
		out = append(out, fmt.Sprintf(verb, ch), fmt.Sprintf(verb, error(ch)))
	}
	out = append(out, ch.Error(), fmt.Errorf("wrapped: %w", ch).Error())
	if b, err := json.Marshal(ch); err == nil {
		out = append(out, string(b))
	}
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("sign-in", "err", ch, "challenge", ch)
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("sign-in", "err", ch, "challenge", ch)
	out = append(out, logged.String())

	for _, s := range out {
		if strings.Contains(s, authtest.Pending) {
			t.Errorf("the challenge cookie is in %q", s)
		}
	}
}

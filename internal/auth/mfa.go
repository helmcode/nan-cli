package auth

// Two-factor sign-in.
//
// A member who has turned on two-factor authentication does not get a session
// from the link alone. The verify POST answers with a challenge instead - a
// cookie that names it, and a redirect whose reason says so - and the session
// comes back only once a code from the member's authenticator app, or one of
// their recovery codes, is posted against that challenge.
//
// The challenge cookie is a credential for the next ten minutes, so it lives
// in memory and nowhere else: not in session.json, not in an error message,
// not in a log. A process that exits halfway leaves it to expire on the
// platform, and the member asks for a new link.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	mfaPendingCookie  = "__Host-nan_mfa_pending"
	mfaRequiredReason = "mfa_required"

	// What ends every message about a sign-in that cannot go on. The link was
	// spent when the challenge was opened, so a new one is the only way back.
	newLinkHint = "ask for a new link and sign in again"
)

func mfaChallengeURL() string { return Origin + "/api/auth/mfa/challenge" }

// MFARequiredError is what ExchangeToken returns when the link was good and
// the account asks for a second factor. Answer it with Complete.
type MFARequiredError struct {
	pending string
}

func (e *MFARequiredError) Error() string {
	return "this account uses two-factor authentication: enter the 6-digit code from your authenticator app, or one of your recovery codes"
}

// Format prints the message whatever the verb. Without it %#v - or %d, or
// anything else fmt does not route to Error - prints the struct, and the
// struct is the challenge.
func (e *MFARequiredError) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, e.Error()) }

// SignIn is a challenge answered.
type SignIn struct {
	Session string
	// UsedRecoveryCode is set when the answer was a recovery code, and
	// RecoveryCodesLeft is then how many the member still has.
	UsedRecoveryCode  bool
	RecoveryCodesLeft int
}

// ErrNotACode is input that is neither shape the platform takes, refused here
// without spending one of the sign-in's attempts on it.
var ErrNotACode = errors.New("that is neither a 6-digit code nor a recovery code (xxxx-xxxx-xxxx-xxxx) — " +
	"type the code your authenticator app shows for nan.builders")

// MFAError is the platform refusing a code, or the challenge it was posted to.
type MFAError struct {
	// The platform's code for it, e.g. "mfa_code_invalid". Empty when the
	// answer carried none.
	Code       string
	Status     int
	Attempts   int // attempts left on this sign-in, for a wrong code
	LockedTill time.Time
	RetryAfter time.Duration
}

// Retryable reports whether the same challenge can take another answer: the
// code was wrong, already used or not a code at all, and the sign-in is still
// open. Anything else ends it, and the member needs a new link.
func Retryable(err error) bool {
	if errors.Is(err, ErrNotACode) {
		return true
	}
	var e *MFAError
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case "mfa_code_invalid", "mfa_recovery_code_invalid", "mfa_code_already_used",
		// Nothing was spent: the platform could not read what was sent.
		"invalid_request":
		return true
	}
	return false
}

func (e *MFAError) Error() string {
	switch e.Code {
	case "mfa_code_invalid":
		return "that code is not right" + attemptsLeft(e.Attempts)
	case "mfa_recovery_code_invalid":
		return "that recovery code is not right, or it was already used" + attemptsLeft(e.Attempts)
	case "mfa_code_already_used":
		return "that code was already used — wait for the next one in your authenticator app" + attemptsLeft(e.Attempts)
	case "invalid_request":
		return "nan.builders could not read that — type the 6-digit code from your authenticator app, or a recovery code (xxxx-xxxx-xxxx-xxxx)"
	case "mfa_challenge_exhausted":
		return "too many wrong codes for this sign-in — " + newLinkHint
	case "mfa_challenge_expired":
		return "this sign-in timed out: the code has to be entered within 10 minutes of using the link — " + newLinkHint
	case "mfa_challenge_used":
		return "this sign-in is already over (it was finished, or a newer sign-in replaced it) — " + newLinkHint
	case "mfa_challenge_missing":
		return "nan.builders has no record of this sign-in — " + newLinkHint
	case "login_refused":
		return "nan.builders refused the sign-in for this account — " + newLinkHint
	case "mfa_locked":
		return "too many wrong codes on this account: two-factor sign-in is paused " + e.pausedFor() + " — then " + newLinkHint
	case "rate_limited":
		return "too many sign-in attempts from this network — wait " + e.wait() + " and " + newLinkHint
	}
	if e.Code == "" || e.Status >= 500 {
		return fmt.Sprintf("nan.builders could not check the code (HTTP %d) — %s", e.Status, newLinkHint)
	}
	// A code from a newer platform than this build still gets said, rather
	// than swallowed.
	return fmt.Sprintf("nan.builders refused the code: %s (HTTP %d) — %s",
		strings.ReplaceAll(e.Code, "_", " "), e.Status, newLinkHint)
}

// attemptsLeft is the tail of a wrong-code message. The platform always says
// how many are left on those; a body without the number says nothing rather
// than "0 attempts left", which would read as the end of the sign-in.
func attemptsLeft(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return " (1 attempt left)"
	}
	return fmt.Sprintf(" (%d attempts left)", n)
}

// now is the clock the lock message is read against. A variable for the tests.
var now = time.Now

// pausedFor says when the lock ends. locked_until is the platform's clock and
// retry_after the seconds it counts from now; the second is what the
// countdown uses, so a machine whose clock is off still waits the right time.
func (e *MFAError) pausedFor() string {
	if e.LockedTill.IsZero() {
		return "for " + e.wait()
	}
	until, today := e.LockedTill.Local(), now().Local()
	layout := "15:04"
	if until.YearDay() != today.YearDay() || until.Year() != today.Year() {
		layout = "Mon 2 Jan 15:04"
	}
	if e.RetryAfter <= 0 {
		return "until " + until.Format(layout)
	}
	return "until " + until.Format(layout) + " (" + e.wait() + " from now)"
}

func (e *MFAError) wait() string {
	d := e.RetryAfter
	if d <= 0 {
		return "a few minutes"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	mins := int((d + time.Minute - 1) / time.Minute)
	if mins == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", mins)
}

// normalizeAnswer decides what the member typed, the way the platform does: six
// digits once the spaces are out is an authenticator code, and sixteen
// characters of the RFC 4648 base32 alphabet (A-Z and 2-7) a recovery code - in
// any case, with or without its hyphens. That is the alphabet the platform
// makes them in, so a 0, 1, 8 or 9 is a typo, refused before it costs one of
// the sign-in's attempts.
func normalizeAnswer(input string) (field, value string, err error) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '-':
			return -1
		}
		return r
	}, strings.TrimSpace(input))

	if len(compact) == 6 && allASCII(compact, isDigit) {
		return "code", compact, nil
	}
	if len(compact) == 16 && allASCII(compact, isBase32) {
		c := strings.ToLower(compact)
		return "recovery_code", c[0:4] + "-" + c[4:8] + "-" + c[8:12] + "-" + c[12:16], nil
	}
	return "", "", ErrNotACode
}

// isErrorCode is the platform's error-code shape, ^[a-z0-9_]+$.
func isErrorCode(s string) bool {
	return s != "" && len(s) <= 64 && allASCII(s, func(b byte) bool {
		return isDigit(b) || (b >= 'a' && b <= 'z') || b == '_'
	})
}

func allASCII(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// isBase32 is the RFC 4648 alphabet, in either case.
func isBase32(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '2' && b <= '7')
}

// Complete answers the challenge with what the member typed: the code their
// authenticator app shows, or one of their recovery codes. It returns the
// session on success, and an *MFAError (or ErrNotACode) otherwise; Retryable
// says whether this same challenge can take another try.
func (e *MFARequiredError) Complete(input string) (*SignIn, error) {
	field, value, err := normalizeAnswer(input)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{field: value})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, mfaChallengeURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// JSON is what the platform requires here: a text/plain POST would skip a
	// browser's preflight, so anything else is refused before the code is
	// looked at. No Origin and no Sec-Fetch-Site - this is not a browser, and
	// the platform lets a request through without them only because the
	// challenge cookie can be held by nothing but the client that opened it.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{Name: mfaPendingCookie, Value: e.pending})

	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusOK {
		var ok struct {
			RecoveryCodesRemaining *int `json:"recovery_codes_remaining"`
		}
		_ = json.Unmarshal(raw, &ok)
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookie && c.Value != "" {
				in := &SignIn{Session: c.Value}
				if ok.RecoveryCodesRemaining != nil {
					in.UsedRecoveryCode = true
					in.RecoveryCodesLeft = *ok.RecoveryCodesRemaining
				}
				return in, nil
			}
		}
		return nil, errors.New("nan.builders accepted the code but sent no session back — " + newLinkHint)
	}

	var refusal struct {
		Error             string `json:"error"`
		AttemptsRemaining int    `json:"attempts_remaining"`
		LockedUntil       string `json:"locked_until"`
		RetryAfter        int    `json:"retry_after"`
	}
	_ = json.Unmarshal(raw, &refusal)
	out := &MFAError{Status: resp.StatusCode, Attempts: refusal.AttemptsRemaining}
	// Only something shaped like the platform's codes is taken as one: an
	// unknown code is printed, and a body that is not ours - a proxy's error
	// page, a 404 from a platform with two-factor sign-in turned off - should
	// not put whatever it carries on the member's terminal.
	if isErrorCode(refusal.Error) {
		out.Code = refusal.Error
	}
	if t, err := time.Parse(time.RFC3339, refusal.LockedUntil); err == nil {
		out.LockedTill = t
	}
	switch {
	case refusal.RetryAfter > 0:
		out.RetryAfter = time.Duration(refusal.RetryAfter) * time.Second
	default:
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			out.RetryAfter = time.Duration(s) * time.Second
		}
	}
	return nil, out
}

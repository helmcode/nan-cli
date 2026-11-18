package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/auth/authtest"
)

// signInAt points the sign-in at url for one test.
func signInAt(t *testing.T, url string) {
	t.Helper()
	old := SignInBaseURL
	SignInBaseURL = url
	t.Cleanup(func() { SignInBaseURL = old })
}

// fastClock makes every wait return at once and records what was asked for.
func fastClock(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	oldAfter := after
	after = func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	t.Cleanup(func() { after = oldAfter })
	return &waits
}

func started(t *testing.T, p *authtest.Platform) *DeviceLogin {
	t.Helper()
	signInAt(t, p.URL)
	d, err := StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return d
}

func TestStartSendsTheCLIClientAndReadsTheCodes(t *testing.T) {
	p := authtest.New(t)
	d := started(t, p)
	bodies := p.StartBodies()
	if len(bodies) != 1 || strings.ReplaceAll(bodies[0], " ", "") != `{"client":"cli"}` {
		t.Errorf("start bodies = %q", bodies)
	}
	if d.DeviceCode != authtest.DeviceCode || d.UserCode != authtest.UserCode {
		t.Errorf("codes = %+v", d)
	}
	if d.Interval != time.Second {
		t.Errorf("interval = %v", d.Interval)
	}
	if time.Until(d.ExpiresAt) < 14*time.Minute {
		t.Errorf("expires = %v", d.ExpiresAt)
	}
}

func TestStartDefaultsAZeroInterval(t *testing.T) {
	p := authtest.New(t)
	p.Interval = -1
	if d := started(t, p); d.Interval != 5*time.Second {
		t.Errorf("interval = %v", d.Interval)
	}
}

// A server that ignores the body would have started a Helmcode Desktop
// request, whose approval mints an inference key: no echo, no sign-in, and
// above all no poll.
func TestStartRefusesAServerThatDoesNotEchoTheClient(t *testing.T) {
	p := authtest.New(t)
	p.OmitClientKind = true
	signInAt(t, p.URL)
	if _, err := StartDeviceLogin(context.Background()); !errors.Is(err, ErrServerTooOld) {
		t.Fatalf("err = %v, want ErrServerTooOld", err)
	}
	if n := len(p.PollBodies()); n != 0 {
		t.Errorf("%d polls reached the platform", n)
	}
}

func TestStartErrors(t *testing.T) {
	for _, c := range []struct {
		status int
		want   string
	}{
		{http.StatusTooManyRequests, "too many sign-in attempts"},
		{http.StatusInternalServerError, "could not start the sign-in (HTTP 500)"},
		{http.StatusServiceUnavailable, "(HTTP 503)"},
	} {
		p := authtest.New(t)
		p.StartStatus = c.status
		signInAt(t, p.URL)
		_, err := StartDeviceLogin(context.Background())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %d: err = %v, want %q", c.status, err, c.want)
		}
	}
}

func TestStartNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	signInAt(t, srv.URL)
	srv.Close()
	_, err := StartDeviceLogin(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not reach nan.builders") {
		t.Errorf("err = %v", err)
	}
}

func TestStartRejectsAVerificationURLThatIsNotOurs(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example/api/auth/device",
		"https://nan.builders.evil.example/x",
		"javascript:alert(1)",
		"/relative",
	} {
		p := authtest.New(t)
		p.VerificationURL = bad
		// Base is http://127.0.0.1: only a 127.0.0.1 host or *.nan.builders
		// over https passes.
		signInAt(t, p.URL)
		if _, err := StartDeviceLogin(context.Background()); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// http is for a test server only: against an https base it is refused.
func TestStartRefusesPlainHTTPAgainstAnHTTPSBase(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"d","user_code":"u","verification_url":"http://cloud-api.nan.builders/x","expires_in":900,"interval":5,"client_kind":"cli"}`))
	}))
	t.Cleanup(srv.Close)
	signInAt(t, srv.URL)
	if err := checkVerificationURL("http://cloud-api.nan.builders/x"); err == nil {
		t.Error("accepted http against an https base")
	}
}

func TestStartAcceptsOurVerificationURLs(t *testing.T) {
	p := authtest.New(t)
	for _, good := range []string{
		"https://cloud-api.nan.builders/api/auth/device",
		"https://nan.builders/api/auth/device",
		p.URL + "/api/auth/device", // http, same host as a (test) base
	} {
		p.VerificationURL = good
		signInAt(t, p.URL)
		if _, err := StartDeviceLogin(context.Background()); err != nil {
			t.Errorf("refused %q: %v", good, err)
		}
	}
}

// The device_code is a bearer credential: a 307/308 would replay the POST
// body (the code included) at whatever the Location says.
func TestRedirectsAreNeverFollowed(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var hits atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		t.Cleanup(other.Close)
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/steal", code)
		}))
		t.Cleanup(redirector.Close)
		signInAt(t, redirector.URL)

		if _, err := StartDeviceLogin(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Errorf("%d start: err = %v", code, err)
		}
		fastClock(t)
		d := &DeviceLogin{DeviceCode: authtest.DeviceCode, ExpiresAt: time.Now().Add(time.Hour), Interval: time.Second}
		_, err := WaitForApproval(context.Background(), d)
		if err == nil || !strings.Contains(err.Error(), "redirect") || strings.Contains(err.Error(), authtest.DeviceCode) {
			t.Errorf("%d poll: err = %v", code, err)
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("%d: the redirect target received %d requests", code, n)
		}
	}
}

func TestPollPendingThenApprovedReturnsTheToken(t *testing.T) {
	p := authtest.New(t)
	p.Script(authtest.Pending, authtest.Pending, authtest.Approved("tok-1"))
	d := started(t, p)
	fastClock(t)
	res, err := WaitForApproval(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionToken != "tok-1" {
		t.Errorf("token = %q", res.SessionToken)
	}
	bodies := p.PollBodies()
	if len(bodies) != 3 || !strings.Contains(bodies[0], authtest.DeviceCode) {
		t.Errorf("polls = %q", bodies)
	}
}

func TestSlowDownAddsFiveSeconds(t *testing.T) {
	p := authtest.New(t)
	p.Script(authtest.SlowDown, authtest.TooManyRequests, authtest.Approved("t"))
	d := started(t, p)
	waits := fastClock(t)
	if _, err := WaitForApproval(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 6 * time.Second, 11 * time.Second}
	if len(*waits) != len(want) {
		t.Fatalf("waits = %v", *waits)
	}
	for i := range want {
		if (*waits)[i] != want[i] {
			t.Errorf("waits = %v, want %v", *waits, want)
			break
		}
	}
}

func TestPerIPLimiterCountsTowardTheTransientBudget(t *testing.T) {
	p := authtest.New(t)
	p.Script(authtest.TooManyRequests)
	d := started(t, p)
	fastClock(t)
	if _, err := WaitForApproval(context.Background(), d); err == nil {
		t.Fatal("kept going forever")
	}
	if n := len(p.PollBodies()); n != 3 {
		t.Errorf("polls = %d, want 3", n)
	}
}

func TestDeniedAndExpired(t *testing.T) {
	for _, c := range []struct {
		step authtest.Step
		want error
	}{
		{authtest.Denied, ErrDenied},
		{authtest.ExpiredS, ErrExpired},
	} {
		p := authtest.New(t)
		p.Script(authtest.Pending, c.step)
		d := started(t, p)
		fastClock(t)
		if _, err := WaitForApproval(context.Background(), d); !errors.Is(err, c.want) {
			t.Errorf("err = %v, want %v", err, c.want)
		}
	}
}

func TestExpiredLocally(t *testing.T) {
	p := authtest.New(t)
	d := started(t, p)
	d.ExpiresAt = time.Now().Add(-time.Second)
	fastClock(t)
	if _, err := WaitForApproval(context.Background(), d); !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v", err)
	}
	if n := len(p.PollBodies()); n != 0 {
		t.Errorf("polled %d times after the deadline", n)
	}
}

func TestApprovedWithoutASessionIsAnError(t *testing.T) {
	p := authtest.New(t)
	p.Script(authtest.Step{Body: `{"status":"approved"}`})
	d := started(t, p)
	fastClock(t)
	if _, err := WaitForApproval(context.Background(), d); err == nil {
		t.Error("accepted an approval with no session")
	}
}

func TestContextCancelStopsPollingPromptly(t *testing.T) {
	p := authtest.New(t)
	d := started(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	d.Interval = time.Hour // the real clock: only the cancel can end this
	done := make(chan error, 1)
	go func() {
		_, err := WaitForApproval(ctx, d)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop")
	}
}

// An approval that arrives together with a cancel loses: nothing is returned.
func TestCancelDuringTheApprovedResponseWins(t *testing.T) {
	p := authtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	p.OnPoll = func(int) { cancel() }
	p.Script(authtest.Approved("tok"))
	d := started(t, p)
	fastClock(t)
	res, err := WaitForApproval(ctx, d)
	if res != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("res=%v err=%v", res, err)
	}
}

func TestTransientNetworkErrorsAreRetriedAtMostThreeTimes(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Drop the connection without an answer.
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close()
	}))
	t.Cleanup(srv.Close)
	signInAt(t, srv.URL)
	fastClock(t)
	d := &DeviceLogin{DeviceCode: authtest.DeviceCode, ExpiresAt: time.Now().Add(time.Hour), Interval: time.Second}
	_, err := WaitForApproval(context.Background(), d)
	if err == nil || strings.Contains(err.Error(), authtest.DeviceCode) {
		t.Fatalf("err = %v", err)
	}
	if n := hits.Load(); n != 3 {
		t.Errorf("hits = %d, want 3", n)
	}
}

func TestATransientErrorFollowedBySuccessResetsTheBudget(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch i := n.Add(1); {
		case i == 3 || i == 6:
			_, _ = w.Write([]byte(`{"status":"approved","session_token":"ok"}`))
		case i%3 == 1 || i%3 == 2:
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close()
		}
	}))
	t.Cleanup(srv.Close)
	signInAt(t, srv.URL)
	fastClock(t)
	d := &DeviceLogin{DeviceCode: "x", ExpiresAt: time.Now().Add(time.Hour), Interval: time.Second}
	res, err := WaitForApproval(context.Background(), d)
	if err != nil || res.SessionToken != "ok" {
		t.Errorf("res=%v err=%v", res, err)
	}
}

func TestServerErrorsEndThePollWithTheCodeOnly(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"secret detail"}`))
		}))
		signInAt(t, srv.URL)
		fastClock(t)
		d := &DeviceLogin{DeviceCode: authtest.DeviceCode, ExpiresAt: time.Now().Add(time.Hour), Interval: time.Second}
		_, err := WaitForApproval(context.Background(), d)
		if err == nil || !strings.Contains(err.Error(), "HTTP ") || strings.Contains(err.Error(), "secret detail") || strings.Contains(err.Error(), authtest.DeviceCode) {
			t.Errorf("%d: err = %v", code, err)
		}
		srv.Close()
	}
}

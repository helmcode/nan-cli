package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock replaces the package's time for a test: waits return at once and are
// recorded, and now moves forward by what was waited.
type clock struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
}

func fakeClock(t *testing.T) *clock {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	oldNow, oldAfter := deviceNow, deviceAfter
	deviceNow = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
	deviceAfter = func(d time.Duration) <-chan time.Time {
		c.mu.Lock()
		c.t = c.t.Add(d)
		c.waits = append(c.waits, d)
		now := c.t
		c.mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- now
		return ch
	}
	t.Cleanup(func() { deviceNow, deviceAfter = oldNow, oldAfter })
	return c
}

// scripted answers the poll route one scripted reply at a time and records
// what the platform saw.
type reply struct {
	status int
	body   string
}

type platform struct {
	*httptest.Server
	mu      sync.Mutex
	replies []reply
	polls   int
	starts  []map[string]string
	polled  []string
	start   string
}

func newPlatform(t *testing.T, replies ...reply) *platform {
	t.Helper()
	p := &platform{replies: replies}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/device/start", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.starts = append(p.starts, body)
		start := p.start
		p.mu.Unlock()
		if start == "" {
			start = fmt.Sprintf(`{"device_code":"dev-secret","user_code":"ABCD-EFGH","verification_url":"%s/device","expires_in":900,"interval":5,"client_kind":"cli"}`, p.URL)
		}
		_, _ = io.WriteString(w, start)
	})
	mux.HandleFunc("POST /api/auth/device/poll", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.polled = append(p.polled, body["device_code"])
		var rep reply
		if p.polls < len(p.replies) {
			rep = p.replies[p.polls]
		} else {
			rep = p.replies[len(p.replies)-1]
		}
		p.polls++
		p.mu.Unlock()
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func (p *platform) client() *Client { return New("", WithBaseURL(p.URL)) }

func (p *platform) pollCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.polls
}

func approved() reply {
	return reply{200, `{"status":"approved","session_token":"session-from-platform","region":"eu"}`}
}

func pending() reply { return reply{200, `{"status":"pending"}`} }

func startDevice(t *testing.T, p *platform) *Device {
	t.Helper()
	d, err := p.client().StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestStartDeclaresTheCLIAndReadsTheRequest(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, pending())
	d := startDevice(t, p)
	if len(p.starts) != 1 || p.starts[0]["client"] != "cli" {
		t.Errorf("start body = %v, want client=cli", p.starts)
	}
	if d.UserCode != "ABCD-EFGH" || d.DeviceCode != "dev-secret" || d.Interval != 5*time.Second {
		t.Errorf("device = %+v", d)
	}
	if want := deviceNow().Add(15 * time.Minute); !d.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v", d.ExpiresAt, want)
	}
}

func TestStartRefusesAPlatformThatDoesNotEchoTheCLI(t *testing.T) {
	for _, kind := range []string{"", "desktop"} {
		t.Run("client_kind="+kind, func(t *testing.T) {
			p := newPlatform(t, pending())
			p.start = fmt.Sprintf(`{"device_code":"d","user_code":"U","verification_url":"%s/device","expires_in":900,"interval":5,"client_kind":%q}`, p.URL, kind)
			_, err := p.client().StartDevice(context.Background())
			if !errors.Is(err, ErrDeviceUnsupported) {
				t.Fatalf("err = %v, want ErrDeviceUnsupported", err)
			}
			if p.pollCount() != 0 {
				t.Error("polled a request that is not a CLI one")
			}
		})
	}
}

func TestStartDefaultsAMissingLifetimeAndInterval(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, pending())
	p.start = fmt.Sprintf(`{"device_code":"d","user_code":"U","verification_url":"%s/device","client_kind":"cli"}`, p.URL)
	d := startDevice(t, p)
	if d.Interval != 5*time.Second || !d.ExpiresAt.After(deviceNow()) {
		t.Errorf("device = %+v: a zero lifetime must not expire on the first tick", d)
	}
}

func TestStartOnlyAcceptsAVerificationPageOnThePlatform(t *testing.T) {
	for _, c := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"the portal", "https://cloud.nan.builders/device", true},
		{"the root", "https://nan.builders/device", true},
		{"the host in caps", "https://CLOUD.NAN.BUILDERS/device?code=A", true},
		{"a lookalike", "https://nan.builders.evil.example/device", false},
		{"a name ours is a suffix of", "https://notnan.builders/device", false},
		{"plain http", "http://cloud.nan.builders/device", false},
		{"a script scheme", "javascript:alert(1)", false},
		{"a control character", "https://cloud.nan.builders/device\n--flag", false},
		{"no host", "/device", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := New("", WithBaseURL("https://cloud-api.nan.builders")).checkVerificationURL(c.url)
			if c.ok && err != nil {
				t.Fatalf("refused %s: %v", c.url, err)
			}
			if !c.ok && err == nil {
				t.Fatalf("accepted %q", c.url)
			}
		})
	}
}

// A local stand-in over plain http may send its own address back.
func TestStartAcceptsTheLocalStandInOverHTTP(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, pending())
	if _, err := p.client().StartDevice(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusesARedirectAndNamesTheErrors(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if _, err := New("", WithBaseURL(redirect.URL)).StartDevice(context.Background()); !errors.Is(err, errDeviceRedirect) {
		t.Errorf("redirect: err = %v", err)
	}

	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	defer limited.Close()
	if _, err := New("", WithBaseURL(limited.URL)).StartDevice(context.Background()); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("429: err = %v", err)
	}
}

func TestWaitReturnsTheSessionOnApproval(t *testing.T) {
	c := fakeClock(t)
	p := newPlatform(t, pending(), pending(), approved())
	token, err := p.client().WaitForApproval(context.Background(), startDevice(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if token != "session-from-platform" {
		t.Errorf("token = %q", token)
	}
	for _, w := range c.waits {
		if w != 5*time.Second {
			t.Errorf("waited %v between polls, want the advertised 5s", w)
		}
	}
	for _, code := range p.polled {
		if code != "dev-secret" {
			t.Errorf("polled with %q", code)
		}
	}
}

func TestWaitEndsOnDeniedAndExpired(t *testing.T) {
	for _, c := range []struct {
		status string
		want   error
	}{{"denied", ErrDeviceDenied}, {"expired", ErrDeviceExpired}} {
		t.Run(c.status, func(t *testing.T) {
			fakeClock(t)
			p := newPlatform(t, pending(), reply{200, fmt.Sprintf(`{"status":%q}`, c.status)})
			_, err := p.client().WaitForApproval(context.Background(), startDevice(t, p))
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestSentinelErrorsCarryNoAdvice(t *testing.T) {
	for _, err := range []error{ErrDeviceDenied, ErrDeviceExpired, ErrDeviceUnsupported} {
		if strings.Contains(err.Error(), "run nan") || strings.Contains(err.Error(), "try again") {
			t.Errorf("%q tells the member what to do; each surface adds its own hint", err)
		}
	}
}

func TestSlowDownAddsFiveSeconds(t *testing.T) {
	c := fakeClock(t)
	p := newPlatform(t,
		reply{429, `{"error":"slow_down — poll at the advertised interval"}`},
		reply{200, `{"status":"slow_down"}`},
		approved())
	if _, err := p.client().WaitForApproval(context.Background(), startDevice(t, p)); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}
	if fmt.Sprint(c.waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", c.waits, want)
	}
}

// A 5xx or a network error is the platform or the path to it having a bad
// moment, not the sign-in failing: the member may be halfway through
// approving in the browser, and the request is still open.
func TestServerErrorsAreWaitedOutNotFatal(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504, 408} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := fakeClock(t)
			p := newPlatform(t,
				reply{status, `{"error":"internal error"}`},
				reply{status, ``},
				reply{status, `<html>bad gateway</html>`},
				reply{status, ``},
				reply{status, ``},
				approved())
			token, err := p.client().WaitForApproval(context.Background(), startDevice(t, p))
			if err != nil {
				t.Fatalf("HTTP %d ended the sign-in: %v", status, err)
			}
			if token != "session-from-platform" {
				t.Errorf("token = %q", token)
			}
			// Backs off while failing, never past the cap, and goes back to
			// the interval once the platform answers.
			if c.waits[1] <= c.waits[0] || c.waits[2] <= c.waits[1] {
				t.Errorf("waits %v do not grow while the platform fails", c.waits)
			}
			for _, w := range c.waits {
				if w > maxBackoff {
					t.Errorf("waited %v, past the cap %v", w, maxBackoff)
				}
			}
		})
	}
}

func TestAnUnreadableAnswerIsWaitedOut(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, reply{200, `<html>captive portal</html>`}, approved())
	if _, err := p.client().WaitForApproval(context.Background(), startDevice(t, p)); err != nil {
		t.Fatal(err)
	}
}

func TestANetworkErrorIsWaitedOut(t *testing.T) {
	fakeClock(t)
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/start") {
			_, _ = io.WriteString(w, `{"device_code":"d","user_code":"U","verification_url":"http://127.0.0.1/device","expires_in":900,"interval":5,"client_kind":"cli"}`)
			return
		}
		if n <= 3 {
			// Hang up without answering: a dropped connection.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"status":"approved","session_token":"s"}`)
	}))
	defer srv.Close()
	c := New("", WithBaseURL(srv.URL))
	d, err := c.StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token, err := c.WaitForApproval(context.Background(), d)
	if err != nil || token != "s" {
		t.Fatalf("token %q, err %v: a dropped connection ended the sign-in", token, err)
	}
}

func TestOtherClientErrorsStillEndTheWait(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			fakeClock(t)
			p := newPlatform(t, reply{status, `{"error":"invalid device_code"}`}, approved())
			_, err := p.client().WaitForApproval(context.Background(), startDevice(t, p))
			if err == nil {
				t.Fatalf("HTTP %d did not end the sign-in", status)
			}
			if p.pollCount() != 1 {
				t.Errorf("polled %d times after a permanent refusal", p.pollCount())
			}
			if strings.Contains(err.Error(), "dev-secret") {
				t.Error("the device code is in the error")
			}
		})
	}
}

func TestServerErrorsUntilExpiryEndWithTheLastErrorNotAHang(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, reply{503, ``})
	_, err := p.client().WaitForApproval(context.Background(), startDevice(t, p))
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err = %v, want the last 503 reported once the request expired", err)
	}
}

func TestPendingUntilExpiryIsExpired(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, pending())
	_, err := p.client().WaitForApproval(context.Background(), startDevice(t, p))
	if !errors.Is(err, ErrDeviceExpired) {
		t.Fatalf("err = %v", err)
	}
}

// The platform decides expiry. An approval that lands in the last interval is
// found by one more poll past the local deadline.
func TestAnApprovalInTheLastIntervalIsNotLost(t *testing.T) {
	c := fakeClock(t)
	p := newPlatform(t, pending())
	d := startDevice(t, p)
	// Pending until the local clock passes the deadline, then approved.
	p.mu.Lock()
	n := int(15*time.Minute/(5*time.Second)) + 1
	p.replies = make([]reply, n)
	for i := range p.replies {
		p.replies[i] = pending()
	}
	p.replies[n-1] = approved()
	p.mu.Unlock()
	token, err := p.client().WaitForApproval(context.Background(), d)
	if err != nil || token != "session-from-platform" {
		t.Fatalf("token %q, err %v: approved in the last interval but reported expired (waited %d polls)", token, err, len(c.waits))
	}
}

func TestCancellingStopsThePolling(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, pending())
	d := startDevice(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.client().WaitForApproval(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if p.pollCount() != 0 {
		t.Error("polled after being cancelled")
	}
}

func TestApprovalWithoutASessionIsAnError(t *testing.T) {
	fakeClock(t)
	p := newPlatform(t, reply{200, `{"status":"approved"}`})
	if _, err := p.client().WaitForApproval(context.Background(), startDevice(t, p)); err == nil {
		t.Fatal("an approval with no session was taken as a sign-in")
	}
}

func TestPollRefusesARedirect(t *testing.T) {
	fakeClock(t)
	var mu sync.Mutex
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		elsewhere++
		mu.Unlock()
	}))
	defer other.Close()
	p := newPlatform(t, pending())
	d := startDevice(t, p)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := New("", WithBaseURL(srv.URL)).WaitForApproval(context.Background(), d); !errors.Is(err, errDeviceRedirect) {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if elsewhere != 0 {
		t.Error("the device_code was sent to another host")
	}
}

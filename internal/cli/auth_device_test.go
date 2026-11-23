package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/api/authtest"
	"github.com/helmcode/nan-cli/internal/browser"
)

// instant is a clock whose waits return at once, so a poll loop runs in
// milliseconds.
func instant(p *authtest.Platform) *api.Client {
	return api.New("", api.WithBaseURL(p.URL), api.WithClock(time.Now, func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}))
}

func noBrowser(t *testing.T) *[]string {
	t.Helper()
	t.Setenv("NAN_NO_BROWSER", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	var opened []string
	old := browser.Open
	browser.Open = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { browser.Open = old })
	return &opened
}

func TestLoginShowsTheCodeOpensThePageAndSavesTheSession(t *testing.T) {
	home := fakeHome(t, "")
	opened := noBrowser(t)
	p := authtest.New(t)
	p.Approve()

	var out bytes.Buffer
	if err := deviceLogin(context.Background(), instant(p), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, authtest.UserCode) || !strings.Contains(got, p.URL+"/device") {
		t.Errorf("the code and the page are not on screen:\n%s", got)
	}
	if strings.Contains(out.String(), authtest.DeviceCode) {
		t.Error("the device code was printed")
	}
	if len(*opened) != 1 || (*opened)[0] != p.URL+"/device" {
		t.Errorf("opened %v", *opened)
	}
	if !strings.Contains(out.String(), "Tried to open it in your browser") {
		t.Errorf("the attempt is not said:\n%s", out.String())
	}
	if starts := p.Starts(); len(starts) != 1 || starts[0]["client"] != "cli" {
		t.Errorf("start bodies = %v", starts)
	}
	sess, mode := readSession(t, home)
	if sess["token"] != authtest.Session {
		t.Errorf("session.json = %v", sess)
	}
	if mode&0o077 != 0 {
		t.Errorf("session.json mode %v is readable by others", mode)
	}
}

func TestLoginDoesNotTryABrowserOverSSHOrWhenDisabled(t *testing.T) {
	for _, env := range []struct{ name, value string }{{"SSH_CONNECTION", "1.2.3.4 1 5.6.7.8 22"}, {"SSH_TTY", "/dev/pts/0"}, {"NAN_NO_BROWSER", "1"}} {
		t.Run(env.name, func(t *testing.T) {
			fakeHome(t, "")
			opened := noBrowser(t)
			t.Setenv(env.name, env.value)
			p := authtest.New(t)
			p.Approve()
			var out bytes.Buffer
			if err := deviceLogin(context.Background(), instant(p), &out); err != nil {
				t.Fatal(err)
			}
			if len(*opened) != 0 {
				t.Errorf("tried to open %v", *opened)
			}
			if strings.Contains(out.String(), "open it in your browser") {
				t.Errorf("says it opened a browser:\n%s", out.String())
			}
			if !strings.Contains(out.String(), p.URL+"/device") {
				t.Error("the address must be printed whatever happens to the browser")
			}
		})
	}
}

func TestLoginEndsWithAHintOnDenialAndExpiry(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*authtest.Platform)
		want error
		hint string
	}{
		{"denied", (*authtest.Platform).Deny, api.ErrDeviceDenied, ""},
		{"expired", (*authtest.Platform).Expire, api.ErrDeviceExpired, "nan auth login"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, "")
			noBrowser(t)
			p := authtest.New(t)
			c.set(p)
			err := deviceLogin(context.Background(), instant(p), &bytes.Buffer{})
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.hint) {
				t.Fatalf("err = %v", err)
			}
			if sess, _ := readSession(t, home); sess != nil && sess["token"] != nil {
				t.Errorf("a session was saved: %v", sess)
			}
		})
	}
}

// One 5xx is not the end of a 15-minute sign-in.
func TestLoginSurvivesServerErrorsWhilePolling(t *testing.T) {
	home := fakeHome(t, "")
	noBrowser(t)
	p := authtest.New(t)
	p.FailNext(502, 3)
	p.Approve()
	if err := deviceLogin(context.Background(), instant(p), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if p.Polls() < 4 {
		t.Errorf("polls = %d, want the three failures and the approval", p.Polls())
	}
	if sess, _ := readSession(t, home); sess["token"] != authtest.Session {
		t.Errorf("session.json = %v", sess)
	}
}

func TestACancelledLoginIsOneLineAndExit130AndSavesNothing(t *testing.T) {
	home := fakeHome(t, "")
	noBrowser(t)
	p := authtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	client := api.New("", api.WithBaseURL(p.URL), api.WithClock(time.Now, func(time.Duration) <-chan time.Time {
		cancel() // ctrl-c while waiting
		return make(chan time.Time)
	}))
	var out bytes.Buffer
	err := cancelled(&out, deviceLogin(ctx, client, &out))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 130 {
		t.Fatalf("err = %v, want exit 130", err)
	}
	if !strings.Contains(out.String(), "Sign-in cancelled.") {
		t.Errorf("output:\n%s", out.String())
	}
	if sess, _ := readSession(t, home); sess != nil && sess["token"] != nil {
		t.Errorf("a cancelled login saved %v", sess)
	}
}

func TestLoginRefusesAPlatformThatIsNotACLIOne(t *testing.T) {
	fakeHome(t, "")
	noBrowser(t)
	p := authtest.New(t)
	p.Legacy = true
	err := deviceLogin(context.Background(), instant(p), &bytes.Buffer{})
	if !errors.Is(err, api.ErrDeviceUnsupported) || !strings.Contains(err.Error(), "try again later") {
		t.Fatalf("err = %v", err)
	}
	if p.Polls() != 0 {
		t.Error("polled a request that is not a CLI one")
	}
}

// The flags of the retired sign-in are hidden stubs: a script that still
// passes one is told what to do, and nothing is asked of the platform.
func TestRetiredLoginFlagsSayWhatToDoInstead(t *testing.T) {
	fakeHome(t, "")
	for _, flag := range []string{"--email", "--link", "--totp"} {
		code, msg := execute(t, "auth", "login", flag, "x")
		if code == 0 || strings.Contains(msg, "unknown flag") ||
			!strings.Contains(msg, flag+" was removed") || !strings.Contains(msg, "nan auth login") {
			t.Errorf("%s: exit %d: %s", flag, code, msg)
		}
	}
	_, help := execute(t, "auth", "login", "--help")
	for _, flag := range []string{"--email", "--link", "--totp"} {
		if strings.Contains(help, flag) {
			t.Errorf("the help still lists %s", flag)
		}
	}
	if !strings.Contains(help, "--no-browser") {
		t.Error("the help does not list --no-browser")
	}
}

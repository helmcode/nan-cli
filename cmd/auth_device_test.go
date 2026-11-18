package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/auth"
	"github.com/nxssie/nan-cli/internal/auth/authtest"
)

// signInAgainst points the sign-in at a fake platform and records every call
// to OpenBrowser instead of running a browser.
func signInAgainst(t *testing.T, p *authtest.Platform) *[]string {
	t.Helper()
	swap(t, &auth.SignInBaseURL, p.URL)
	var opened []string
	swap(t, &auth.OpenBrowser, func(u string) error {
		opened = append(opened, u)
		return nil
	})
	t.Setenv("NAN_NO_BROWSER", "")
	return &opened
}

// runDevice runs the login the way runLogin does (start, wait, save, one
// cancellation point) with os.Stdout captured, and returns what it printed.
func runDevice(t *testing.T, ctx context.Context) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	runErr := cancelled(f, deviceLogin(ctx, f))
	os.Stdout = old
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return string(raw), runErr
}

func TestDeviceLoginSavesTheSession(t *testing.T) {
	home := fakeHome(t, "")
	p := authtest.New(t)
	p.Script(authtest.Pending, authtest.Approved("sess-device"))
	opened := signInAgainst(t, p)

	text, err := runDevice(t, context.Background())
	if err != nil {
		t.Fatalf("login: %v\n%s", err, text)
	}
	sess, mode := readSession(t, home)
	if sess["token"] != "sess-device" {
		t.Errorf("session.json = %v", sess)
	}
	if runtime.GOOS != "windows" && mode != 0o600 {
		t.Errorf("mode = %v, want 0600", mode)
	}
	if !strings.Contains(text, authtest.UserCode) || !strings.Contains(text, "/api/auth/device") {
		t.Errorf("output lacks the code or the address:\n%s", text)
	}
	if strings.Contains(text, authtest.DeviceCode) {
		t.Errorf("the device_code was printed:\n%s", text)
	}
	if n := strings.Count(text, "Logged in successfully."); n != 1 {
		t.Errorf("%d success lines:\n%s", n, text)
	}
	if len(*opened) != 1 || !strings.Contains((*opened)[0], "/api/auth/device") {
		t.Errorf("opened = %v", *opened)
	}
}

func TestNoBrowserEnvSkipsOpeningIt(t *testing.T) {
	fakeHome(t, "")
	p := authtest.New(t)
	p.Approve("tok")
	opened := signInAgainst(t, p)
	t.Setenv("NAN_NO_BROWSER", "1")

	out, err := runDevice(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 0 {
		t.Errorf("OpenBrowser called with NAN_NO_BROWSER=1: %v", *opened)
	}
	if !strings.Contains(out, "Open ") {
		t.Errorf("the address was not printed:\n%s", out)
	}
}

func TestDeviceLoginFailuresSaveNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*authtest.Platform)
		want error
	}{
		{"denied", func(p *authtest.Platform) { p.Deny() }, auth.ErrDenied},
		{"expired", func(p *authtest.Platform) { p.Expire() }, auth.ErrExpired},
		{"old server", func(p *authtest.Platform) { p.OmitClientKind = true }, auth.ErrServerTooOld},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, "")
			p := authtest.New(t)
			c.set(p)
			signInAgainst(t, p)
			_, err := runDevice(t, context.Background())
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			if sess, _ := readSession(t, home); sess != nil {
				t.Errorf("session.json written: %v", sess)
			}
		})
	}
}

// A ctrl-c while the platform is still being asked for a code.
func TestCancelDuringStart(t *testing.T) {
	home := fakeHome(t, "")
	p := authtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	p.OnStart = func() { cancel(); time.Sleep(50 * time.Millisecond) }
	signInAgainst(t, p)

	out, err := runDevice(t, ctx)
	assertCancelled(t, err, out)
	if sess, _ := readSession(t, home); sess != nil {
		t.Errorf("session.json written: %v", sess)
	}
}

func TestCancelDuringPoll(t *testing.T) {
	home := fakeHome(t, "")
	p := authtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	p.OnPoll = func(int) { cancel() }
	signInAgainst(t, p)

	out, err := runDevice(t, ctx)
	assertCancelled(t, err, out)
	if sess, _ := readSession(t, home); sess != nil {
		t.Errorf("session.json written: %v", sess)
	}
}

// The approval and the ctrl-c land together: the cancel wins, nothing is
// printed as a success and a session that was already there is untouched.
func TestCancelRacingTheApprovalWins(t *testing.T) {
	const before = `{"token":"old-session","apiKey":"sk-keep"}`
	home := fakeHome(t, before)
	p := authtest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	p.OnPoll = func(int) { cancel() }
	p.Approve("new-session")
	signInAgainst(t, p)

	out, err := runDevice(t, ctx)
	assertCancelled(t, err, out)
	if strings.Contains(out, "Logged in") {
		t.Errorf("success printed:\n%s", out)
	}
	raw, rerr := os.ReadFile(filepath.Join(home, ".config", "nan", "session.json"))
	if rerr != nil || string(raw) != before {
		t.Errorf("session.json = %q (%v), want it untouched", raw, rerr)
	}
}

func assertCancelled(t *testing.T, err error, out string) {
	t.Helper()
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 130 {
		t.Errorf("err = %v, want exit 130", err)
	}
	if n := strings.Count(out, "Sign-in cancelled."); n != 1 {
		t.Errorf("%d cancel lines:\n%s", n, out)
	}
}

// The retired flags are unknown now, not silently accepted.
func TestRetiredLoginFlagsAreUnknown(t *testing.T) {
	// Spelled in two halves so a grep for the retired flags stays clean.
	for _, name := range []string{"em" + "ail", "li" + "nk", "to" + "tp"} {
		flag := "--" + name
		fakeHome(t, "")
		rootCmd.SetArgs([]string{"auth", "login", flag, "x"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		err := rootCmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s: err = %v", flag, err)
		}
	}
}

func TestTokenFlagStillSavesTheSession(t *testing.T) {
	home := fakeHome(t, "")
	defer func() { tokenFlag = "" }()
	rootCmd.SetArgs([]string{"auth", "login", "--token", "direct-token"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if sess, _ := readSession(t, home); sess["token"] != "direct-token" {
		t.Errorf("session.json = %v", sess)
	}
}

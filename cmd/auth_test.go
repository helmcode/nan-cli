package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `--token` and `--link` both take a secret, and a secret spelled out on a
// command line is in ~/.bash_history and in `ps` for as long as the command
// runs. "-" is the way out for a script or a CI step, so it has to actually
// read stdin - and has to say so rather than saving an empty token when
// nothing arrives.
func TestFlagValueReadsStdinForADash(t *testing.T) {
	for _, c := range []struct {
		name    string
		flag    string
		stdin   string
		want    string
		wantErr bool
	}{
		{name: "a value stays a value", flag: "a-token", want: "a-token"},
		{name: "trimmed", flag: "  a-token\n", want: "a-token"},
		{name: "a dash reads stdin", flag: "-", stdin: "from-stdin\n", want: "from-stdin"},
		{name: "an empty stdin is not a token", flag: "-", stdin: "   \n", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.flag == "-" {
				withStdin(t, c.stdin)
			}
			got, err := flagValue(c.flag)
			if c.wantErr {
				if err == nil {
					t.Fatalf("took %q as a credential", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("flagValue(%q) = %q, want %q", c.flag, got, c.want)
			}
		})
	}
}

func withStdin(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = original
		f.Close()
	})
}

// tokenServer answers GET /v1/runs?limit=1 with status, and records what it
// was asked.
func tokenServer(t *testing.T, status int) (*int, *string) {
	t.Helper()
	calls, auth := new(int), new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		*auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/runs" || r.URL.Query().Get("limit") != "1" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"object":"list","data":[],"next_cursor":null}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":{"message":"invalid token","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(srv.Close)
	old := runsBaseURL
	runsBaseURL = srv.URL
	t.Cleanup(func() { runsBaseURL = old })
	return calls, auth
}

func readSession(t *testing.T, home string) (map[string]any, os.FileMode) {
	t.Helper()
	path := filepath.Join(home, ".config", "nan", "session.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m, info.Mode().Perm()
}

func pipedToken(s string) tokenInput { return tokenInput{r: strings.NewReader(s)} }

func TestAPITokenLoginSavesAVerifiedToken(t *testing.T) {
	for _, c := range []struct {
		name, tok, field string
	}{
		{"platform token", testPAT, "platformToken"},
		{"api key", testKey, "apiKey"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, `{"token":"","enabledTools":{"pi":true}}`)
			calls, auth := tokenServer(t, http.StatusOK)
			var out, prompt bytes.Buffer
			if err := loginWithAPIToken(context.Background(), pipedToken(c.tok+"\n"), &out, &prompt); err != nil {
				t.Fatal(err)
			}
			if *calls != 1 || *auth != "Bearer "+c.tok {
				t.Errorf("verification: %d calls, Authorization %q", *calls, *auth)
			}
			sess, mode := readSession(t, home)
			if sess[c.field] != c.tok {
				t.Errorf("session.json = %v", sess)
			}
			if sess["enabledTools"] == nil {
				t.Error("saving the token dropped the rest of the session")
			}
			if runtime.GOOS != "windows" && mode != 0o600 {
				t.Errorf("session.json mode %04o", mode)
			}
			if strings.Contains(out.String()+prompt.String(), "SECRET") {
				t.Errorf("token printed: %q %q", out.String(), prompt.String())
			}
		})
	}
}

// A platform token must not land in apiKey: the Setup tab writes apiKey into
// every tool it configures, and the inference API refuses platform tokens.
func TestAPITokenLoginKeepsPlatformTokensOutOfAPIKey(t *testing.T) {
	home := fakeHome(t, `{"token":"","apiKey":"`+testKey+`"}`)
	tokenServer(t, http.StatusOK)
	if err := loginWithAPIToken(context.Background(), pipedToken(testPAT), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	sess, _ := readSession(t, home)
	if sess["apiKey"] != testKey || sess["platformToken"] != testPAT {
		t.Errorf("session.json = %v", sess)
	}
}

func TestAPITokenLoginSavesNothingOnFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		input  string
		status int
		want   string
		calls  int
	}{
		{"empty stdin", "", http.StatusOK, "no token arrived", 0},
		{"wrong shape", "ghp_SECRETnotours\n", http.StatusOK, "not a valid token", 0},
		{"only the first line counts", "nan_pat_a\r\nX-SECRET: 1", http.StatusOK, "", 1},
		{"refused", testPAT, http.StatusUnauthorized, "refused that token", 1},
		{"platform down", testPAT, http.StatusBadGateway, "could not check the token", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, "")
			calls, _ := tokenServer(t, c.status)
			var out bytes.Buffer
			err := loginWithAPIToken(context.Background(), pipedToken(c.input), &out, io.Discard)
			if c.name == "only the first line counts" {
				// Only the first line is the token, and that line is valid.
				if err != nil {
					t.Fatalf("first line refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if *calls != c.calls {
				t.Errorf("%d requests, want %d", *calls, c.calls)
			}
			if sess, _ := readSession(t, home); sess != nil {
				t.Errorf("saved %v after a failure", sess)
			}
			if strings.Contains(err.Error()+out.String(), "SECRET") {
				t.Errorf("token echoed: %v", err)
			}
		})
	}
}

// On a terminal the token is read without echo, after a prompt on stderr.
func TestAPITokenLoginOnATerminalReadsHidden(t *testing.T) {
	home := fakeHome(t, `{"token":"sess-1"}`)
	tokenServer(t, http.StatusOK)
	hidden := false
	in := tokenInput{tty: true, readHidden: func() ([]byte, error) { hidden = true; return []byte(testPAT + "\n"), nil }}
	var out, prompt bytes.Buffer
	if err := loginWithAPIToken(context.Background(), in, &out, &prompt); err != nil {
		t.Fatal(err)
	}
	if !hidden || !strings.Contains(prompt.String(), "not shown") {
		t.Errorf("hidden=%v prompt=%q", hidden, prompt.String())
	}
	if !strings.Contains(out.String(), "session") {
		t.Errorf("does not say the session is used first: %q", out.String())
	}
	if sess, _ := readSession(t, home); sess["token"] != "sess-1" || sess["platformToken"] != testPAT {
		t.Errorf("session.json = %v", sess)
	}
}

func TestAPITokenCannotBeCombined(t *testing.T) {
	fakeHome(t, "")
	defer func() { apiTokenFlag, emailFlag = false, "" }()
	rootCmd.SetArgs([]string{"auth", "login", "--api-token", "--email", "a@b.c"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("err = %v", err)
	}
}

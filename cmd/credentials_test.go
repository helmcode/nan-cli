package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

const (
	testPAT = "nan_pat_0123456789abcdefSECRET"
	testKey = "sk-0123456789SECRETkey"
)

// fakeHome points the session at an empty temp directory, optionally with a
// session.json, and clears NAN_TOKEN so the developer's own does not leak in.
func fakeHome(t *testing.T, sessionJSON string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(tokenEnvVar, "")
	if sessionJSON != "" {
		dir := filepath.Join(home, ".config", "nan")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(sessionJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func writeTokenFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestValidTokenShape(t *testing.T) {
	for tok, want := range map[string]bool{
		testPAT:                  true,
		testKey:                  true,
		"nan_pat_":               false,
		"sk-":                    false,
		"nan_session_abc":        false,
		"Bearer " + testPAT:      false,
		"nan_pat_a\r\nX-Evil: 1": false,
		"nan_pat_a b":            false,
		"nan_pat_" + strings.Repeat("a", maxTokenLen): false,
		"": false,
	} {
		if got := validTokenShape(tok); got != want {
			t.Errorf("validTokenShape(%q) = %v, want %v", tok, got, want)
		}
	}
}

// The documented precedence: NAN_TOKEN > --token-file > session > saved
// platform token > saved API key.
func TestCredentialPrecedence(t *testing.T) {
	full := `{"token":"sess-1","platformToken":"` + testPAT + `","apiKey":"` + testKey + `"}`
	file := writeTokenFile(t, "nan_pat_fromfile\n", 0o600)

	cases := []struct {
		name     string
		session  string
		env      string
		file     string
		kind     credKind
		wantSess string
		bearer   string
	}{
		{name: "env wins over everything", session: full, env: "nan_pat_fromenv", file: file, kind: credEnv, bearer: "nan_pat_fromenv"},
		{name: "env is trimmed", session: full, env: "  nan_pat_fromenv\n", kind: credEnv, bearer: "nan_pat_fromenv"},
		{name: "file wins over the session", session: full, file: file, kind: credFile, bearer: "nan_pat_fromfile"},
		{name: "file works with no session at all", file: file, kind: credFile, bearer: "nan_pat_fromfile"},
		{name: "session wins over saved tokens", session: full, kind: credSession, wantSess: "sess-1"},
		{name: "saved platform token over saved key", session: `{"token":"","platformToken":"` + testPAT + `","apiKey":"` + testKey + `"}`, kind: credStoredToken, bearer: testPAT},
		{name: "saved key last", session: `{"token":"","apiKey":"` + testKey + `"}`, kind: credStoredKey, bearer: testKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeHome(t, c.session)
			cred, err := resolveCredential(envOf(map[string]string{tokenEnvVar: c.env}), c.file)
			if err != nil {
				t.Fatal(err)
			}
			if cred.kind != c.kind || cred.bearer != c.bearer || cred.session != c.wantSess {
				t.Errorf("got kind %d bearer %q session %q", cred.kind, cred.bearer, cred.session)
			}
			if cred.usingKey() != (c.kind != credSession) {
				t.Errorf("usingKey = %v", cred.usingKey())
			}
		})
	}
}

func TestNothingConfiguredSaysHowToSignIn(t *testing.T) {
	for _, sess := range []string{"", `{"token":""}`} {
		fakeHome(t, sess)
		_, err := resolveCredential(envOf(nil), "")
		if exitCode(err) != exitAuth {
			t.Fatalf("exit %d (%v)", exitCode(err), err)
		}
		for _, want := range []string{"nan auth login", tokenEnvVar} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q does not mention %s", err, want)
			}
		}
	}
}

// A malformed token is refused before anything is sent, and the error never
// repeats its value.
func TestMalformedTokensAreRefusedWithoutEchoing(t *testing.T) {
	const bad = "ghp_SECRETnotours"
	fakeHome(t, "")
	_, err := resolveCredential(envOf(map[string]string{tokenEnvVar: bad}), "")
	if exitCode(err) != exitAuth || !strings.Contains(err.Error(), tokenEnvVar) {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error echoes the value: %v", err)
	}

	path := writeTokenFile(t, bad+"\n", 0o600)
	_, err = resolveCredential(envOf(nil), path)
	if exitCode(err) != exitAuth || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("exit %d (%v)", exitCode(err), err)
	}
}

func TestTokenFileReadsTheFirstLine(t *testing.T) {
	for name, content := range map[string]string{
		"newline":       testPAT + "\n",
		"no newline":    testPAT,
		"crlf":          testPAT + "\r\n",
		"padded":        "  " + testPAT + "  \n",
		"trailing junk": testPAT + "\nsomething else\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readTokenFile(writeTokenFile(t, content, 0o600))
			if err != nil {
				t.Fatal(err)
			}
			if got != testPAT {
				t.Errorf("got %q", got)
			}
		})
	}
}

func TestTokenFileErrors(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		path string
		want string
	}{
		"missing":   {filepath.Join(dir, "nope"), "could not open"},
		"directory": {dir, "directory"},
		"empty":     {writeTokenFile(t, "\n", 0o600), "empty"},
		"too long":  {writeTokenFile(t, "nan_pat_"+strings.Repeat("a", 4096), 0o600), "not a valid token"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readTokenFile(c.path)
			if exitCode(err) != exitAuth || !strings.Contains(err.Error(), c.want) {
				t.Errorf("exit %d (%v), want %q", exitCode(err), err, c.want)
			}
		})
	}
}

// Like ssh with a private key: a token file others can read or write is
// refused, with the command that fixes it.
func TestTokenFileMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not the mechanism on Windows")
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o620, 0o602, 0o666} {
		path := writeTokenFile(t, testPAT+"\n", mode)
		_, err := readTokenFile(path)
		if exitCode(err) != exitAuth || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o: exit %d (%v)", mode, exitCode(err), err)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("mode %04o: error echoes the token: %v", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		if _, err := readTokenFile(writeTokenFile(t, testPAT+"\n", mode)); err != nil {
			t.Errorf("mode %04o refused: %v", mode, err)
		}
	}
}

// What the runs commands send is a bearer token for every source but the
// session, and the session cookie for that one.
func TestCredentialIsSentAsBearer(t *testing.T) {
	var mu sync.Mutex
	var auth, cookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth, cookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		mu.Unlock()
		_, _ = io.WriteString(w, `{"object":"list","data":[],"next_cursor":null}`)
	}))
	defer srv.Close()
	old := runsBaseURL
	runsBaseURL = srv.URL
	defer func() { runsBaseURL = old }()

	for _, c := range []struct {
		cred       credential
		auth, cook string
	}{
		{credential{kind: credEnv, bearer: testPAT}, "Bearer " + testPAT, ""},
		{credential{kind: credFile, bearer: testKey}, "Bearer " + testKey, ""},
		{credential{kind: credSession, session: "sess-1"}, "", "nan_session=sess-1"},
	} {
		if _, err := c.cred.client().ListRuns(context.Background(), apiListOne()); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		if auth != c.auth || cookie != c.cook {
			t.Errorf("kind %d sent Authorization %q Cookie %q", c.cred.kind, auth, cookie)
		}
		mu.Unlock()
	}
}

// A refused token names where it came from and how to replace it, never
// what it is.
func TestRefusedTokenNamesItsSource(t *testing.T) {
	for _, c := range []struct {
		cred credential
		want []string
	}{
		{credential{kind: credEnv, bearer: testPAT}, []string{tokenEnvVar, "Settings > Tokens"}},
		{credential{kind: credFile, path: "/run/secrets/nan", bearer: testPAT}, []string{"/run/secrets/nan", "Settings > Tokens"}},
		{credential{kind: credStoredToken, bearer: testPAT}, []string{"saved platform token", "nan auth login --api-token"}},
		{credential{kind: credStoredKey, bearer: testKey}, []string{"saved API key", "nan auth login"}},
		{credential{kind: credSession, session: "s"}, []string{"session has expired", "nan auth login"}},
	} {
		err := authError(c.cred)
		if err.Code != exitAuth {
			t.Errorf("exit %d", err.Code)
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%q does not mention %q", err, w)
			}
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error echoes the credential: %v", err)
		}
	}
}

// End to end through the command line: NAN_TOKEN alone is enough for
// `nan runs ls`, with no session file on the machine.
func TestRunsLsWithOnlyNANTOKEN(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"object":"list","data":[],"next_cursor":null}`)
	}))
	defer srv.Close()
	old := runsBaseURL
	runsBaseURL = srv.URL
	defer func() { runsBaseURL = old }()
	fakeHome(t, "")
	t.Setenv(tokenEnvVar, testPAT)

	env, err := newRunsEnv()
	if err != nil {
		t.Fatal(err)
	}
	env.stdout, env.stderr = io.Discard, io.Discard
	if err := doRunsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer "+testPAT {
		t.Errorf("Authorization = %q", got)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "nan", "session.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("NAN_TOKEN was written to disk (stat: %v)", err)
	}
}

// `nan me` and `nan metrics usage` need a session; with only a token they
// say so instead of a bare 401.
func TestSessionCommandsExplainTokens(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T){
		"env":         func(t *testing.T) { fakeHome(t, ""); t.Setenv(tokenEnvVar, testPAT) },
		"saved token": func(t *testing.T) { fakeHome(t, `{"token":"","platformToken":"`+testPAT+`"}`) },
		"saved key":   func(t *testing.T) { fakeHome(t, `{"token":"","apiKey":"`+testKey+`"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			_, err := requireSession("nan me")
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range []string{"nan me needs a session", "nan auth login", "nan run"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%q does not mention %q", err, w)
				}
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error echoes the token: %v", err)
			}
		})
	}

	fakeHome(t, "")
	if _, err := requireSession("nan me"); !strings.Contains(fmtErr(err), "not logged in") {
		t.Errorf("nothing configured: %v", err)
	}
	fakeHome(t, `{"token":"sess-1"}`)
	if sess, err := requireSession("nan me"); err != nil || sess.Token != "sess-1" {
		t.Errorf("a session is refused: %v", err)
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func apiListOne() api.ListRunsParams { return api.ListRunsParams{Limit: 1} }

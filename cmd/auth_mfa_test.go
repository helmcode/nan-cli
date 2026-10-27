package cmd

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/auth"
	"github.com/nxssie/nan-cli/internal/auth/authtest"
)

// Two-factor sign-in from `nan auth login`, against a fake platform written
// from the MFA API contract (internal/auth/authtest).

const testLink = "https://cloud-api.nan.builders/api/auth/login/verify?token=" + authtest.LinkToken

// mfaPlatform points the sign-in at the fake and gives the login flags back
// their defaults when the test ends: cobra writes them into package vars.
func mfaPlatform(t *testing.T, factor bool) *authtest.Platform {
	t.Helper()
	p := authtest.New(t)
	p.Factor = factor
	swap(t, &auth.SignInBaseURL, p.URL)
	for _, f := range []*string{&linkFlag, &totpFlag, &tokenFlag, &emailFlag} {
		swap(t, f, "")
	}
	swap(t, &apiTokenFlag, false)
	return p
}

// typed is a terminal on which the member types these, one per prompt.
func typed(answers ...string) (codeInput, *int) {
	asked := new(int)
	return codeInput{tty: true, readHidden: func() ([]byte, error) {
		if *asked >= len(answers) {
			return nil, io.EOF
		}
		*asked++
		return []byte(answers[*asked-1] + "\n"), nil
	}}, asked
}

// piped is stdin that is not a terminal: what --totp - reads.
func piped(stdin string, totp bool) codeInput {
	r := strings.NewReader(stdin)
	line := func() (string, error) {
		var b strings.Builder
		buf := make([]byte, 1)
		for {
			n, err := r.Read(buf)
			if n == 1 {
				if buf[0] == '\n' {
					return b.String(), nil
				}
				b.WriteByte(buf[0])
			}
			if err != nil {
				return b.String(), err
			}
		}
	}
	return codeInput{fromStdin: totp, readLine: line}
}

func TestLoginWithoutAFactorIsUnchanged(t *testing.T) {
	home := fakeHome(t, "")
	p := mfaPlatform(t, false)
	var prompt bytes.Buffer
	codes, asked := typed()
	if err := finishSignIn(authtest.LinkToken, codes, io.Discard, &prompt); err != nil {
		t.Fatal(err)
	}
	if sess, _ := readSession(t, home); sess["token"] != authtest.Session {
		t.Errorf("session.json = %v", sess)
	}
	if *asked != 0 || prompt.Len() != 0 || len(p.Challenges()) != 0 {
		t.Errorf("asked for a code an account without a factor does not have: %d prompts, %q", *asked, prompt.String())
	}
}

func TestLoginAsksForTheCodeOnATerminal(t *testing.T) {
	home := fakeHome(t, `{"token":"","apiKey":"sk-kept"}`)
	mfaPlatform(t, true)
	var prompt bytes.Buffer
	codes, asked := typed(authtest.Code)
	if err := finishSignIn(authtest.LinkToken, codes, io.Discard, &prompt); err != nil {
		t.Fatal(err)
	}
	sess, _ := readSession(t, home)
	if sess["token"] != authtest.Session || sess["apiKey"] != "sk-kept" {
		t.Errorf("session.json = %v", sess)
	}
	if *asked != 1 || !strings.Contains(prompt.String(), "Authenticator code: ") {
		t.Errorf("%d prompts: %q", *asked, prompt.String())
	}
	if !strings.Contains(prompt.String(), "recovery codes") {
		t.Errorf("the prompt does not say a recovery code works too: %q", prompt.String())
	}
}

// A wrong code, a replayed one and something that is not a code at all are
// all asked again, on the same sign-in, with what went wrong.
func TestLoginAsksAgainAfterAWrongCode(t *testing.T) {
	home := fakeHome(t, "")
	p := mfaPlatform(t, true)
	var prompt bytes.Buffer
	codes, asked := typed("000000", "not a code", authtest.Code)
	if err := finishSignIn(authtest.LinkToken, codes, io.Discard, &prompt); err != nil {
		t.Fatal(err)
	}
	if *asked != 3 {
		t.Errorf("%d prompts, want 3", *asked)
	}
	for _, want := range []string{"not right (4 attempts left)", "neither a 6-digit code"} {
		if !strings.Contains(prompt.String(), want) {
			t.Errorf("%q does not say %q", prompt.String(), want)
		}
	}
	// "not a code" never left the machine.
	if n := len(p.Challenges()); n != 2 {
		t.Errorf("%d challenge POSTs, want 2", n)
	}
	if sess, _ := readSession(t, home); sess["token"] != authtest.Session {
		t.Errorf("session.json = %v", sess)
	}
}

func TestLoginWithARecoveryCodeSaysHowManyAreLeft(t *testing.T) {
	for _, c := range []struct {
		codes []string
		want  string
	}{
		{[]string{"q7wa-z2xd-pl4r-6wze", "aaaa-bbbb-cccc-dddd", "eeee-ffff-gggg-hhhh"}, "2 left"},
		{[]string{"q7wa-z2xd-pl4r-6wze"}, "last recovery code"},
	} {
		t.Run(c.want, func(t *testing.T) {
			fakeHome(t, "")
			p := mfaPlatform(t, true)
			p.RecoveryCodes = c.codes
			var out bytes.Buffer
			codes, _ := typed("Q7WA Z2XD PL4R 6WZE")
			if err := finishSignIn(authtest.LinkToken, codes, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), c.want) || !strings.Contains(out.String(), "Two-factor authentication") {
				t.Errorf("%q does not say %q and where to make more", out.String(), c.want)
			}
		})
	}
}

// Each way the sign-in ends saves nothing and says what to do next.
func TestLoginThatCannotFinishSavesNothing(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*authtest.Platform)
		codes []string
		want  string
	}{
		{"five wrong codes", nil, []string{"000000", "000001", "000002", "000003", "000004"}, "too many wrong codes"},
		{"expired", func(p *authtest.Platform) { p.Expire() }, []string{authtest.Code}, "timed out"},
		{"refused", func(p *authtest.Platform) {
			p.Next = &authtest.Refusal{Status: 403, Body: `{"error":"login_refused","message":"x"}`}
		}, []string{authtest.Code}, "refused the sign-in"},
		{"nothing typed at all", nil, nil, "could not read the code"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, "")
			p := mfaPlatform(t, true)
			codes, _ := typed(c.codes...)
			// The challenge opens on the verify POST, which finishSignIn
			// makes; the fake is changed as soon as it has.
			if c.setup != nil {
				inner := codes.readHidden
				first := true
				codes.readHidden = func() ([]byte, error) {
					if first {
						first = false
						c.setup(p)
					}
					return inner()
				}
			}
			err := finishSignIn(authtest.LinkToken, codes, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if sess, _ := readSession(t, home); sess != nil {
				t.Errorf("saved %v", sess)
			}
		})
	}
}

// --totp - reads one line of stdin, with no prompt and no second try: a
// script cannot answer one.
func TestTOTPFromStdin(t *testing.T) {
	t.Run("right", func(t *testing.T) {
		home := fakeHome(t, "")
		mfaPlatform(t, true)
		var prompt bytes.Buffer
		if err := finishSignIn(authtest.LinkToken, piped(authtest.Code+"\n", true), io.Discard, &prompt); err != nil {
			t.Fatal(err)
		}
		if prompt.Len() != 0 {
			t.Errorf("prompted a pipe: %q", prompt.String())
		}
		if sess, _ := readSession(t, home); sess["token"] != authtest.Session {
			t.Errorf("session.json = %v", sess)
		}
	})
	t.Run("wrong, then nothing", func(t *testing.T) {
		home := fakeHome(t, "")
		p := mfaPlatform(t, true)
		err := finishSignIn(authtest.LinkToken, piped("000000\n"+authtest.Code+"\n", true), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "not right") {
			t.Fatalf("err = %v", err)
		}
		if n := len(p.Challenges()); n != 1 {
			t.Errorf("%d tries from a pipe, want 1", n)
		}
		if sess, _ := readSession(t, home); sess != nil {
			t.Errorf("saved %v", sess)
		}
	})
	t.Run("empty", func(t *testing.T) {
		fakeHome(t, "")
		mfaPlatform(t, true)
		err := finishSignIn(authtest.LinkToken, piped("", true), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "no code arrived") || !strings.Contains(err.Error(), "--totp -") {
			t.Fatalf("err = %v", err)
		}
	})
}

// --totp - with a terminal on stdin: the code is still read without echo,
// and it still gets one try, as the help says.
func TestTOTPFromATerminalStdin(t *testing.T) {
	home := fakeHome(t, "")
	p := mfaPlatform(t, true)
	codes, asked := typed("000000", authtest.Code)
	codes.fromStdin = true
	var prompt bytes.Buffer
	err := finishSignIn(authtest.LinkToken, codes, io.Discard, &prompt)
	if err == nil || !strings.Contains(err.Error(), "not right") {
		t.Fatalf("err = %v", err)
	}
	if *asked != 1 || len(p.Challenges()) != 1 {
		t.Errorf("%d prompts, %d POSTs: --totp - gets one try", *asked, len(p.Challenges()))
	}
	if !strings.Contains(prompt.String(), "Authenticator code: ") {
		t.Errorf("not asked on the terminal: %q", prompt.String())
	}
	if sess, _ := readSession(t, home); sess != nil {
		t.Errorf("saved %v", sess)
	}
}

// An account that has not finished signing up and has a factor finishes in
// the browser: nan auth login asks for no code and saves nothing, and says
// what to do next.
func TestLoginForAnUnfinishedSignUpSendsItToTheBrowser(t *testing.T) {
	home := fakeHome(t, "")
	p := mfaPlatform(t, true)
	p.Onboarding = true
	var prompt bytes.Buffer
	codes, asked := typed(authtest.Code)
	err := finishSignIn(authtest.LinkToken, codes, io.Discard, &prompt)
	const want = "your NaN account is not set up yet — finish it in the browser: ask for a new link at " +
		"https://cloud.nan.builders (this one is spent), complete the setup there, then sign in here again"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	if *asked != 0 || prompt.Len() != 0 || len(p.Challenges()) != 0 {
		t.Errorf("asked for a code with no challenge: %d prompts, %q", *asked, prompt.String())
	}
	if sess, _ := readSession(t, home); sess != nil {
		t.Errorf("saved %v", sess)
	}
}

// No terminal and no --totp: the link is already spent, so the message says
// to ask for another and names the flag that would have worked.
func TestNoTerminalAndNoFlag(t *testing.T) {
	home := fakeHome(t, "")
	p := mfaPlatform(t, true)
	err := finishSignIn(authtest.LinkToken, piped(authtest.Code+"\n", false), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("signed in with no way to ask for the code")
	}
	for _, want := range []string{"two-factor", "new one", "--totp -"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
	if n := len(p.Challenges()); n != 0 {
		t.Errorf("read a code nobody asked to be read: %d POSTs", n)
	}
	if sess, _ := readSession(t, home); sess != nil {
		t.Errorf("saved %v", sess)
	}
}

// The whole command, as a script runs it: link as an argument, code piped in.
func TestLoginCommandWithTOTPFromStdin(t *testing.T) {
	home := fakeHome(t, "")
	p := mfaPlatform(t, true)
	withStdin(t, authtest.Code+"\n")
	rootCmd.SetArgs([]string{"auth", "login", "--link", testLink, "--totp", "-"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if sess, _ := readSession(t, home); sess["token"] != authtest.Session {
		t.Errorf("session.json = %v", sess)
	}
	if f := p.Verifies(); len(f) != 1 || f[0].Get("mfa") != "1" {
		t.Errorf("verify forms = %v", f)
	}
	assertNoChallengeOnDisk(t, home)
}

func TestTOTPFlagSpellings(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"a code on the command line", []string{"--link", testLink, "--totp", authtest.Code}, `only takes "-"`},
		{"two flags reading stdin", []string{"--link", "-", "--totp", "-"}, "cannot both read stdin"},
		{"with a saved token", []string{"--token", "x", "--totp", "-"}, "cannot be combined with --token"},
		{"with --api-token", []string{"--api-token", "--totp", "-"}, "cannot be combined"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, "")
			p := mfaPlatform(t, true)
			rootCmd.SetArgs(append([]string{"auth", "login"}, c.args...))
			rootCmd.SetOut(io.Discard)
			rootCmd.SetErr(io.Discard)
			err := rootCmd.Execute()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), authtest.Code) {
				t.Errorf("the refusal repeats the code: %v", err)
			}
			if n := len(p.Verifies()); n != 0 {
				t.Errorf("spent the link on flags that cannot work: %d verify POSTs", n)
			}
			if sess, _ := readSession(t, home); sess != nil {
				t.Errorf("saved %v", sess)
			}
		})
	}
}

// No example of --totp - shows the code typed into the command, which would
// put it in the shell history the flag exists to keep it out of: not the
// help, not the hint the errors print, not the README.
func TestTOTPExamplesKeepTheCodeOffTheCommandLine(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"nan auth login --help": loginCmd.Example + "\n" + loginCmd.Long,
		"the errors' hint":      totpHint,
		"README.md":             string(readme),
	} {
		var examples int
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "--totp -") {
				continue
			}
			examples++
			if strings.Contains(line, "echo ") || strings.Contains(line, "<code>") || strings.Contains(line, authtest.Code) {
				t.Errorf("%s puts the code on the command line: %q", name, strings.TrimSpace(line))
			}
		}
		if examples == 0 {
			t.Errorf("%s has no --totp - example left to check", name)
		}
	}
}

// The challenge cookie is a credential for ten minutes and is kept in memory
// only. Whatever the sign-in wrote under HOME - on success and on failure -
// does not contain it, and neither does anything it printed.
func TestTheChallengeCookieNeverReachesDiskOrOutput(t *testing.T) {
	for _, c := range []struct {
		name  string
		codes []string
	}{
		{"signed in", []string{"000000", authtest.Code}},
		{"gave up", []string{"000000"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := fakeHome(t, `{"token":"","apiKey":"sk-kept"}`)
			mfaPlatform(t, true)
			var out, prompt bytes.Buffer
			codes, _ := typed(c.codes...)
			err := finishSignIn(authtest.LinkToken, codes, &out, &prompt)
			printed := out.String() + prompt.String()
			if err != nil {
				printed += err.Error()
			}
			if strings.Contains(printed, authtest.Pending) {
				t.Errorf("printed the challenge cookie: %q", printed)
			}
			assertNoChallengeOnDisk(t, home)
		})
	}
}

func assertNoChallengeOnDisk(t *testing.T, home string) {
	t.Helper()
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(authtest.Pending)) {
			t.Errorf("%s holds the challenge cookie", path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

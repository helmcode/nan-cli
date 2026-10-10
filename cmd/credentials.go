package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/nxssie/nan-cli/internal/session"
)

// tokenEnvVar holds a platform token (nan_pat_...) or an API key (sk-...)
// for `nan run` and `nan runs`. It is read, never written anywhere.
const tokenEnvVar = session.TokenEnvVar // short name for the many messages that print it

// maxTokenLen bounds what is accepted as a token. Real ones are far shorter;
// anything longer is a file that is not a token.
const maxTokenLen = 512

// runsBaseURL is where the run commands send requests. A variable so the
// tests can point it at a server of their own.
var runsBaseURL = api.RunsBaseURL

// tokenFileFlag is --token-file on `nan run` and `nan runs`.
var tokenFileFlag string

const tokenFileUsage = "Read the token (nan_pat_... or sk-...) from the first line of `PATH`, a file only you can read (chmod 600)"

type credKind int

const (
	credSession credKind = iota
	credStoredToken
	credStoredKey
	credEnv
	credFile
)

// credential is what a run command authenticates with, and where it came
// from: an error about a refused credential has to name the one that was
// refused, never its value.
type credential struct {
	kind    credKind
	path    string // the --token-file, for credFile
	session string // the nan_session token, for credSession
	bearer  string // a nan_pat_ token or an sk- key, for everything else
}

// describe names the credential for an error message.
func (c credential) describe() string {
	switch c.kind {
	case credEnv:
		return "the token in " + tokenEnvVar
	case credFile:
		return "the token in " + runs.SanitizeLine(c.path)
	case credStoredToken:
		return "your saved platform token"
	case credStoredKey:
		return "your saved API key"
	}
	return "your session"
}

func (c credential) client() *api.RunsClient {
	return api.NewRunsClient(c.session, c.bearer).WithBaseURL(runsBaseURL)
}

// validTokenShape is the check done before a token is sent anywhere: a
// platform token or an API key, printable ASCII, and nothing that could
// split an HTTP header.
func validTokenShape(tok string) bool {
	var prefix string
	switch {
	case strings.HasPrefix(tok, "nan_pat_"):
		prefix = "nan_pat_"
	case strings.HasPrefix(tok, "sk-"):
		prefix = "sk-"
	default:
		return false
	}
	if len(tok) <= len(prefix) || len(tok) > maxTokenLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < 0x21 || tok[i] > 0x7e {
			return false
		}
	}
	return true
}

const tokenShapeHint = "expected a platform token (nan_pat_...) from Settings > Tokens at https://cloud.nan.builders, or an API key (sk-...)"

// resolveCredential picks what the run commands authenticate with, first
// match wins:
//
//  1. NAN_TOKEN
//  2. --token-file PATH
//  3. a platform token saved with `nan auth login --api-token`
//  4. the session from `nan auth login`
//  5. the API key saved by the Setup tab (or by --api-token, for an sk- key)
//
// A saved platform token goes before the session because it was saved for
// exactly these commands, and because a session expires on the server with
// nothing on this side knowing: ranked after it, the token would never be
// reached on a machine that once signed in by email.
func resolveCredential(getenv func(string) string, tokenFile string) (credential, error) {
	if v := getenv(tokenEnvVar); v != "" {
		tok := strings.TrimSpace(v)
		if !validTokenShape(tok) {
			return credential{}, exitf(exitAuth, "%s is set but does not hold a valid token: %s", tokenEnvVar, tokenShapeHint)
		}
		return credential{kind: credEnv, bearer: tok}, nil
	}
	if tokenFile != "" {
		tok, err := readTokenFile(tokenFile)
		if err != nil {
			return credential{}, err
		}
		return credential{kind: credFile, path: tokenFile, bearer: tok}, nil
	}

	sess, err := session.Load()
	if err != nil {
		if errors.Is(err, session.ErrNotLoggedIn) {
			return credential{}, notSignedIn()
		}
		return credential{}, exitf(exitAuth, "could not read your session (%v): run nan auth login", err)
	}
	switch {
	case sess.PlatformToken != "":
		return credential{kind: credStoredToken, bearer: sess.PlatformToken}, nil
	case sess.Token != "":
		return credential{kind: credSession, session: sess.Token}, nil
	case sess.APIKey != "":
		return credential{kind: credStoredKey, bearer: sess.APIKey}, nil
	}
	return credential{}, notSignedIn()
}

func notSignedIn() *ExitError {
	return exitf(exitAuth, "not signed in: run nan auth login, or set %s (see: nan run --help)", tokenEnvVar)
}

// readTokenFile reads the token from the first line of path.
//
// A token file that other users can read hands them the token, and one they
// can write lets them swap in their own, so that runs (and their prompts) go
// to an account they control. Like ssh with a private key, such a file is
// refused rather than used. Only regular files are checked: a pipe, such as
// the one `--token-file <(vault read ...)` hands over, belongs to the process
// that made it and has no mode worth reading.
func readTokenFile(path string) (string, error) {
	shown := runs.SanitizeLine(path)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", exitf(exitAuth, "--token-file %s does not exist", shown)
	}
	// The *PathError would repeat the path unsanitised; its cause is enough.
	if err != nil {
		return "", exitf(exitAuth, "could not open --token-file %s: %v", shown, unwrapPath(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", exitf(exitAuth, "could not read --token-file %s: %v", shown, unwrapPath(err))
	}
	if info.IsDir() {
		return "", exitf(exitAuth, "--token-file %s is a directory, not a file", shown)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && info.Mode().IsRegular() && perm&0o077 != 0 {
		return "", exitf(exitAuth, "--token-file %s can be read or changed by other users (mode %04o); make it private with: chmod 600 %s", shown, perm, shown)
	}
	line, err := bufio.NewReader(io.LimitReader(f, maxTokenLen+2)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", exitf(exitAuth, "could not read --token-file %s: %v", shown, unwrapPath(err))
	}
	tok := strings.TrimSpace(line)
	if tok == "" {
		return "", exitf(exitAuth, "--token-file %s is empty: put the token on its first line", shown)
	}
	if !validTokenShape(tok) {
		return "", exitf(exitAuth, "the first line of --token-file %s is not a valid token: %s", shown, tokenShapeHint)
	}
	return tok, nil
}

// unwrapPath drops the path an *os.PathError carries, which error messages
// here print sanitised on their own.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// requireSession is for the commands that only a signed-in session can use.
// The platform refuses tokens and API keys there, and a bare 401 would leave
// a member who set one up wondering why it does not work.
func requireSession(command string) (*session.Session, error) {
	sess, err := session.Load()
	if err != nil && !errors.Is(err, session.ErrNotLoggedIn) {
		return nil, err
	}
	if sess != nil && sess.Token != "" {
		return sess, nil
	}
	hasToken := os.Getenv(tokenEnvVar) != "" || (sess != nil && (sess.PlatformToken != "" || sess.APIKey != ""))
	if hasToken {
		return nil, fmt.Errorf("%s needs a session: run nan auth login (platform tokens and API keys only work for nan run and nan runs)", command)
	}
	return nil, session.ErrNotLoggedIn
}

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/browser"
	"github.com/helmcode/nan-cli/internal/session"
	"github.com/helmcode/nan-cli/internal/tools"
)

var (
	tokenFlag     string
	keepToolsFlag bool
	apiTokenFlag  bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in through your browser, or save a platform token with --api-token",
	Long: `Log in through your browser.

This prints a short code and a page on nan.builders, and opens the page in
your browser. Sign in there if asked, type the code, and approve: this
command finishes by itself. Two-factor authentication, if you have it on, is
answered in the browser, never here. The code expires after a few minutes;
ctrl-c cancels. Set NAN_NO_BROWSER=1 to not open the page (it is always
printed), for a server or an SSH session: open the address on any device.

With --api-token, save a platform token instead, for a machine with nobody
at the keyboard (a server, a CI runner). Create the token in Settings >
Tokens at https://cloud.nan.builders. It is read from stdin only, so it
never ends up in your shell history or in ps. It is checked against
nan.builders and saved in ~/.config/nan/session.json, readable only by you.
Tokens work for nan run and nan runs; the other commands and the dashboard
need the browser sign-in. An API key (sk-...) can be saved the same way; it
replaces the one the Setup tab saved.`,
	Example: `  nan auth login
  NAN_NO_BROWSER=1 nan auth login         # print the address, do not open it
  nan auth login --api-token              # paste the token; it is not shown
  nan auth login --api-token < token.txt`,
	RunE: runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out: delete the local session, saved token and API key",
	RunE:  runLogout,
}

func init() {
	authCmd.AddCommand(loginCmd)
	authCmd.AddCommand(logoutCmd)
	loginCmd.Flags().StringVar(&tokenFlag, "token", "", `Save a nan_session token directly, skipping the browser ("-" reads it from stdin, keeping it out of your shell history)`)
	loginCmd.Flags().BoolVar(&apiTokenFlag, "api-token", false, "Save a platform token (nan_pat_...) or API key (sk-...) for nan run and nan runs, read from stdin")
	logoutCmd.Flags().BoolVar(&keepToolsFlag, "keep-tools", false, "Leave the API key in the tools this CLI configured")
}

// The platform signs in through the browser: the member approves a device
// request on its approval page, the way Helmcode Desktop does. It used to be
// Discord OAuth, and then an emailed link pasted into the terminal; opening
// that link anywhere but the terminal spent it.
func runLogin(cmd *cobra.Command, _ []string) error {
	if apiTokenFlag {
		if tokenFlag != "" {
			return fmt.Errorf("--api-token cannot be combined with --token")
		}
		return loginWithAPIToken(cmd.Context(), newTokenInput(os.Stdin), os.Stdout, os.Stderr)
	}
	if tokenFlag != "" {
		token, err := flagValue(tokenFlag)
		if err != nil {
			return err
		}
		if tokenFlag != "-" {
			// A session lasts until it expires: typed into the command line it
			// stays in the shell's history and is visible in ps meanwhile.
			_, _ = fmt.Fprintln(loginWarnings, "The session token is now in your shell history. Next time pass --token - and pipe it in.")
		}
		return saveToken(token)
	}

	ctx, stop := signal.NotifyContext(contextOf(cmd), os.Interrupt)
	defer stop()
	return cancelled(os.Stdout, deviceLogin(ctx, newAPIClient(""), os.Stdout))
}

// cancelled turns a ctrl-c during the sign-in into one line and exit 130, the
// code a shell gives an interrupted command.
func cancelled(out io.Writer, err error) error {
	if errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(out, "\nSign-in cancelled.")
		return &ExitError{Code: 130}
	}
	return err
}

// deviceLogin signs the CLI in through the browser: it starts a device
// request, shows the code, opens the approval page and waits.
func deviceLogin(ctx context.Context, client *api.Client, out io.Writer) error {
	d, err := client.StartDevice(ctx)
	if err != nil {
		return deviceError(err)
	}
	_, _ = fmt.Fprintf(out, "Your sign-in code: %s\n\n", d.UserCode)
	_, _ = fmt.Fprintf(out, "Open %s, sign in if asked, and type the code there.\n", d.VerificationURL)
	if browser.ShouldOpen() && browser.Open(d.VerificationURL) == nil {
		// Said as an attempt: a browser that starts and then fails to show
		// the page cannot be told from one that worked.
		_, _ = fmt.Fprintln(out, "(Tried to open it in your browser.)")
	}
	_, _ = fmt.Fprintf(out, "Waiting for approval (expires in %d minutes, ctrl-c to cancel)...\n", int(time.Until(d.ExpiresAt).Round(time.Minute).Minutes()))

	token, err := client.WaitForApproval(ctx, d)
	if err != nil {
		return deviceError(err)
	}
	// A ctrl-c that landed together with the approval wins: nothing is
	// written after a cancellation.
	if err := ctx.Err(); err != nil {
		return err
	}
	return saveToken(token)
}

// deviceError adds what to do next to the sign-in's sentinel errors, which
// carry none so that the dashboard can say its own.
func deviceError(err error) error {
	switch {
	case errors.Is(err, api.ErrDeviceExpired):
		return fmt.Errorf("%w - run nan auth login again", err)
	case errors.Is(err, api.ErrDeviceUnsupported):
		return fmt.Errorf("%w - try again later", err)
	}
	return err
}

// readHiddenLine reads a line from the terminal f without echo.
//
// term.ReadPassword turns echo off and only puts it back when it returns. A
// ctrl-c in between ends the process first, and the shell it hands back to is
// left without echo: whatever is typed next is invisible until `reset`. So for
// as long as the prompt is up, an interrupt puts the terminal back before the
// command exits.
func readHiddenLine(f *os.File) ([]byte, error) {
	fd := f.Fd()
	if state, err := termGetState(fd); err == nil {
		release := onInterrupt(func() {
			_ = termRestore(fd, state)
			_, _ = fmt.Fprintln(os.Stderr)
		})
		defer release()
	}
	return termReadPassword(fd)
}

// The terminal calls readHiddenLine makes, as variables so a test can stand
// in for a terminal it does not have and see what the interrupt handler puts
// back.
var (
	termGetState     = term.GetState
	termRestore      = term.Restore
	termReadPassword = term.ReadPassword
)

// interrupted ends the command after ctrl-c, with the status a shell gives an
// interrupted one. A variable for the tests.
var interrupted = func() { os.Exit(130) }

// onInterrupt runs undo and then interrupted if ctrl-c arrives before release
// is called. Until then the interrupt is this function's to handle, not the
// runtime's, which would exit without undoing anything.
func onInterrupt(undo func()) (release func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			undo()
			interrupted()
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}

// A flag whose value is a credential, with "-" meaning stdin.
//
// Both of these take a secret, and a secret spelled out on a command line is
// not one for long: the shell writes it to ~/.bash_history or ~/.zsh_history,
// and while the command runs `ps` shows the whole line to anything running as
// the member - on Linux /proc/<pid>/cmdline to other accounts as well. A CI
// step or a script can pipe it in instead:
//
//	echo "$NAN_TOKEN" | nan auth login --token -
func flagValue(value string) (string, error) {
	if value != "-" {
		return strings.TrimSpace(value), nil
	}
	// A session token is well under this; the bound keeps a
	// mistaken pipe (a file, a log) from being read into memory whole, and
	// anything longer is refused rather than cut into a credential that is
	// not one.
	const maxValue = 8192
	read, err := io.ReadAll(io.LimitReader(os.Stdin, maxValue+1))
	if err != nil {
		return "", fmt.Errorf("could not read it from stdin: %w", err)
	}
	if len(read) > maxValue {
		return "", fmt.Errorf("more than %d bytes arrived on stdin: that is not a token", maxValue)
	}
	value = strings.TrimSpace(string(read))
	if value == "" {
		return "", fmt.Errorf("nothing arrived on stdin")
	}
	return value, nil
}

// loginWarnings is where `nan auth login` says a credential was typed where
// it stays readable. A variable so the tests can read it.
var loginWarnings io.Writer = os.Stderr

// logoutWarnings is where `nan auth logout` says the platform did not confirm
// the sign-out. A variable so the tests can read it.
var logoutWarnings io.Writer = os.Stderr

func runLogout(cmd *cobra.Command, _ []string) error {
	// End the session on the platform first, while session.json still holds
	// the token: deleting the file alone leaves any copy of the token working
	// until it expires. A platform that cannot be reached does not keep the
	// member signed in here; the file still goes, and they are told.
	if current, err := session.Load(); err == nil && current.Token != "" {
		if err := newAPIClient(current.Token).Logout(contextOf(cmd)); err != nil {
			_, _ = fmt.Fprintf(logoutWarnings, "Could not end the session on the platform (%v); it stays valid until it expires. To end it now: cloud.nan.builders, Settings > Account > Sign out other sessions.\n", err)
		}
	}
	if err := session.Delete(); err != nil {
		return err
	}
	// Unfinished context uploads are remembered per credential, so a push
	// can resume. A machine signed out of keeps nothing of that account.
	if dir, err := ctxUploadStateDir(); err == nil {
		_ = os.RemoveAll(dir)
	}
	fmt.Println("Logged out.")

	// Deleting session.json is only half of what logging out means here. The
	// API key was copied into every tool `nan` configured, and it goes on
	// working from those files: a member who logs out on a machine they are
	// giving back would have been logged out of everything except the cluster
	// their key bills.
	if keepToolsFlag {
		fmt.Println("Your API key is still in the tools - run it again without --keep-tools to take it out.")
		return nil
	}
	removed, failed := tools.RemoveAll()
	if len(removed) > 0 {
		fmt.Println("Removed your API key from " + strings.Join(removed, ", ") + ".")
	}
	if len(failed) > 0 {
		names := make([]string, len(failed))
		for i, f := range failed {
			names[i] = f.Tool + " (" + f.FirstLine() + ")"
		}
		return fmt.Errorf("your API key is still in %s", strings.Join(names, ", "))
	}
	return nil
}

func saveToken(token string) error {
	current, err := session.Load()
	if err != nil {
		current = &session.Session{}
	}
	current.Token = token
	if err := session.Save(current); err != nil {
		return fmt.Errorf("could not save session: %w", err)
	}
	fmt.Println("Logged in successfully.")
	return nil
}

// tokenInput is where `nan auth login --api-token` reads the token from. On
// a terminal it is read without echo; anywhere else, the first line of stdin.
type tokenInput struct {
	r          io.Reader
	tty        bool
	readHidden func() ([]byte, error)
}

// newTokenInput reads from f, hidden when f is a terminal. Asking the
// terminal itself, not the file mode: /dev/null is a character device too,
// and under cron or `docker run` without -i that is what stdin is.
func newTokenInput(f *os.File) tokenInput {
	return tokenInput{
		r:          f,
		tty:        term.IsTerminal(f.Fd()),
		readHidden: func() ([]byte, error) { return readHiddenLine(f) },
	}
}

func (in tokenInput) read(prompt io.Writer) (string, error) {
	if in.tty {
		_, _ = fmt.Fprint(prompt, "Paste the token (it is not shown): ")
		raw, err := in.readHidden()
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("could not read the token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	line, err := bufio.NewReader(io.LimitReader(in.r, maxTokenLen+2)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("could not read the token from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// loginWithAPIToken saves a platform token (or an API key) for the run
// commands, after nan.builders has accepted it. Nothing is saved otherwise,
// and the token is never printed.
func loginWithAPIToken(ctx context.Context, in tokenInput, out, prompt io.Writer) error {
	tok, err := in.read(prompt)
	if err != nil {
		return err
	}
	if tok == "" {
		return fmt.Errorf("no token arrived on stdin: paste it when asked, or pipe it in: nan auth login --api-token < token.txt")
	}
	if !validTokenShape(tok) {
		return fmt.Errorf("that is not a valid token, nothing was saved: %s", tokenShapeHint)
	}

	client := newRunsClient("", tok)
	if _, err := client.ListRuns(ctx, api.ListRunsParams{Limit: 1}); err != nil {
		if errors.Is(err, api.ErrSessionExpired) {
			return fmt.Errorf("nan.builders refused that token (revoked, expired or mistyped), nothing was saved: create one in Settings > Tokens at https://cloud.nan.builders")
		}
		return fmt.Errorf("could not check the token, nothing was saved: %v", apiExit(err, credential{kind: credStoredToken}).Err) //nolint:errorlint // printed, never matched on
	}

	current, err := session.Load()
	switch {
	case errors.Is(err, session.ErrNotLoggedIn):
		current = &session.Session{}
	case err != nil:
		return fmt.Errorf("could not read %s, nothing was saved: %w", session.Path(), err)
	}
	isKey := strings.HasPrefix(tok, "sk-")
	replacedKey := isKey && current.APIKey != "" && current.APIKey != tok
	if isKey {
		current.APIKey = tok
	} else {
		current.PlatformToken = tok
	}
	if err := session.Save(current); err != nil {
		return fmt.Errorf("could not save the token: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Token saved to %s. nan run and nan runs will use it on this machine.\n", session.Path())
	if replacedKey {
		_, _ = fmt.Fprintln(out, "It replaces the API key saved before. Tools the Setup tab configured keep the old key until you apply Setup again.")
	}
	if isKey && current.Token != "" {
		// The session goes first and nothing here knows when it expires, so
		// saying "while it lasts" would promise a fallback that never happens.
		_, _ = fmt.Fprintln(out, "You are also signed in through the browser, and nan run uses that session first, even after it expires. To use only this key: nan auth logout, then save it again.")
	}
	return nil
}

// contextOf is the command's context, or a background one when it is run
// outside Execute, as the tests do.
func contextOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

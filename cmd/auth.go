package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/auth"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/nxssie/nan-cli/internal/tui"
	"github.com/spf13/cobra"
)

var (
	tokenFlag     string
	emailFlag     string
	linkFlag      string
	keepToolsFlag bool
	apiTokenFlag  bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in by email, or save a platform token with --api-token",
	Long: `Log in with a sign-in link sent to your email.

With --api-token, save a platform token instead, for a machine with nobody
at the keyboard (a server, a CI runner). Create the token in Settings >
Tokens at https://cloud.nan.builders. It is read from stdin only, so it
never ends up in your shell history or in ps, checked against nan.builders,
and saved in ~/.config/nan/session.json, readable only by you. Tokens work
for nan run and nan runs; the other commands and the dashboard need the
email sign-in.`,
	Example: `  nan auth login --email you@example.com
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
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(loginCmd)
	authCmd.AddCommand(logoutCmd)
	loginCmd.Flags().StringVar(&emailFlag, "email", "", "Email to send the sign-in link to")
	loginCmd.Flags().StringVar(&linkFlag, "link", "", `Finish the login with the link from the email ("-" reads it from stdin, keeping the token out of your shell history)`)
	loginCmd.Flags().StringVar(&tokenFlag, "token", "", `Save a nan_session token directly, skipping the email ("-" reads it from stdin, keeping it out of your shell history)`)
	loginCmd.Flags().BoolVar(&apiTokenFlag, "api-token", false, "Save a platform token (nan_pat_...) or API key (sk-...) for nan run and nan runs, read from stdin")
	logoutCmd.Flags().BoolVar(&keepToolsFlag, "keep-tools", false, "Leave the API key in the tools this CLI configured")
}

// The platform signs in by emailed link. It used to be Discord OAuth, and this
// command opened https://cloud-api.nan.builders/api/auth/discord, which has
// answered 404 since that flow was retired: the browser landed on an error page
// and the command then asked for a cookie that no longer existed.
func runLogin(cmd *cobra.Command, args []string) error {
	if apiTokenFlag {
		if tokenFlag != "" || linkFlag != "" || emailFlag != "" {
			return fmt.Errorf("--api-token cannot be combined with --email, --link or --token")
		}
		return loginWithAPIToken(cmd.Context(), newTokenInput(os.Stdin), os.Stdout, os.Stderr)
	}
	if tokenFlag != "" {
		token, err := flagValue(tokenFlag)
		if err != nil {
			return err
		}
		return saveToken(token)
	}

	// `--link` picks the flow up at its second half, for a shell that cannot
	// answer a prompt: a script, a CI step, or a terminal that runs one command
	// at a time.
	if linkFlag != "" {
		pasted, err := flagValue(linkFlag)
		if err != nil {
			return err
		}
		token, err := auth.TokenFromLink(pasted)
		if err != nil {
			return err
		}
		sessionToken, err := auth.ExchangeToken(token)
		if err != nil {
			return err
		}
		return saveToken(sessionToken)
	}

	in := bufio.NewScanner(os.Stdin)

	// Both dead ends below name the flag that skips the prompt. They used to
	// say only that the email was missing or malformed, which is the whole
	// story when someone typed it wrong and none of it when the prompt itself
	// never reached them - a terminal that renders a command's output as a
	// block, a shell running one command at a time, anything piping into this.
	// The `--link` half of this flow has said so for a while; this half did
	// not, so the first of the two prompts was the one you could get stuck on.
	const emailHint = "pass it instead:\n\n  nan auth login --email you@example.com"

	email := strings.TrimSpace(emailFlag)
	if email == "" {
		fmt.Print("Email: ")
		if !in.Scan() {
			fmt.Println()
			return fmt.Errorf("nothing to read from here — %s", emailHint)
		}
		email = strings.TrimSpace(in.Text())
	}
	if !strings.Contains(email, "@") {
		if email == "" {
			return fmt.Errorf("no email — %s", emailHint)
		}
		return fmt.Errorf("not an email address: %q — %s", email, emailHint)
	}

	if err := auth.RequestSignInLink(email); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("A sign-in link is on its way to %s.\n", email)
	fmt.Println()
	fmt.Println("Copy the link out of the email. Don't open it in your browser first:")
	fmt.Println("the link works once, and the browser would spend it.")
	fmt.Printf("It also expires %s after it is sent — an email that turns up late\n", auth.LinkValidity)
	// Saying "run this command again" and stopping there sent people at a
	// blocking prompt looking for a way out of it. The empty line they reach
	// for is the one exit that is not one: it comes back as `nothing pasted`
	// and exit 1, which reads like the login failed rather than like they
	// left it.
	fmt.Println("turns up dead. If it has not arrived, leave with ctrl-c and run")
	fmt.Println("this command again.")
	fmt.Println()

	fmt.Print("Paste the link: ")
	if !in.Scan() {
		// Nobody at the keyboard: a pipe, a CI step, a shell that runs one
		// command at a time. The email has already gone out, so this is not a
		// failure to report - it is the second half of the flow, as a command.
		// (Asking the OS whether stdin is a terminal does not settle it on
		// Windows, where NUL is a character device too.)
		fmt.Println()
		fmt.Println("Nothing to read from here. Finish the login with:")
		fmt.Println()
		fmt.Println("  nan auth login --link \"<the link>\"")
		return nil
	}

	token, err := auth.TokenFromLink(strings.TrimSpace(in.Text()))
	if err != nil {
		return err
	}

	sessionToken, err := auth.ExchangeToken(token)
	if err != nil {
		return err
	}
	return saveToken(sessionToken)
}

// A flag whose value is a credential, with "-" meaning stdin.
//
// Both of these take a secret, and a secret spelled out on a command line is
// not one for long: the shell writes it to ~/.bash_history or ~/.zsh_history,
// and while the command runs `ps` shows the whole line to anything running as
// the member - on Linux /proc/<pid>/cmdline to other accounts as well. A CI
// step or a script can pipe it in instead:
//
//	echo "$NAN_LINK" | nan auth login --link -
func flagValue(value string) (string, error) {
	if value != "-" {
		return strings.TrimSpace(value), nil
	}
	read, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("could not read it from stdin: %w", err)
	}
	value = strings.TrimSpace(string(read))
	if value == "" {
		return "", fmt.Errorf("nothing arrived on stdin")
	}
	return value, nil
}

func runLogout(cmd *cobra.Command, args []string) error {
	if err := session.Delete(); err != nil {
		return err
	}
	fmt.Println("Logged out.")

	// Deleting session.json is only half of what logging out means here. The
	// API key was copied into every tool `nan` configured, and it goes on
	// working from those files: a member who logs out on a machine they are
	// giving back would have been logged out of everything except the cluster
	// their key bills.
	if keepToolsFlag {
		fmt.Println("Your API key is still in the tools — run it again without --keep-tools to take it out.")
		return nil
	}
	removed, failed := tui.RemoveNanFromTools()
	if len(removed) > 0 {
		fmt.Println("Removed your API key from " + strings.Join(removed, ", ") + ".")
	}
	if len(failed) > 0 {
		return fmt.Errorf("your API key is still in %s", strings.Join(failed, ", "))
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
		readHidden: func() ([]byte, error) { return term.ReadPassword(f.Fd()) },
	}
}

func (in tokenInput) read(prompt io.Writer) (string, error) {
	if in.tty {
		fmt.Fprint(prompt, "Paste the token (it is not shown): ")
		raw, err := in.readHidden()
		fmt.Fprintln(prompt)
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

	client := api.NewRunsClient("", tok).WithBaseURL(runsBaseURL)
	if _, err := client.ListRuns(ctx, api.ListRunsParams{Limit: 1}); err != nil {
		if errors.Is(err, api.ErrSessionExpired) {
			return fmt.Errorf("nan.builders refused that token (revoked, expired or mistyped), nothing was saved: create one in Settings > Tokens at https://cloud.nan.builders")
		}
		return fmt.Errorf("could not check the token, nothing was saved: %v", apiExit(err, credential{kind: credStoredToken}).Err)
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

	fmt.Fprintf(out, "Token saved to %s. nan run and nan runs will use it on this machine.\n", session.Path())
	if replacedKey {
		fmt.Fprintln(out, "It replaces the API key saved before. Tools the Setup tab configured keep the old key until you apply Setup again.")
	}
	if isKey && current.Token != "" {
		// The session goes first and nothing here knows when it expires, so
		// saying "while it lasts" would promise a fallback that never happens.
		fmt.Fprintln(out, "You are also signed in by email, and nan run uses that session first, even after it expires. To use only this key: nan auth logout, then save it again.")
	}
	return nil
}

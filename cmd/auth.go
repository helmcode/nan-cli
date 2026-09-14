package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/nxssie/nan-cli/internal/auth"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/spf13/cobra"
)

var (
	tokenFlag string
	emailFlag string
	linkFlag  string
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in with a sign-in link sent to your email",
	RunE:  runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out and delete local session",
	RunE:  runLogout,
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(loginCmd)
	authCmd.AddCommand(logoutCmd)
	loginCmd.Flags().StringVar(&emailFlag, "email", "", "Email to send the sign-in link to")
	loginCmd.Flags().StringVar(&linkFlag, "link", "", "Finish the login with the link from the email")
	loginCmd.Flags().StringVar(&tokenFlag, "token", "", "Save a nan_session token directly, skipping the email")
}

// The platform signs in by emailed link. It used to be Discord OAuth, and this
// command opened https://cloud-api.nan.builders/api/auth/discord, which has
// answered 404 since that flow was retired: the browser landed on an error page
// and the command then asked for a cookie that no longer existed.
func runLogin(cmd *cobra.Command, args []string) error {
	if tokenFlag != "" {
		return saveToken(tokenFlag)
	}

	// `--link` picks the flow up at its second half, for a shell that cannot
	// answer a prompt: a script, a CI step, or a terminal that runs one command
	// at a time.
	if linkFlag != "" {
		token, err := auth.TokenFromLink(strings.TrimSpace(linkFlag))
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

func runLogout(cmd *cobra.Command, args []string) error {
	if err := session.Delete(); err != nil {
		return err
	}
	fmt.Println("Logged out.")
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

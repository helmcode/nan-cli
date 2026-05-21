package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/nxssie/nan-cli/internal/session"
)

const apiAuthURL = "https://cloud-api.nan.builders/api/auth/discord"

var tokenFlag string

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in with Discord (opens browser)",
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
	loginCmd.Flags().StringVar(&tokenFlag, "token", "", "Save a nan_session token directly")
}

func runLogin(cmd *cobra.Command, args []string) error {
	if tokenFlag != "" {
		return saveToken(tokenFlag)
	}

	fmt.Println("Opening browser to log in with Discord...")
	openBrowser(apiAuthURL)
	fmt.Println()
	fmt.Println("After logging in:")
	fmt.Println("  1. Open DevTools (F12) → Application → Cookies → cloud-api.nan.builders")
	fmt.Println("  2. Copy the value of the 'nan_session' cookie")
	fmt.Println()
	fmt.Print("Paste nan_session: ")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return fmt.Errorf("no token provided")
	}
	return saveToken(token)
}

func runLogout(cmd *cobra.Command, args []string) error {
	if err := session.Delete(); err != nil {
		return err
	}
	fmt.Println("Logged out.")
	return nil
}

func saveToken(token string) error {
	if err := session.Save(&session.Session{Token: token}); err != nil {
		return fmt.Errorf("could not save session: %w", err)
	}
	fmt.Println("Logged in successfully.")
	return nil
}

func openBrowser(url string) {
	var bin string
	var binArgs []string
	switch runtime.GOOS {
	case "darwin":
		bin = "open"
	case "windows":
		bin = "rundll32"
		binArgs = []string{"url.dll,FileProtocolHandler"}
	default:
		bin = "xdg-open"
	}
	exec.Command(bin, append(binArgs, url)...).Start()
}

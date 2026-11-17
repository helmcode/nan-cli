package auth

import (
	"os"
	"os/exec"
	"runtime"
)

// OpenBrowser tries to open a URL in the member's browser. Best effort: the
// URL is always printed too, so a machine without a browser (SSH, a
// container) loses nothing when this fails. A variable so tests replace it.
//
// Only the validated DeviceLogin.VerificationURL is ever passed in: no user
// input reaches exec.
var OpenBrowser = openBrowser

// BrowserDisabled reports NAN_NO_BROWSER: callers skip OpenBrowser entirely.
func BrowserDisabled() bool { return os.Getenv("NAN_NO_BROWSER") != "" }

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap the child; Enter can reopen many times
	return nil
}

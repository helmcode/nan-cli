// Package browser opens a page in the member's browser, when there is one to
// open it in.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

// Open tries to open a URL in the member's browser. A variable so tests
// replace it.
//
// Only the validated device verification URL is ever passed in (see
// api.Client.StartDevice): no user input reaches exec.
var Open = open

// Disabled reports NAN_NO_BROWSER: callers skip Open entirely.
func Disabled() bool { return os.Getenv("NAN_NO_BROWSER") != "" }

// Possible reports whether opening a browser from this process can work at
// all. It is false in an SSH session, where the browser would open (or fail
// to) on the machine at the other end, and on Linux without a display, where
// xdg-open starts and then fails silently. The caller prints the address
// either way; this only decides whether to try, and whether to say it did.
func Possible() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	return runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// ShouldOpen is whether to try at all: not disabled, and possible here.
func ShouldOpen() bool { return !Disabled() && Possible() }

func open(url string) error {
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

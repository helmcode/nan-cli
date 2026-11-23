package browser

import (
	"runtime"
	"testing"
)

func TestNoBrowserDisablesIt(t *testing.T) {
	t.Setenv("NAN_NO_BROWSER", "")
	if Disabled() {
		t.Error("disabled with the variable empty")
	}
	t.Setenv("NAN_NO_BROWSER", "1")
	if !Disabled() || ShouldOpen() {
		t.Error("NAN_NO_BROWSER=1 must stop it opening")
	}
}

func TestOverSSHItDoesNotTry(t *testing.T) {
	t.Setenv("NAN_NO_BROWSER", "")
	for _, name := range []string{"SSH_CONNECTION", "SSH_TTY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("SSH_TTY", "")
			t.Setenv(name, "x")
			if Possible() || ShouldOpen() {
				t.Errorf("%s set: a browser cannot open from here", name)
			}
		})
	}
}

func TestLinuxNeedsADisplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux decides on a display")
	}
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if Possible() {
		t.Error("no display, and it says it can open a browser")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if !Possible() {
		t.Error("a Wayland session was refused")
	}
}

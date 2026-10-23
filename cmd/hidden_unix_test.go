//go:build unix

package cmd

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
)

// Ctrl-c at a hidden prompt used to end the process while echo was off, and
// the shell stayed that way. While the prompt is up, the interrupt is caught:
// the terminal is put back first, and only then does the command exit.
func TestCtrlCAtAHiddenPromptPutsTheTerminalBackFirst(t *testing.T) {
	var restored atomic.Bool
	exited := make(chan bool, 1)
	swap(t, &interrupted, func() { exited <- restored.Load() })

	release := onInterrupt(func() { restored.Store(true) })
	defer release()

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case restoredFirst := <-exited:
		if !restoredFirst {
			t.Error("exited before putting the terminal back")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl-c at the prompt was not handled")
	}
}

// readHiddenLine hands the interrupt handler the terminal's own state: a
// ctrl-c while the password is being read restores exactly what GetState
// saved, on the same descriptor, before the command exits. Without a real
// terminal, the three calls are stood in for.
func TestCtrlCWhileReadingRestoresTheSavedState(t *testing.T) {
	saved := &term.State{}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	type restore struct {
		fd    uintptr
		state *term.State
	}
	restored := make(chan restore, 1)
	exited := make(chan struct{})
	swap(t, &termGetState, func(uintptr) (*term.State, error) { return saved, nil })
	swap(t, &termRestore, func(fd uintptr, s *term.State) error { restored <- restore{fd, s}; return nil })
	swap(t, &interrupted, func() { close(exited) })
	// The member presses ctrl-c while the prompt is waiting for them.
	swap(t, &termReadPassword, func(uintptr) ([]byte, error) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Error(err)
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("ctrl-c while reading was not handled")
		}
		return nil, os.ErrClosed
	})

	_, _ = readHiddenLine(f)
	select {
	case got := <-restored:
		if got.fd != f.Fd() || got.state != saved {
			t.Errorf("restored fd %d state %p, want fd %d state %p", got.fd, got.state, f.Fd(), saved)
		}
	default:
		t.Error("ctrl-c exited without restoring the terminal")
	}
}

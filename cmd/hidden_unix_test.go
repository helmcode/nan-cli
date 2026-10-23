//go:build unix

package cmd

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

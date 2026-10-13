// Command nan is the nan.builders CLI.
//
// Thin on purpose: wiring and exit codes live here, and everything else lives
// in internal/ where it can be tested without a terminal.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/fang"

	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/cli"
)

func main() {
	// os.Exit does not run deferred functions, so the exit code is decided
	// in run() and spent here.
	os.Exit(run())
}

func run() int {
	// SIGTERM cancels whatever is in flight. Ctrl-C is deliberately NOT
	// taken here: `nan run` owns it while it follows a run - the first one
	// detaches and leaves the run going, a second cancels it - and a context
	// cancelled underneath it would cut the stream before it could decide.
	// Every other command keeps Ctrl-C's ordinary meaning, and the dashboard
	// reads it as a key.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCmd()
	err := fang.Execute(ctx, root,
		fang.WithVersion(build.Version),
		fang.WithColorSchemeFunc(cli.ColorScheme),
		fang.WithErrorHandler(cli.ErrorHandler),
	)
	if err == nil {
		return 0
	}
	cmd, _, _ := root.Find(os.Args[1:])
	return cli.ExitCode(cmd, err)
}

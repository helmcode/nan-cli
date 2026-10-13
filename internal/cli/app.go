package cli

import (
	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/session"
)

// app is what the dashboard-side commands are handed: somewhere to print, a
// session, and a client that carries it.
//
// It is built once per invocation, which is what keeps each command file down
// to the thing it actually does rather than five lines of the same wiring.
// The run commands authenticate their own way (see credentials.go) and do
// not use it.
type app struct {
	p       *Printer
	session *session.Session
	client  *api.Client
}

func newApp(asJSON, noColor bool) *app {
	s := session.LoadOrEmpty()
	return &app{
		p:       NewPrinter(asJSON, noColor),
		session: s,
		client:  newAPIClient(s.Token),
	}
}

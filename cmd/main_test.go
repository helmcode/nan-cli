package cmd

import (
	"os"
	"testing"
	"time"
)

// TestMain pins the clock's zone before any test or goroutine starts.
//
// Rendered times (runs ls, golden views) go through t.Local(), so the tests
// need one fixed zone. Swapping time.Local inside a test races with every
// goroutine that calls time.Now() meanwhile, including net/http servers left
// over from earlier tests; set once here, nothing reads it concurrently.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

// swap sets *p to v for the rest of the test and puts the old value back
// when it ends, whatever order the tests run in.
func swap[T any](t testing.TB, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

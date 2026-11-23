package tools

import (
	"os"
	"testing"
)

// No test runs the member's codex: the version is pinned to the one this
// suite's goldens describe, and a test about another version swaps it itself.
func TestMain(m *testing.M) {
	codexVersion = func() string { return "codex-cli 0.169.0" }
	os.Exit(m.Run())
}

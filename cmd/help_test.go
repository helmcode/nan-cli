package cmd

import (
	"strings"
	"testing"
)

const runsDocs = "https://nan.builders/docs/runs"

// The help is where a member learns what a run does to their repository and
// how to use the CLI with nobody at the keyboard. These are the facts it has
// to carry.
func TestRunHelpCarriesTheEssentials(t *testing.T) {
	help := runCmd.Long + "\n" + runCmd.Example
	for _, want := range []string{
		"nan-run/<first 8 chars of the run id>",
		"never", // never pushes
		"checkout is untouched",
		"--worktree", "fails outside a repository",
		"--no-worktree",
		"waits as queued",
		"Folder = --cwd", "Separate branch = --worktree/--no-worktree", "Time limit = --timeout", "Task = the prompt",
		"NAN_TOKEN", "--token-file PATH", "nan auth login", "nan auth login --api-token",
		"Settings > Tokens",
		runsDocs,
	} {
		if !strings.Contains(help, want) {
			t.Errorf("nan run --help does not mention %q", want)
		}
	}
}

func TestRunsHelpLinksTheDocs(t *testing.T) {
	if !strings.Contains(runsCmd.Long, runsDocs) || !strings.Contains(runsCmd.Long, "NAN_TOKEN") {
		t.Errorf("nan runs --help: %q", runsCmd.Long)
	}
	for _, want := range []string{"tool_call", "tool_result", "message", "finished", "--json", "one event per line", runsDocs} {
		if !strings.Contains(runsLogsCmd.Long, want) {
			t.Errorf("nan runs logs --help does not mention %q", want)
		}
	}
}

// House style for new help text: no em-dashes.
func TestNewHelpHasNoEmDashes(t *testing.T) {
	for name, text := range map[string]string{
		"run":        runCmd.Long + runCmd.Example,
		"runs":       runsCmd.Long,
		"runs logs":  runsLogsCmd.Long,
		"auth login": loginCmd.Long + loginCmd.Example,
		"token-file": tokenFileUsage,
	} {
		if strings.Contains(text, "—") {
			t.Errorf("%s help has an em-dash", name)
		}
	}
}

// --token-file is accepted by nan run and by every nan runs subcommand.
func TestTokenFileFlagIsOnEveryRunsCommand(t *testing.T) {
	if runCmd.Flags().Lookup("token-file") == nil {
		t.Error("nan run has no --token-file")
	}
	for _, c := range runsCmd.Commands() {
		if c.InheritedFlags().Lookup("token-file") == nil && c.Flags().Lookup("token-file") == nil {
			t.Errorf("nan runs %s has no --token-file", c.Name())
		}
	}
}

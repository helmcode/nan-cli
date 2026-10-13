// Package tools points the coding tools on this machine at NaN, and takes
// them back off it.
//
// Only installed tools are touched, only NaN's part of each config is
// written, and a tool that is unticked has ours taken back out. Every tool
// is its own failure: one that cannot be configured never stops the rest.
package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Tool is one coding tool this CLI knows how to configure.
type Tool struct {
	Name   string
	Binary string
	// ConfigPath is the file that ends up pointing at NaN.
	ConfigPath string
	// InstallPath, when set, is what says the tool is installed when its
	// binary is not on PATH; otherwise the config file is.
	InstallPath string
	// Open and NextStep are how to check it works once it is configured:
	// the command, and what to do in it. They are the steps each tool's page
	// on nan.builders publishes under "check that it works".
	Open     string
	NextStep string

	apply      func(t Tool, apiKey string) error
	remove     func(t Tool) error
	configured func(path string) bool
	// keyFiles are the files that hold the key once it is written. For
	// every tool but Hermes that is the config itself.
	keyFiles func(t Tool) []string
}

// Status is a tool as found on this machine.
type Status struct {
	Tool
	Installed  bool
	Configured bool
}

// Registry is every tool this version knows, in the order they are listed.
func Registry() []Tool {
	home, _ := os.UserHomeDir()
	hermes := hermesHome()
	return []Tool{
		{
			Name:       "Factory AI",
			Binary:     "droid",
			ConfigPath: filepath.Join(home, ".factory", "settings.json"),
			Open:       "droid",
			NextStep:   "pick a model with (NaN) in its name",
			apply:      func(t Tool, key string) error { return writeFactoryConfig(t.ConfigPath, key) },
			remove:     func(t Tool) error { return removeFactoryConfig(t.ConfigPath) },
			configured: factoryConfigured,
		},
		{
			Name:       "OpenCode",
			Binary:     "opencode",
			ConfigPath: filepath.Join(home, ".config", "opencode", "opencode.json"),
			Open:       "opencode",
			NextStep:   "then /models inside it, and pick a NaN one",
			apply:      func(t Tool, key string) error { return writeOpencodeConfig(t.ConfigPath, key) },
			remove:     func(t Tool) error { return removeOpencodeConfig(t.ConfigPath) },
			configured: opencodeConfigured,
		},
		{
			Name:        "Pi",
			Binary:      "pi",
			ConfigPath:  filepath.Join(home, ".pi", "agent", "models.json"),
			InstallPath: filepath.Join(home, ".pi"),
			Open:        "pi",
			NextStep:    "NaN is already its default provider",
			apply:       func(t Tool, key string) error { return writePiConfig(t.ConfigPath, key) },
			remove:      func(t Tool) error { return removePiConfig(t.ConfigPath) },
			configured:  piConfigured,
		},
		{
			Name:       "Codex",
			Binary:     "codex",
			ConfigPath: filepath.Join(home, ".codex", "config.toml"),
			Open:       "codex",
			NextStep:   "already aimed at the cluster; codex --model <id> to switch",
			apply:      func(t Tool, key string) error { return writeCodexConfig(t.ConfigPath, key) },
			remove:     func(t Tool) error { return removeCodexConfig(t.ConfigPath) },
			configured: mentionsNaN,
		},
		{
			// Not a coding agent like the rest: it lives in the member's
			// messaging channels. It is here because connecting it is the
			// same two values, and because it is the one tool that tells us
			// where its config is instead of making us guess.
			Name:        "Hermes",
			Binary:      "hermes",
			ConfigPath:  hermesConfigPath(hermes),
			InstallPath: hermes,
			Open:        "hermes doctor",
			NextStep:    "says whether the provider answers, then talk to it",
			apply:       func(t Tool, key string) error { return writeHermesConfig(filepath.Dir(t.ConfigPath), key) },
			remove:      func(t Tool) error { return removeHermesConfig(filepath.Dir(t.ConfigPath)) },
			configured:  mentionsNaN,
			// Its config.yaml carries a reference; the secret is in its .env.
			keyFiles: func(t Tool) []string { return []string{hermesEnvPath(filepath.Dir(t.ConfigPath))} },
		},
	}
}

// List names every tool, for a sentence: "Factory AI, OpenCode, Pi, Codex
// and Hermes".
func List() string {
	names := make([]string, 0, len(Registry()))
	for _, t := range Registry() {
		names = append(names, t.Name)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Detect looks for every tool on this machine. A tool is installed when its
// binary is on PATH or its install directory exists: a member who installed
// it somewhere PATH does not reach still has the config we would write.
func Detect() []Status {
	registry := Registry()
	out := make([]Status, 0, len(registry))
	for _, t := range registry {
		st := Status{Tool: t}
		_, binErr := exec.LookPath(t.Binary)
		probe := t.ConfigPath
		if t.InstallPath != "" {
			probe = t.InstallPath
		}
		_, pathErr := os.Stat(probe)
		st.Installed = binErr == nil || pathErr == nil
		if st.Installed && t.configured != nil {
			st.Configured = t.configured(t.ConfigPath)
		}
		out = append(out, st)
	}
	return out
}

// Installed keeps the ones on this machine.
func Installed(all []Status) []Status {
	out := make([]Status, 0, len(all))
	for _, s := range all {
		if s.Installed {
			out = append(out, s)
		}
	}
	return out
}

// Enabled reads whether a tool is ticked. A tool nobody has unticked is.
func Enabled(ticked map[string]bool, name string) bool {
	if v, ok := ticked[name]; ok {
		return v
	}
	return true
}

// Apply writes NaN into the tool, then makes sure every file holding the key
// is private to the member.
func (t Tool) Apply(apiKey string) error {
	if t.apply == nil {
		return fmt.Errorf("%s cannot be configured by this version", t.Name)
	}
	if err := t.apply(t, apiKey); err != nil {
		return err
	}
	// Every writer can decide it has nothing to change - a tool already
	// pointing at the cluster is left alone - and that file is exactly the
	// one that has been sitting there with a key in it since before this
	// CLI tightened modes on its way past. So the mode is checked whether
	// or not anything was written.
	return t.tightenKeyFiles()
}

// Remove takes NaN back out of the tool. Every remover is a no-op where
// there is nothing of ours to take.
func (t Tool) Remove() error {
	if t.remove == nil {
		return nil
	}
	return t.remove(t)
}

func (t Tool) keyPaths() []string {
	if t.keyFiles != nil {
		return t.keyFiles(t)
	}
	return []string{t.ConfigPath}
}

// tightenKeyFiles puts a key file's mode right on its own. A config written
// before this CLI wrote through a temp file kept whatever mode the tool gave
// it, which for most of them is 0644.
func (t Tool) tightenKeyFiles() error {
	if runtime.GOOS == "windows" {
		// Not the mechanism there: the file inherits the ACL of the
		// profile directory it sits in.
		return nil
	}
	for _, path := range t.keyPaths() {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 == 0 {
			continue
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Failure is what went wrong for one tool, kept apart from the others so a
// failure in one does not read as a failure in all of them.
type Failure struct {
	Tool string
	Err  error
}

// FirstLine of the reason, for a summary. A tool that is configured by
// running it - Hermes - hands back whatever it printed, which can be a
// paragraph.
func (f Failure) FirstLine() string {
	reason := f.Err.Error()
	if i := strings.IndexAny(reason, "\r\n"); i >= 0 {
		return strings.TrimSpace(reason[:i])
	}
	return strings.TrimSpace(reason)
}

// Result is what one Apply did.
type Result struct {
	// Written is every tool configured, in order, for saying how to use
	// each one next.
	Written []string
	Removed int
	Failed  []Failure
}

// Summary is the result in one line: "3 added  ·  1 removed  ·  Hermes
// failed", or "nothing to sync".
//
// One tool failing used to throw away everything that worked: a member with
// a broken Hermes was told the whole step had failed. So every part is said.
func (r Result) Summary() string {
	var parts []string
	if n := len(r.Written); n > 0 {
		parts = append(parts, fmt.Sprintf("%d added", n))
	}
	if r.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", r.Removed))
	}
	for _, f := range r.Failed {
		parts = append(parts, f.Tool+" failed")
	}
	if len(parts) == 0 {
		return "nothing to sync"
	}
	return strings.Join(parts, "  ·  ")
}

// Apply configures every installed tool that is ticked and takes NaN out of
// every installed one that is not, which is how unticking a tool undoes it.
func Apply(apiKey string, ticked map[string]bool) Result {
	var r Result
	for _, st := range Installed(Detect()) {
		if Enabled(ticked, st.Name) {
			if err := st.Apply(apiKey); err != nil {
				r.Failed = append(r.Failed, Failure{Tool: st.Name, Err: err})
				continue
			}
			r.Written = append(r.Written, st.Name)
			continue
		}
		if st.Configured {
			if err := st.Remove(); err != nil {
				r.Failed = append(r.Failed, Failure{Tool: st.Name, Err: err})
				continue
			}
			r.Removed++
		}
	}
	return r
}

// RemoveAll takes the key back out of every tool, which is half of signing
// out: the key was copied into as many as five files that have nothing to do
// with the session, and it goes on working from there.
//
// Installed is the only gate, not Configured. Configured is read off the
// tool's own config, and the case that matters most is where that file is
// not the whole story: a run that wrote Hermes' .env and then failed before
// its config.yaml left a key on disk that a Configured gate would walk past.
func RemoveAll() (removed []string, failed []Failure) {
	for _, st := range Installed(Detect()) {
		if err := st.Remove(); err != nil {
			failed = append(failed, Failure{Tool: st.Name, Err: err})
			continue
		}
		if st.Configured {
			removed = append(removed, st.Name)
		}
	}
	return removed, failed
}

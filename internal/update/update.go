// Package update knows whether this copy of the CLI is out of date, how to
// update it, and what has changed since the last time it looked: any model
// the member's key can call that it could not before.
//
// All of it is remembered in one small file in the user's cache - not the
// session, because none of it is a credential and all of it can be thrown
// away. Losing it costs one repeated "new model" and one extra version
// check; nothing else depends on it.
package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nxssie/nan-cli/internal/build"
)

// CheckEvery is how often the latest version is asked for: often enough to
// hear about a release within a day, rarely enough that almost no run pays
// for a request it did not need.
const CheckEvery = 24 * time.Hour

// State is what is remembered between runs.
type State struct {
	// KnownModels is every model the organisation could use last time, to
	// tell which ones are new. Empty until the first look.
	KnownModels []string `json:"knownModels,omitempty"`
	// NewModels is what the last comparison found, kept until the list is
	// opened, so a model announced once is not lost on the next run.
	NewModels []string `json:"newModels,omitempty"`
	// Latest is the newest release seen, and when it was asked for.
	Latest    string `json:"latest,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// Path is the state file.
func Path() string {
	// XDG_CACHE_HOME when set, on every platform, as the session does with
	// XDG_CONFIG_HOME; otherwise the OS's cache directory.
	if xdg := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, build.Name, "state.json")
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, build.Name, "state.json")
}

// Load reads the state; a missing or unreadable file is an empty one.
func Load() State {
	var s State
	//nolint:gosec // G304: this package's own file
	data, err := os.ReadFile(Path())
	if err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

// Save writes the state, through a temp file and a rename so a run that is
// killed halfway never leaves half a file for the next to trip on.
func (s State) Save() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ── versions ─────────────────────────────────────────────────────────────

// DueForCheck is whether the latest version should be asked for again.
func (s State) DueForCheck(now time.Time) bool {
	t, err := time.Parse(time.RFC3339, s.CheckedAt)
	return err != nil || now.Sub(t) >= CheckEvery
}

// Available is the newer release this copy should update to, or "".
//
// Nothing for a development build: one built from a working copy is ahead
// of or beside the last release, and telling somebody hacking on the CLI to
// install the release they are replacing is noise.
func (s State) Available() string {
	if s.Latest == "" || strings.Contains(build.Version, "-dev") {
		return ""
	}
	if Newer(s.Latest, build.Version) {
		return s.Latest
	}
	return ""
}

// Newer reports whether version a is later than b. Semantic versions,
// with a leading v ignored, and a pre-release (0.2.0-rc.1) older than its
// release (0.2.0). Anything that does not parse is never newer, so a
// malformed answer from the server cannot nag.
func Newer(a, b string) bool {
	av, apre, ok1 := parse(a)
	bv, bpre, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range av {
		if av[i] != bv[i] {
			return av[i] > bv[i]
		}
	}
	// Same numbers: a release is newer than a pre-release of it.
	return apre == "" && bpre != ""
}

func parse(v string) (nums [3]int, pre string, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// Command is what to run to update, worked out from how this copy was
// installed. The server cannot know that; only the binary's own path can
// tell.
func Command() string {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return commandFor(runtime.GOOS, exe, os.Getenv("GOPATH"))
}

func commandFor(goos, exe, gopath string) string {
	dir := filepath.Dir(exe)
	switch {
	// go install first: a copy built by it is replaced by it, on Windows too.
	case isGoBin(dir, gopath):
		return "go install github.com/nxssie/nan-cli/cmd/nan@latest"
	case goos == "windows":
		return "irm https://nan.builders/install.ps1 | iex"
	case dir == "/usr/local/bin" || dir == ".":
		return "curl -fsSL https://nan.builders/install | bash"
	}
	// The install script to the same place it went last time, or the new
	// copy lands in /usr/local/bin and the old one keeps winning on PATH.
	return "INSTALL_DIR=" + dir + " curl -fsSL https://nan.builders/install | bash"
}

func isGoBin(dir, gopath string) bool {
	if gopath != "" && dir == filepath.Join(gopath, "bin") {
		return true
	}
	home, _ := os.UserHomeDir()
	return home != "" && dir == filepath.Join(home, "go", "bin")
}

// ── models ───────────────────────────────────────────────────────────────

// SeeModels compares the models available now with the ones known, records
// any that are new, and reports them. The first look records without
// reporting: everything is "new" to a machine that has never looked, and
// announcing the whole catalogue would bury the one that matters later.
func (s *State) SeeModels(now []string) []string {
	if len(now) == 0 {
		return s.NewModels
	}
	if len(s.KnownModels) == 0 {
		s.KnownModels = sortedCopy(now)
		return nil
	}
	known := map[string]bool{}
	for _, m := range s.KnownModels {
		known[m] = true
	}
	for _, m := range now {
		if !known[m] && !contains(s.NewModels, m) {
			s.NewModels = append(s.NewModels, m)
		}
	}
	s.KnownModels = sortedCopy(now)
	return s.NewModels
}

// MarkSeen is the new models having been looked at: they stop being new.
func (s *State) MarkSeen() {
	s.NewModels = nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

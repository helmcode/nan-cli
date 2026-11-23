package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	catalog "github.com/helmcode/nan-cli/internal/models"
)

const (
	codexVersionTimeout   = 3 * time.Second
	codexVersionWaitDelay = 500 * time.Millisecond
)

// Swapped in tests, so nothing here spawns a process. "codex-cli 0.169.0" on
// every release this CLI has met; empty when there is no codex on PATH (the
// desktop app ships its own) or it would not answer.
var codexVersion = func() string { return probeCodexVersion(codexVersionTimeout) }

// Runs `codex --version`. A codex that hangs is an unknown version, not a
// stuck setup. The context kills only the process it started, and npm's
// codex is a wrapper whose native child keeps the output pipe open after
// that; WaitDelay is what stops Output from waiting on it.
func probeCodexVersion(timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "--version") //nolint:gosec // G204: fixed arguments, the member's own codex
	cmd.WaitDelay = codexVersionWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// A config key, as the table it lives in and its name.
type codexSwitch struct{ table, key string }

var (
	codexAgentsEnabled      = codexSwitch{"agents", "enabled"}
	codexFeaturesMultiAgent = codexSwitch{"features", "multi_agent"}
)

// The switch this Codex reads, and the one it does not.
//
// The key that switches sub-agents off moved in 0.168: [agents].enabled
// arrived and [features].multi_agent started printing a deprecation warning on
// every run. Older releases do not know the new key - they load the file and
// ignore it, and go on sending the sub-agent tools the cluster cannot stream.
// A version that cannot be read is taken to be a current one.
func codexSubAgentSwitches(version string) (use, other codexSwitch) {
	if codexBefore168(version) {
		return codexFeaturesMultiAgent, codexAgentsEnabled
	}
	return codexAgentsEnabled, codexFeaturesMultiAgent
}

func (s codexSwitch) off() string {
	return "[" + s.table + "]\n" + s.key + " = false\n"
}

// Whether the version string ("codex-cli 0.167.2") names a release older than
// 0.168, which still reads [features].multi_agent. Anything unreadable is not.
func codexBefore168(version string) bool {
	fields := strings.Fields(version)
	if len(fields) == 0 {
		return false
	}
	parts := strings.SplitN(fields[len(fields)-1], ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return false
	}
	return major == 0 && minor < 168
}

func writeCodexConfig(cfgPath, apiKey string, cat []catalog.Model) error {
	data, _ := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	use, other := codexSubAgentSwitches(codexVersion())
	// The model a starter config opens with: the coding model, or - when the
	// member cannot call it - one they can. Its window is the platform's, and
	// is left out when nobody reported one.
	startID := catalog.Pick(cat, catalog.Coding)
	window := 0
	for _, m := range cat {
		if m.ID == startID {
			window = m.Context
		}
	}
	// The window older versions of this CLI wrote, unmarked, for the coding
	// model: still ours to take out.
	legacy, _ := catalog.Get(catalog.Coding)

	if strings.Contains(string(data), "api.nan.builders") {
		// Already pointing at the cluster, so there is nothing to add - but
		// the section may be one we wrote back when it said wire_api =
		// "chat", and Codex 0.154 will not load a config with that value at
		// all: it prints the error and exits before the TUI is up. A member
		// whose Codex does not open is the last member who can be asked to
		// disconnect and reconnect it, so the repair happens here.
		//
		// The same member is also the one most likely to be carrying a
		// model_context_window we appended once per run, back when it went to
		// the end of the file. Two of them is a duplicate key, and Codex
		// refuses the whole file for it - the desktop app too, which opens on
		// an error dialog and nothing else. Repairing the wire_api alone left
		// that member exactly as stuck as before, so the pruning runs first
		// and puts the key back where it is read from.
		repaired, pruned := codexContextWindowsPruned(data, legacy.Context)
		if codexRootProviderIsNaN(string(repaired)) {
			// Always: it is idempotent and steps aside for a window the
			// member declared, and a root the member pointed at NaN by hand
			// after setup left it on their own provider has none yet.
			withWindow := withCodexContextWindow(string(repaired), window)
			if withWindow != string(repaired) {
				repaired, pruned = []byte(withWindow), true
			}
		} else if swept, ok := codexRootWindowMarkDropped(repaired); ok {
			// An earlier version put its marked window at the root of a
			// config that stays on the member's own provider.
			repaired, pruned = swept, true
		}
		rewired, changed := codexWireAPIRepaired(repaired)
		defaulted := withCodexNaNSessionDefaults(string(rewired), filepath.Dir(cfgPath), use, other)
		if pruned || changed || defaulted != string(rewired) {
			if err := writeConfigFile(cfgPath, []byte(defaulted)); err != nil {
				return err
			}
		}
		return writeCodexProfiles(filepath.Dir(cfgPath), use, cat)
	}

	// If no existing config, write a complete starter config.
	if len(data) == 0 {
		windowLine := ""
		if window > 0 {
			windowLine = codexWindowLine(window) + "\n"
		}
		content := fmt.Sprintf(`model = %q
model_provider = "nan"
%s
[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "responses"
`, startID, windowLine, apiKey)
		content = withCodexNaNSessionDefaults(content, filepath.Dir(cfgPath), use, other)
		if err := writeConfigFile(cfgPath, []byte(content)); err != nil {
			return err
		}
		return writeCodexProfiles(filepath.Dir(cfgPath), use, cat)
	}

	// Existing config: only append the provider section; preserve user's model/provider choices.
	section := fmt.Sprintf(`
[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "responses"
`, apiKey)
	content := strings.TrimRight(string(data), "\n") + "\n" + section
	// The window is for the model the root runs. On the member's own provider
	// that is their model, not one of ours, so the profiles carry it instead.
	if codexRootProviderIsNaN(content) {
		content = withCodexContextWindow(content, window)
	}
	// A root that already says model_provider = "nan" is a NaN session too.
	content = withCodexNaNSessionDefaults(content, filepath.Dir(cfgPath), use, other)
	if err := writeConfigFile(cfgPath, []byte(content)); err != nil {
		return err
	}
	return writeCodexProfiles(filepath.Dir(cfgPath), use, cat)
}

// What a --profile run already gets, given to the default run when the
// default run is a NaN session - which is only when this CLI wrote the
// starter config, the one place it sets model_provider = "nan" at the root.
// The catalogue gives Codex metadata for the model (no "Model metadata ...
// not found" warning, the window and modalities it is served at), and web
// search and sub-agents go for the reason writeCodexProfiles gives. A root
// whose provider is the member's own is left alone: those keys would take
// OpenAI's model list, web search and sub-agents from their OpenAI sessions.
//
// Missing keys only, so a second run adds nothing and a value the member set
// stands. The one thing taken out is the other sub-agent switch with the
// value this CLI writes, so a Codex upgraded or downgraded across 0.168 is
// not left with a key it warns about or ignores. Anything this cannot do
// cleanly - a table defined inline or by dotted keys - it leaves alone, and
// an edit whose result does not parse to exactly what was meant is dropped.
func withCodexNaNSessionDefaults(content, codexHome string, use, other codexSwitch) string {
	doc, ok := codexTOML(content)
	if !ok || doc["model_provider"] != "nan" {
		return content
	}
	want, _ := codexTOML(content)
	lines := codexTableKeyDropped(strings.Split(content, "\n"), want, other.table, other.key, false)

	var root []string
	if _, has := doc["model_catalog_json"]; !has {
		path := codexCatalogPath(codexHome)
		root = append(root, fmt.Sprintf("model_catalog_json = %q", path))
		want["model_catalog_json"] = path
	}
	if _, has := doc["web_search"]; !has {
		root = append(root, `web_search = "disabled"`)
		want["web_search"] = "disabled"
	}

	var block []string // a new [table] before the first header
	if table, has := want[use.table]; !has {
		block = strings.Split(strings.TrimRight(use.off(), "\n"), "\n")
		want[use.table] = map[string]any{use.key: false}
	} else if tbl, isTable := table.(map[string]any); isTable {
		if _, set := tbl[use.key]; !set {
			// Under the table's own header, when it has one; an inline table
			// or one made of dotted keys is not edited.
			if i := codexTableHeader(lines, use.table); i >= 0 {
				lines = append(lines[:i+1], append([]string{use.key + " = false"}, lines[i+1:]...)...)
				tbl[use.key] = false
			}
		}
	}

	rootEnd := codexRootEnd(lines)
	// Root keys right after the last root key: below a header TOML reads
	// them as keys of that table.
	last := -1
	for i := 0; i < rootEnd; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
		}
	}
	out := append([]string{}, lines[:last+1]...)
	out = append(out, root...)
	rest := strings.TrimLeft(strings.Join(lines[last+1:], "\n"), "\n")
	if len(block) > 0 {
		out = append(out, "")
		out = append(out, block...)
	}
	if rest != "" {
		out = append(out, "", rest)
	} else {
		out = append(out, "")
	}
	edited := strings.Join(out, "\n")
	if !codexEditMeans(edited, want) {
		return content
	}
	return edited
}

// One file per chat model, so every model on the cluster is a flag away with
// the window it is actually served at.
//
// `codex --model <id>` switches the model and nothing else, and the
// model_context_window in config.toml stays where it was: point it at a
// 262,144-token model with a million-token window declared and Codex compacts
// against a window that is not there. Codex 0.155 layers
// $CODEX_HOME/<name>.config.toml over the base config for `--profile <name>`,
// which is the one place a per-model window can live without a second copy of
// the member's provider, MCP servers and permissions.
//
// No API key goes in them: the provider section in config.toml is what
// carries it, and a key in eight files is a key to rotate in eight files.
//
// They also switch off the two tools Codex adds on its own. Sub-agents go
// with [agents].enabled = false on Codex 0.168+, and with
// [features].multi_agent = false before it, which 0.168 deprecated with a
// warning on every run. Codex 0.159
// sends the sub-agent tools as a "namespace" tool and web search as a
// "web_search" one, and with either in the request the cluster's /responses
// stops streaming items for qwen3.8-flash and mimo: all that comes back is
// response.completed, which Codex does not read an answer from, so the turn
// ends with no text and no tool call. Neither tool has anywhere to go on the
// cluster anyway. They go here because these files are ours and leave with
// the disconnect. A config.toml whose root provider is nan - one this CLI
// created - gets them too (withCodexNaNSessionDefaults) and they leave with
// the disconnect as well; a root whose provider is the member's own never
// does, since they would take search and the model list from their OpenAI
// sessions.
func writeCodexProfiles(codexHome string, subAgents codexSwitch, cat []catalog.Model) error {
	// Starting from none, so a model the cluster retired does not leave its
	// profile behind to answer 401 on `--profile`.
	if err := removeCodexProfiles(codexHome); err != nil {
		return err
	}
	models, err := codexCatalog(cat)
	if err != nil {
		return err
	}
	if err := writeConfigFile(filepath.Join(codexHome, codexCatalogFile), models); err != nil {
		return err
	}
	for _, m := range cat {
		windowLine := ""
		if m.Context > 0 {
			windowLine = fmt.Sprintf("model_context_window = %d\n", m.Context)
		}
		body := fmt.Sprintf(`# Written by the NaN CLI. Disconnecting Codex removes it.
model = %q
model_provider = "nan"
%smodel_catalog_json = %q
web_search = "disabled"

%s`, m.ID, windowLine, codexCatalogPath(codexHome), subAgents.off())
		path := filepath.Join(codexHome, codexProfileName(m.ID)+".config.toml")
		if err := writeConfigFile(path, []byte(body)); err != nil {
			return err
		}
	}
	return nil
}

// Codex takes "a plain name" for --profile and rejects a dot in it outright,
// so the ids that carry a version number cannot be used as they are spelled:
// glm5.3-flash is refused, glm53-flash is not. The nan- prefix is what makes
// the file ours to delete later, and what keeps it clear of a profile the
// member wrote themselves.
func codexProfileName(id string) string {
	return "nan-" + strings.ReplaceAll(id, ".", "")
}

// Takes back only the files this CLI wrote: the profiles, by the prefix it
// wrote them under, and the catalogue they point at.
func removeCodexProfiles(codexHome string) error {
	entries, err := os.ReadDir(codexHome)
	if err != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	for _, e := range entries {
		name := e.Name()
		ours := strings.HasPrefix(name, "nan-") && strings.HasSuffix(name, ".config.toml")
		if !e.IsDir() && (ours || name == codexCatalogFile) {
			if err := os.Remove(filepath.Join(codexHome, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// The root window carries a mark, so taking NaN out later knows the line is
// ours whatever number the platform reported - a figure that moves with the
// catalogue cannot be recognised by its value. Keys written before the mark
// existed are matched by the value they had.
const codexWindowMark = "# nan-cli"

func codexWindowLine(window int) string {
	return fmt.Sprintf("model_context_window = %d %s", window, codexWindowMark)
}

func codexWindowIsOurs(value, legacy string) bool {
	v := strings.TrimSpace(value)
	return strings.HasSuffix(v, codexWindowMark) || v == legacy
}

// codexStep is how to open Codex once it is configured. A root on NaN is aimed at the
// cluster, so the tool's static step is true. On the member's own provider
// plain `codex` still talks to that provider, and `--model <id>` would send
// it a NaN id: the way in is the profile of the model setup would have
// started with.
func codexStep(t Tool, cat []catalog.Model) (Step, bool) {
	data, err := os.ReadFile(t.ConfigPath) //nolint:gosec // G304: a config file at a path this program chose
	// Re-read rather than threaded out of the write, which only reports an
	// error. A config that cannot be read is not known to be on NaN, and the
	// profile works either way, where "already aimed" could be false.
	if err == nil && codexRootProviderIsNaN(string(data)) {
		return Step{}, false
	}
	id := catalog.Pick(cat, catalog.Coding)
	if id == "" {
		return Step{}, false
	}
	return Step{
		Open: "codex --profile " + codexProfileName(id),
		Next: "yours stays the default; more: nan-* profiles",
	}, true
}

// Whether the root table sends Codex to NaN. A root with no model_provider is
// on Codex's own default, and a file that does not parse is not known to be
// on NaN: neither is ours to put keys in.
func codexRootProviderIsNaN(content string) bool {
	doc, ok := codexTOML(content)
	return ok && doc["model_provider"] == "nan"
}

// Takes the marked window out of the root table, for a root that is not on
// NaN. Only the mark identifies it: an unmarked key is the member's, whatever
// its value. The blank line it was written with goes too when it would leave
// a double gap. Not ok when there was none to take or the file is not valid
// TOML.
func codexRootWindowMarkDropped(data []byte) ([]byte, bool) {
	lines := strings.Split(string(data), "\n")
	rootEnd := codexRootEnd(lines)
	out := make([]string, 0, len(lines))
	dropped := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); i < rootEnd && ok &&
			strings.TrimSpace(key) == "model_context_window" && strings.HasSuffix(strings.TrimSpace(value), codexWindowMark) {
			dropped = true
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" &&
				i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
			continue
		}
		out = append(out, line)
	}
	if !dropped {
		return data, false
	}
	// Lines are read one at a time, which a multi-line string can fool: the
	// edit stands only when a re-parse shows it changed nothing but windows.
	after := strings.Join(out, "\n")
	if !codexOnlyWindowsDiffer(string(data), after) {
		return data, false
	}
	return []byte(after), true
}

// Codex has no metadata for a model on this cluster, so without
// model_context_window it compacts against a window it guessed. (It prints
// the "metadata not found" warning either way - that fires before it reads
// the override.)
//
// The key is a root key, so it belongs above the first table header.
// Appended at the end of the file the way the section around it is, it lands
// inside whatever table came last - for a member with [projects.*] entries it
// became a key of the last project they trusted, and Codex never looked
// there.
func withCodexContextWindow(content string, window int) string {
	if window <= 0 {
		return content // nobody reported one: Codex keeps its own
	}
	declared := codexWindowLine(window)
	lines := strings.Split(content, "\n")
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		// Everything above the first table header is the root table, so this
		// only ever reads the member's root keys.
		if strings.HasPrefix(trimmed, "[") {
			head := strings.TrimRight(strings.Join(lines[:i], "\n"), "\n")
			tail := strings.Join(lines[i:], "\n")
			if head == "" {
				return declared + "\n\n" + tail
			}
			return head + "\n" + declared + "\n\n" + tail
		}
		if key, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(key) == "model_context_window" {
			return content // the member declared one of their own; it stands
		}
	}
	return strings.TrimRight(content, "\n") + "\n" + declared + "\n"
}

// [projects."/home/member/repo"] and the [projects] table above it. This is
// the only shape the appending bug could put a key into: it wrote at the end
// of the file, and what sits at the end of a config.toml is the last project
// the member trusted.
func isCodexProjectsHeader(trimmed string) bool {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
	return inner == "projects" || strings.HasPrefix(inner, "projects.")
}

// Takes out every model_context_window this CLI is responsible for putting
// somewhere it does not belong, and leaves at most one standing.
//
// Two of them anywhere in the file is a duplicate key, and Codex does not
// load a config with one: `codex` exits on the error and the desktop app
// opens an error dialog instead of a window. That is how a member ends up
// unable to use Codex at all, for a key that only ever existed to stop it
// compacting against a guessed window.
//
// Inside a table the key is not a root key at all - it reads as a key of
// whatever table came last, and the table it landed in was a [projects.*]
// the member trusted, because that is what a config.toml ends with. One
// carrying our own value under a [projects.*] header is ours, appended by a
// version that wrote it at the end of the file, and it goes.
//
// Under any other header it stays, whatever its value. The member's own
// tables are somewhere our appending bug could never reach, and [profiles.*]
// is now a place a window belongs: this CLI writes one per model, and the
// value it writes there is the same number the coding model declares. Judging
// those by value alone took a window the member had set on a profile of their
// own and moved it to the root, quietly changing what that profile does.
//
// In the root table the first one stands and any repeat after it is the
// duplicate - unless we are taking our own work back out, where the first one
// goes too if it reads as ours. See codexContextWindowsCleared.
//
// Done line by line for the same reason the wire_api repair is: a round trip
// through a TOML library reflows a document the member also edits by hand.
func codexContextWindowsPruned(data []byte, window int) ([]byte, bool) {
	return codexContextWindowsSwept(data, window, true)
}

// The disconnect half. Everything above still holds, and the root table stops
// being off limits: the key we put there went in beside the provider section
// and means nothing without it, so leaving it behind hands back a file
// declaring a window for a model the member no longer has.
//
// Ours is the one whose value is the window we write. A root key that says
// anything else is the member's - withCodexContextWindow steps aside for it
// on the way in, and this steps around it on the way out. The one case both
// halves get wrong is a member who wrote our exact number themselves before
// we ever ran: connecting leaves their key alone and disconnecting takes it
// for ours. Telling those apart needs a mark on the line, which the keys
// already out there do not carry.
func codexContextWindowsCleared(data []byte, window int) ([]byte, bool) {
	return codexContextWindowsSwept(data, window, false)
}

func codexContextWindowsSwept(data []byte, window int, keepRoot bool) ([]byte, bool) {
	ours := strconv.Itoa(window)
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	inTable, inProjects, keptRoot, pruned := false, false, false, false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inTable = true
			inProjects = isCodexProjectsHeader(trimmed)
			out = append(out, line)
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "model_context_window" {
			out = append(out, line)
			continue
		}
		mine := codexWindowIsOurs(value, ours)
		drop := false
		switch {
		case inProjects:
			drop = mine
		// Under any other header it is the member's, and the root cases
		// below are not about it.
		case inTable:
			drop = false
		case !keepRoot && mine:
			drop = true
		case keptRoot:
			drop = true
		default:
			keptRoot = true
		}
		if !drop {
			out = append(out, line)
			continue
		}
		pruned = true
		// The line was written with a blank line on either side of it. Left
		// alone both survive it and the file gains a gap where a key used to
		// be, which is a diff the member did not ask for on a file they read.
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" &&
			i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
			i++
		}
	}
	if !pruned {
		return data, false
	}
	return []byte(strings.Join(out, "\n")), true
}

// Brings our own [model_providers.nan] section up to the only wire protocol
// Codex still speaks, and touches nothing else in the file - not another
// provider's wire_api, not the member's own keys, not a byte of the layout.
//
// The section used to say wire_api = "chat" on purpose: the cluster's
// /responses endpoint answered in one terminal event, so "responses" meant
// the whole reply arriving at once at the end instead of streaming. The
// endpoint streams deltas now, and Codex 0.154 dropped "chat" entirely, so
// the reason for the old value is gone twice over.
//
// Done by hand rather than through a TOML library because a round trip
// through one reflows the whole document - a member's comments, their
// spacing, their quoting - to satisfy a change of one word on one line.
func codexWireAPIRepaired(data []byte) ([]byte, bool) {
	lines := strings.Split(string(data), "\n")
	inNaN, changed := false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inNaN = isCodexNaNProviderHeader(trimmed)
			continue
		}
		if !inNaN {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "wire_api" {
			continue
		}
		if strings.Trim(strings.TrimSpace(value), `"'`) != "chat" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + `wire_api = "responses"`
		changed = true
	}
	if !changed {
		return data, false
	}
	return []byte(strings.Join(lines, "\n")), true
}

// [model_providers.nan] however TOML spells the header: quoted, spaced, with
// a trailing comment.
func isCodexNaNProviderHeader(line string) bool {
	path, ok := codexTablePath(line)
	return ok && path == "model_providers.nan"
}

// The root keys that send Codex to NaN, taken back on a disconnect.
//
// model_provider = "nan" at the root is only ever there because this CLI
// wrote the starter config into an empty file: on a file that already
// existed it adds the provider section and leaves the member's model and
// provider as they were. So there is no earlier choice of theirs to put back,
// and Codex goes back to its own default. Left behind, the key names a
// provider that no longer exists and Codex will not start at all ("Model
// provider `nan` not found").
//
// The model beside it goes too: a model under the nan provider is a NaN
// model, whatever its id. Nothing is touched when the root provider is
// anything else, and nothing under a table header is touched at all - a
// [profiles.*] of the member's naming nan is theirs to keep.
//
// The edit is found line by line and then checked: the result is parsed again
// and has to be the original document minus those two root keys, or nothing
// is changed and false comes back.
func codexNaNRootCleared(content string) (string, bool) {
	doc, ok := codexTOML(content)
	if !ok || doc["model_provider"] != "nan" {
		return content, false
	}
	want, _ := codexTOML(content)
	drop := map[string]bool{"model_provider": true}
	delete(want, "model_provider")
	if _, isString := doc["model"].(string); isString {
		drop["model"] = true
		delete(want, "model")
	}
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i < rootEnd {
			if k, _, ok := codexLine(line); ok && drop[k] {
				continue
			}
		}
		out = append(out, line)
	}
	// What is left of the root may be nothing but the blank line that used
	// to separate it from the first table.
	edited := strings.TrimLeft(strings.Join(out, "\n"), "\n")
	if !codexEditMeans(edited, want) {
		return content, false
	}
	return edited, true
}

// The keys withCodexNaNSessionDefaults put in beside the root provider, taken
// back on a disconnect whatever the root provider is by then.
//
// The catalogue file goes with the disconnect no matter what, so a root
// model_catalog_json naming it is always ours to take: left behind it points
// Codex at a file that is gone. That includes a member who switched the root
// to their own provider after we wrote the starter config - their OpenAI
// sessions would fail on the missing catalogue.
//
// The rest - web_search = "disabled", [agents] enabled = false and
// [features] multi_agent = false - are only taken when something says we
// wrote them: the catalogue line just described, or a root that still names
// the nan provider. Without either they are the member's choices. Taken by
// value, so web_search = "live" or an [agents] table with other keys stands.
//
// A member who wrote one of these exact lines themselves before we ever ran
// and then kept a NaN root cannot be told from us; the same limit as the
// model_context_window we write (see codexContextWindowsCleared).
//
// Found line by line and checked: the result is parsed again and has to be
// the original document minus those keys, or nothing changes.
func codexSessionDefaultsCleared(content, codexHome string) (string, bool) {
	doc, ok := codexTOML(content)
	if !ok {
		return content, false
	}
	catalogPath := codexCatalogPath(codexHome)
	ownCatalog := doc["model_catalog_json"] == catalogPath
	ours := ownCatalog || doc["model_provider"] == "nan"
	if !ours {
		return content, false
	}
	want, _ := codexTOML(content)
	lines := strings.Split(content, "\n")
	if ownCatalog {
		lines = codexRootKeyDropped(lines, want, "model_catalog_json", catalogPath)
	}
	if ours {
		lines = codexRootKeyDropped(lines, want, "web_search", "disabled")
		for _, sw := range []codexSwitch{codexAgentsEnabled, codexFeaturesMultiAgent} {
			lines = codexTableKeyDropped(lines, want, sw.table, sw.key, false)
		}
	}
	edited := strings.Join(lines, "\n")
	if edited == content || !codexEditMeans(edited, want) {
		return content, false
	}
	return edited, true
}

// codexConfigured is whether config.toml is NaN's: the provider section is
// there, or a root provider naming nan with no section behind it - what an
// older CLI left, still ours to take out or to give its section back.
func codexConfigured(path string) bool {
	if mentionsNaN(path) {
		return true
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return false
	}
	doc, ok := codexTOML(string(data))
	return ok && doc["model_provider"] == "nan"
}

// ── Hermes ───────────────────────────────────────────────────────────────────
//
// Every other tool here is configured by writing its file. Hermes is not,
// because its config.yaml is a long commented document the member also edits
// by hand, and because it ships the command to do it: `hermes config set`
// writes a key without flattening the comments around it and rejects a key it
// does not know. Reproducing that schema in Go would buy nothing and would
// start drifting the day Hermes moves a field.
//
// Two things the tool knows and the docs page does not say. The path in
// nan.builders/docs/hermes is the Unix one; on Windows Hermes keeps its home
// under LOCALAPPDATA. And with the `custom` provider Hermes asks the endpoint
// what it serves, so there is no model list to write here and none to keep in
// step with the cluster - only which model to open with.

var (
	errCodexRootNotCleared = errors.New(`sets model_provider = "nan" in a form the NaN CLI ` +
		`cannot take out safely`)
	errCodexSectionNotRemoved = errors.New(`has a [model_providers.nan] section the NaN CLI ` +
		`cannot take out safely`)
)

func removeCodexConfig(cfgPath string) error {
	data, readErr := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	content := string(data)
	// The keys written beside the root provider go first, and whatever the
	// root provider is: see codexSessionDefaultsCleared.
	defaultsCleared, defaultsChanged := codexSessionDefaultsCleared(content, filepath.Dir(cfgPath))
	root, rootCleared := codexNaNRootCleared(defaultsCleared)
	rootCleared = rootCleared || defaultsChanged
	if readErr == nil {
		// A root that still points at nan after the cleanup is one written in
		// a form the line edit cannot take out safely - a multi-line model,
		// say. Taking the provider section anyway would leave exactly the
		// Codex that does not start, so the section, the profiles and the
		// catalogue stay and the member is told what to remove by hand. The
		// key does not stay: it is a live credential this CLI cannot revoke,
		// and a Codex that answers 401 is better than one that leaks it.
		if doc, ok := codexTOML(root); ok && doc["model_provider"] == "nan" {
			return errors.Join(
				fmt.Errorf("%s %w; remove the root model, model_provider, model_catalog_json and web_search lines and the "+
					"[agents] enabled / [features] multi_agent switch by hand", cfgPath, errCodexRootNotCleared),
				codexKeyBlanked(cfgPath, content))
		}
	}
	// Before either way out below, because the profile files are ours whatever
	// state config.toml is in. A member who edited our provider section out by
	// hand, or deleted the file, used to keep all seven of them - each one
	// naming model_provider = "nan", which by then resolves to nothing, so
	// `codex -p nan-qwen36` fails and no tool on the machine admits to having
	// put them there.
	//
	// The error waits until the end. A profile that will not delete - open in
	// an editor, locked by a running Codex - is a cosmetic failure, and
	// returning it here would leave experimental_bearer_token sitting in
	// config.toml after the member asked us to take it out.
	profileErr := removeCodexProfiles(filepath.Dir(cfgPath))
	if readErr != nil {
		return profileErr
	}
	// Nothing of ours in it - no root provider, no provider section - so
	// nothing to rewrite: this is called on every sign-out, against a
	// config.toml that may never have been ours at all.
	lines := strings.Split(root, "\n")
	hasSection := false
	for _, line := range lines {
		if isCodexNaNProviderHeader(line) {
			hasSection = true
			break
		}
	}
	if !rootCleared && !hasSection {
		return profileErr
	}
	var out []string
	inNanSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isCodexNaNProviderHeader(trimmed) {
			inNanSection = true
			continue
		}
		if inNanSection {
			// End of the nan section when a new section header appears.
			if codexIsHeader(trimmed) {
				inNanSection = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	// A file Codex can read is checked: what is left has to be the document
	// minus the provider table. One it cannot read (invalid TOML, which Codex
	// refuses already) is edited as before, because there is nothing to
	// check it against and the key still has to leave.
	if _, parses := codexTOML(root); parses && !codexProviderRemovalMeans(root, result) {
		return errors.Join(profileErr,
			fmt.Errorf("%s %w; remove it by hand", cfgPath, errCodexSectionNotRemoved),
			codexKeyBlanked(cfgPath, root))
	}
	// The section is gone, and so is the reason for the model_context_window
	// that went in beside it. Left behind it is a key the member never wrote,
	// naming a window no model of theirs has - and if there are two of them
	// it is a file their Codex does not open, now with nothing left in it to
	// say who did that or which tool to disconnect to undo it. Kept only
	// when a re-parse shows it changed nothing but window keys.
	codexModel, _ := catalog.Get(catalog.Coding)
	if cleared, changed := codexContextWindowsCleared([]byte(result), codexModel.Context); changed {
		if _, parses := codexTOML(result); !parses || codexOnlyWindowsDiffer(result, string(cleared)) {
			result = string(cleared)
		}
	}
	// Whatever went from the top - the root keys, the window - may have left
	// the blank line that separated them from the first table.
	if rootCleared {
		result = strings.TrimLeft(result, "\n")
	}
	if strings.TrimSpace(result) == "" {
		if err := os.Remove(cfgPath); err != nil {
			return err
		}
		return profileErr
	}
	if err := writeConfigFile(cfgPath, []byte(result)); err != nil {
		return err
	}
	return profileErr
}

// Empties experimental_bearer_token in [model_providers.nan] when the file is
// left otherwise as it is, verified by re-parse. Nothing happens when there is
// no such key, or the edit would change anything else.
func codexKeyBlanked(cfgPath, content string) error {
	doc, ok := codexTOML(content)
	if !ok {
		return nil
	}
	providers, _ := doc["model_providers"].(map[string]any)
	nan, _ := providers["nan"].(map[string]any)
	token, _ := nan[codexTokenKey].(string)
	if token == "" {
		return nil
	}
	edited := strings.Join(codexTableKeyBlanked(strings.Split(content, "\n"), "model_providers.nan", codexTokenKey, token), "\n")
	want, _ := codexTOML(content)
	want["model_providers"].(map[string]any)["nan"].(map[string]any)[codexTokenKey] = ""
	if !codexEditMeans(edited, want) {
		return fmt.Errorf("%s: the API key could not be taken out of config.toml; remove %s by hand", cfgPath, codexTokenKey)
	}
	return writeConfigFile(cfgPath, []byte(edited))
}

const codexTokenKey = "experimental_bearer_token"

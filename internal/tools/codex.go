package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

func writeCodexConfig(cfgPath, apiKey string) error {
	data, _ := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	codexModel, _ := catalog.Get(catalog.Coding)

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
		repaired, pruned := codexContextWindowsPruned(data, codexModel.Context)
		if pruned {
			repaired = []byte(withCodexContextWindow(string(repaired), codexModel.Context))
		}
		rewired, changed := codexWireAPIRepaired(repaired)
		if pruned || changed {
			if err := writeConfigFile(cfgPath, rewired); err != nil {
				return err
			}
		}
		return writeCodexProfiles(filepath.Dir(cfgPath))
	}

	// If no existing config, write a complete starter config.
	if len(data) == 0 {
		content := fmt.Sprintf(`model = %q
model_provider = "nan"
model_context_window = %d

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "responses"
`, codexModel.ID, codexModel.Context, apiKey)
		if err := writeConfigFile(cfgPath, []byte(content)); err != nil {
			return err
		}
		return writeCodexProfiles(filepath.Dir(cfgPath))
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
	if err := writeConfigFile(cfgPath, []byte(withCodexContextWindow(content, codexModel.Context))); err != nil {
		return err
	}
	return writeCodexProfiles(filepath.Dir(cfgPath))
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
// They also switch off the two tools Codex adds on its own. Codex 0.159
// sends the sub-agent tools as a "namespace" tool and web search as a
// "web_search" one, and with either in the request the cluster's /responses
// stops streaming items for qwen3.8-flash and mimo: all that comes back is
// response.completed, which Codex does not read an answer from, so the turn
// ends with no text and no tool call. Neither tool has anywhere to go on the
// cluster anyway. Here and not in config.toml, because these files are ours
// and leave with the disconnect - a web_search = "disabled" at the root
// would stay behind and take search from the member's OpenAI sessions too.
func writeCodexProfiles(codexHome string) error {
	for _, m := range catalog.ChatModels() {
		body := fmt.Sprintf(`# Written by the NaN CLI. Disconnecting Codex removes it.
model = %q
model_provider = "nan"
model_context_window = %d
web_search = "disabled"

[features]
multi_agent = false
`, m.ID, m.Context)
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

// Takes back only the files this CLI wrote, by the prefix it wrote them under.
func removeCodexProfiles(codexHome string) error {
	entries, err := os.ReadDir(codexHome)
	if err != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, "nan-") && strings.HasSuffix(name, ".config.toml") {
			if err := os.Remove(filepath.Join(codexHome, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
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
	declared := fmt.Sprintf("model_context_window = %d", window)
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
		mine := strings.TrimSpace(value) == ours
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

// [model_providers.nan], and the quoted spelling of it that TOML also allows.
func isCodexNaNProviderHeader(line string) bool {
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
	if !strings.HasPrefix(name, "model_providers.") {
		return false
	}
	return strings.Trim(strings.TrimPrefix(name, "model_providers."), `"'`) == "nan"
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

func removeCodexConfig(cfgPath string) error {
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

	data, err := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return profileErr
	}
	// Nothing of ours in it, so nothing to rewrite: this is called on every
	// sign-out, against a config.toml that may never have been ours at all.
	if !strings.Contains(string(data), "[model_providers.nan]") {
		return profileErr
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	inNanSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[model_providers.nan]" {
			inNanSection = true
			continue
		}
		if inNanSection {
			// End of the nan section when a new section header appears.
			if strings.HasPrefix(trimmed, "[") {
				inNanSection = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	// The section is gone, and so is the reason for the model_context_window
	// that went in beside it. Left behind it is a key the member never wrote,
	// naming a window no model of theirs has - and if there are two of them
	// it is a file their Codex does not open, now with nothing left in it to
	// say who did that or which tool to disconnect to undo it.
	codexModel, _ := catalog.Get(catalog.Coding)
	if cleared, changed := codexContextWindowsCleared([]byte(result), codexModel.Context); changed {
		result = string(cleared)
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

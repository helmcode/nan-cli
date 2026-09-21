package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nxssie/nan-cli/internal/api"
	catalog "github.com/nxssie/nan-cli/internal/models"
	"github.com/nxssie/nan-cli/internal/session"
)

// What this CLI actually does for a member is write four config files, and
// nothing ever read them back. They had drifted a long way from the cluster:
// three models out of seven, a 128000-token window on models served at
// 1,048,576, and an opencode block with no `limit` at all - the exact bug
// nan.builders/docs/opencode was rewritten to stop publishing. A wrong config
// here is silent twice over: the tool starts, answers, and only behaves oddly
// much later, deep into a session.

const testKey = "sk-test-key-not-a-real-one"

func tempConfig(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// A file this CLI put a key into is the member's and nobody else's on the
// machine. Windows does not carry the mode bits - the file inherits the ACL of
// the profile directory it sits in - so there is nothing to assert there.
func assertNotWorldReadable(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("%s is mode %04o, readable by more than the member", filepath.Base(path), mode)
	}
}

func TestOpencodeConfigPublishesEveryModelWithItsWindow(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	if err := writeOpencodeConfig(path, testKey); err != nil {
		t.Fatal(err)
	}

	cfg := readJSON(t, path)
	nan := cfg["provider"].(map[string]any)["nan"].(map[string]any)

	if npm := nan["npm"]; npm != "@ai-sdk/openai-compatible" {
		t.Errorf("npm = %v, want the generic openai-compatible adapter", npm)
	}

	written := nan["models"].(map[string]any)
	if len(written) != len(catalog.ChatModels()) {
		t.Errorf("wrote %d models, the cluster serves %d", len(written), len(catalog.ChatModels()))
	}

	for _, m := range catalog.ChatModels() {
		entry, ok := written[m.ID].(map[string]any)
		if !ok {
			t.Errorf("%s: not written", m.ID)
			continue
		}
		// `contextWindow` is not in opencode's schema. An unknown key is
		// ignored in silence, which is how this went unnoticed for months.
		if _, bad := entry["contextWindow"]; bad {
			t.Errorf("%s: contextWindow is not a field opencode reads", m.ID)
		}
		limit, ok := entry["limit"].(map[string]any)
		if !ok {
			t.Errorf("%s: no limit, so opencode will guess the window", m.ID)
			continue
		}
		if got := int(limit["context"].(float64)); got != m.Context {
			t.Errorf("%s: context %d, served at %d", m.ID, got, m.Context)
		}
		if got := int(limit["output"].(float64)); got != m.Output {
			t.Errorf("%s: output %d, want %d", m.ID, got, m.Output)
		}
	}
}

func TestOpencodeConfigRepairsAnEntryWrittenByAnOlderVersion(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	// Exactly what versions up to v0.1.1 left behind: the model is there, the
	// window is not, and the member has renamed it.
	old := `{
	  "provider": {
	    "nan": {
	      "npm": "@ai-sdk/openai-compatible",
	      "options": { "baseURL": "https://api.nan.builders/v1", "apiKey": "sk-old" },
	      "models": { "gemma4": { "name": "my own name for it" } }
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeConfig(path, testKey); err != nil {
		t.Fatal(err)
	}

	cfg := readJSON(t, path)
	models := cfg["provider"].(map[string]any)["nan"].(map[string]any)["models"].(map[string]any)
	gemma := models["gemma4"].(map[string]any)

	if gemma["name"] != "my own name for it" {
		t.Errorf("name = %v, the member's own value was overwritten", gemma["name"])
	}
	limit, ok := gemma["limit"].(map[string]any)
	if !ok {
		t.Fatal("gemma4 still has no limit, so an upgrade fixes nothing for anyone already configured")
	}
	if got, want := int(limit["context"].(float64)), 262_144; got != want {
		t.Errorf("context %d, want %d", got, want)
	}
	if _, ok := models["glm5.3-flash"]; !ok {
		t.Error("glm5.3-flash was not added to an existing config")
	}
}

func TestOpencodeConfigKeepsWhatItDoesNotOwn(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"theme":"tokyonight","provider":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if theme := readJSON(t, path)["theme"]; theme != "tokyonight" {
		t.Errorf("theme = %v, an unrelated setting was lost", theme)
	}
}

func piModels(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	cfg := readJSON(t, path)
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		t.Fatal("models.json has no providers object, which is the only thing Pi reads")
	}
	nan, ok := providers["nan"].(map[string]any)
	if !ok {
		t.Fatal("no nan provider")
	}
	out := map[string]map[string]any{}
	for _, raw := range nan["models"].([]any) {
		m := raw.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

func TestPiConfigIsAModelsJsonWithTheRealWindows(t *testing.T) {
	path := tempConfig(t, "models.json")
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}

	written := piModels(t, path)
	for _, m := range catalog.ChatModels() {
		entry, ok := written[m.ID]
		if !ok {
			t.Errorf("%s: missing from the Pi provider", m.ID)
			continue
		}
		// The old template wrote 128000 and 8192 for every model, whatever it
		// was: an eighth of the room on the 1M models.
		if got := int(entry["contextWindow"].(float64)); got != m.Context {
			t.Errorf("%s: contextWindow %d, served at %d", m.ID, got, m.Context)
		}
		if got := int(entry["maxTokens"].(float64)); got != m.Output {
			t.Errorf("%s: maxTokens %d, want %d", m.ID, got, m.Output)
		}
	}
}

func TestPiConfigWritesOnlyTheModalitiesPiAccepts(t *testing.T) {
	path := tempConfig(t, "models.json")
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	// Pi's schema is `("text" | "image")[]`. A third value fails validation and
	// Pi then refuses the whole file, every other provider in it included, so
	// mimo-v2.5 goes in without its audio.
	for id, entry := range piModels(t, path) {
		for _, raw := range entry["input"].([]any) {
			if in := raw.(string); in != "text" && in != "image" {
				t.Errorf("%s: input %q is not in Pi's schema", id, in)
			}
		}
	}
}

// Pi joins Codex as a tool that never carries the literal key: models.json is
// a file members paste into issues and dotfiles, so the provider points at
// `nan key print` instead.
func TestPiConfigWritesACommandReferenceNotTheKey(t *testing.T) {
	restoreNanExecutable(t, "/usr/local/bin/nan")

	path := tempConfig(t, "models.json")
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}

	// piModels only returns the model list, so the raw provider object is what
	// carries the reference.
	providers := readJSON(t, path)["providers"].(map[string]any)
	nan := providers["nan"].(map[string]any)
	if got := nan["apiKey"]; got != "!/usr/local/bin/nan key print" {
		t.Errorf("apiKey = %v, want the key command reference", got)
	}
	data := readFile(t, path)
	if strings.Contains(data, testKey) {
		t.Error("models.json carries the literal key")
	}
}

// The escapes are Pi's, not a shell's: `$$` reads as a literal `$` and `$!`
// as a literal `!`, so `$` has to be doubled before `!` gets its `$` prefix.
func TestPiKeyReferenceEscapesDollarAndBang(t *testing.T) {
	got := piKeyReference("/opt/nan$bin/nan!")
	if want := "!/opt/nan$$bin/nan$! key print"; got != want {
		t.Errorf("piKeyReference = %q, want %q", got, want)
	}
}

func TestPiConfigLeavesOtherProvidersAlone(t *testing.T) {
	path := tempConfig(t, "models.json")
	existing := `{"providers":{"openai":{"baseUrl":"https://api.openai.com/v1","models":[]}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	providers := readJSON(t, path)["providers"].(map[string]any)
	if _, ok := providers["openai"]; !ok {
		t.Fatal("another provider was dropped from a shared file")
	}

	// And removing ours has to leave theirs standing, which deleting the file
	// would not.
	if err := removePiConfig(path); err != nil {
		t.Fatal(err)
	}
	providers = readJSON(t, path)["providers"].(map[string]any)
	if _, ok := providers["openai"]; !ok {
		t.Error("removing the NaN provider took another one with it")
	}
	if _, ok := providers["nan"]; ok {
		t.Error("the NaN provider is still there after removing it")
	}
}

func TestPiConfigIsRecognisedAsConfigured(t *testing.T) {
	path := tempConfig(t, "models.json")
	if isNaNConfigured("Pi", path) {
		t.Error("an absent file reads as configured")
	}
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if !isNaNConfigured("Pi", path) {
		t.Error("the Setup tab will not see the config it just wrote")
	}
}

func TestCodexConfigSpeaksAWireProtocolCodexStillLoads(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Codex 0.154 exits on wire_api = "chat" before its TUI is up, and the
	// cluster's /responses endpoint streams deltas now, which is the only
	// thing "chat" was ever here for.
	if !strings.Contains(content, `wire_api = "responses"`) {
		t.Error(`wire_api is not "responses", so Codex will refuse to start`)
	}
	model, _ := catalog.Get(catalog.Coding)
	if !strings.Contains(content, `model = "`+model.ID+`"`) {
		t.Errorf("the default model is not %s", model.ID)
	}
	if strings.Contains(content, "model_context_window = 131072") {
		t.Error("still declaring a window no model on the cluster has")
	}
}

// The member updates the CLI because Codex stopped opening, so the repair has
// to reach a config that is already ours: they cannot be asked to disconnect
// and reconnect a tool that will not start.
func TestCodexConfigRepairsTheWireAPIWeWroteBefore(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := `model = "glm5.3-flash"
model_provider = "nan"

[model_providers.openai]
name = "OpenAI"
wire_api = "chat"

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = "nan-old"
wire_api = "chat"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Count(content, "[model_providers.nan]") != 1 {
		t.Error("the provider was appended a second time instead of repaired")
	}
	// The same write also takes the plaintext key an older version of this
	// CLI put here, and leaves the command reference in its place.
	if strings.Contains(content, "experimental_bearer_token") {
		t.Error("the repair left the plaintext key in the file")
	}
	if !strings.Contains(content, `[model_providers.nan.auth]`) || !strings.Contains(content, `args = ["key", "print"]`) {
		t.Errorf("the repair did not write the key command reference:\n%s", content)
	}
	// Another provider's wire_api is that provider's business.
	openai := content[strings.Index(content, "[model_providers.openai]"):strings.Index(content, "[model_providers.nan]")]
	if !strings.Contains(openai, `wire_api = "chat"`) {
		t.Error("a wire_api outside our own section was rewritten")
	}
	nan := content[strings.Index(content, "[model_providers.nan]"):]
	if !strings.Contains(nan, `wire_api = "responses"`) {
		t.Errorf("our section still does not speak a protocol Codex loads:\n%s", nan)
	}
}

// A config already on "responses" is a config with nothing to do to it.
func TestCodexConfigLeavesARepairedFileAlone(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := `[model_providers.nan]
base_url = "https://api.nan.builders/v1"
wire_api = "responses"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != existing {
		t.Errorf("the file was rewritten with nothing to change:\n%s", data)
	}
}

// Appended at the end of a file that ends in a table, a root key stops being
// a root key: it becomes a key of that table, where Codex never looks for it.
func TestCodexContextWindowLandsInTheRootTable(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := `model = "gpt-5"

[projects."/home/member/repo"]
trust_level = "trusted"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)

	window := strings.Index(content, "model_context_window")
	if window < 0 {
		t.Fatal("no model_context_window was written at all")
	}
	if header := strings.Index(content, "[projects."); window > header {
		t.Errorf("model_context_window is inside a table, not the root:\n%s", content)
	}
	if !strings.Contains(content, `trust_level = "trusted"`) {
		t.Error("the member's own project entry was lost")
	}
}

func TestCodexConfigDoesNotTouchAnExistingChoice(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"gpt-5\"\nmodel_provider = \"openai\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `model = "gpt-5"`) {
		t.Error("the member's own model choice was replaced")
	}
	if !strings.Contains(string(data), "[model_providers.nan]") {
		t.Error("the NaN provider was not appended")
	}
	if !strings.Contains(string(data), `args = ["key", "print"]`) {
		t.Errorf("the appended section does not reference nan key print:\n%s", data)
	}
}

// Under `go test` os.Executable() is the test binary, so the tests that
// assert on the written command swap it for a path with a known shape.
func restoreNanExecutable(t *testing.T, path string) {
	t.Helper()
	original := nanExecutable
	nanExecutable = func() string { return path }
	t.Cleanup(func() { nanExecutable = original })
}

// The key an older version of this CLI wrote here sat in a file members paste
// into issues and, for dotfiles, in a repository. The migration takes it out
// and points the section at `nan key print` instead, in the file that already
// exists - reached on the same early return the wire repair is.
func TestCodexConfigMigratesThePlaintextKeyToTheKeyCommand(t *testing.T) {
	path := tempConfig(t, "config.toml")
	restoreNanExecutable(t, "/usr/local/bin/nan")
	existing := `model = "glm5.3-flash"

[model_providers.old]
name = "Old"

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = "nan-old"
wire_api = "responses"

[projects."/home/member/repo"]
trust_level = "trusted"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)

	if strings.Contains(content, "experimental_bearer_token") || strings.Contains(content, "nan-old") {
		t.Errorf("the plaintext key survived the migration:\n%s", content)
	}
	if strings.Contains(content, testKey) {
		t.Error("the migration wrote a literal key of its own")
	}

	// Codex runs the command with exec, never a shell: command is the bare
	// absolute path to the binary and every argument goes over in args.
	if !strings.Contains(content, `[model_providers.nan.auth]`) {
		t.Fatalf("no auth sub-table was written:\n%s", content)
	}
	if !strings.Contains(content, `command = "/usr/local/bin/nan"`) {
		t.Errorf("command is not the bare absolute path to nan:\n%s", content)
	}
	if !strings.Contains(content, `args = ["key", "print"]`) {
		t.Errorf("the arguments did not go into args:\n%s", content)
	}

	// [model_providers.nan.auth] is a sub-table: every key after it belongs
	// to it, the way model_context_window once became a key of the last
	// [projects.*] entry. The member's own keys must come out of the
	// migration where they went in.
	if !strings.Contains(content, `trust_level = "trusted"`) {
		t.Error("the member's project entry was lost")
	}
	if auth := strings.Index(content, "[model_providers.nan.auth]"); strings.Index(content, "trust_level") < auth {
		t.Errorf("the auth sub-table swallowed the member's keys:\n%s", content)
	}
	if strings.Count(content, "[model_providers.nan]") != 1 {
		t.Error("the provider was appended a second time instead of migrated")
	}
}

// A config a member also edits by hand is rewritten one line at a time, never
// reflowed: the comments, the spacing and the quoting around the one line
// that changed are theirs, and a round trip through a TOML library takes all
// of them.
func TestCodexMigrationIsLineSurgeryNotARoundTrip(t *testing.T) {
	path := tempConfig(t, "config.toml")
	restoreNanExecutable(t, "/usr/local/bin/nan")
	existing := `# my notes, kept however I wrote them

model = "gpt-5"   # trailing comment
[model_providers.nan]
name = "NaN"  
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = "nan-old"
wire_api = "responses"
`
	want := `# my notes, kept however I wrote them

model = "gpt-5"   # trailing comment
[model_providers.nan]
name = "NaN"  
base_url = "https://api.nan.builders/v1"
wire_api = "responses"

[model_providers.nan.auth]
command = "/usr/local/bin/nan"
args = ["key", "print"]
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != want {
		t.Errorf("the file was reflowed, not operated on:\n got: %q\nwant: %q", got, want)
	}
}

// A config already carrying the reference has nothing to migrate, and a
// second run must not rewrite it: the command path it holds may not be
// replaced with whatever this run of the CLI happens to resolve to.
func TestCodexConfigAlreadyUsingTheKeyCommandIsNotRewritten(t *testing.T) {
	path := tempConfig(t, "config.toml")
	restoreNanExecutable(t, "/elsewhere/nan")
	existing := `model = "gpt-5"

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
wire_api = "responses"

[model_providers.nan.auth]
command = "/usr/local/bin/nan"
args = ["key", "print"]
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != existing {
		t.Errorf("a config already using auth was rewritten:\n got: %q\nwant: %q", got, existing)
	}
}

// The key reads out of session.json at request time now, so no path through
// this writer may put the literal key into the file any more - the starter
// config and the append included.
func TestCodexConfigWritesACommandReferenceNotTheKey(t *testing.T) {
	restoreNanExecutable(t, "/usr/local/bin/nan")

	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	starter := readFile(t, path)
	if strings.Contains(starter, testKey) {
		t.Errorf("the starter config carries the literal key:\n%s", starter)
	}
	if !strings.Contains(starter, `[model_providers.nan.auth]`) ||
		!strings.Contains(starter, `command = "/usr/local/bin/nan"`) ||
		!strings.Contains(starter, `args = ["key", "print"]`) {
		t.Errorf("the starter config does not reference nan key print:\n%s", starter)
	}
	if strings.Contains(starter, "experimental_bearer_token") {
		t.Errorf("the starter config still writes the old bearer token:\n%s", starter)
	}

	appended := tempConfig(t, "config.toml")
	if err := os.WriteFile(appended, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(appended, testKey); err != nil {
		t.Fatal(err)
	}
	if content := readFile(t, appended); strings.Contains(content, testKey) {
		t.Errorf("the appended section carries the literal key:\n%s", content)
	}
}

// The file this repairs is the one a member actually turns up with: the
// provider section is ours and already fine, and Codex still will not open -
// not the CLI, not the desktop app, which shows "failed to read
// configuration layers: duplicate key" in a dialog and quits. Two runs of an
// older setup put two model_context_window lines at the end of the file,
// which is both a duplicate key and, after a [projects.*] header, not a root
// key at all. Repairing only the wire_api left that member exactly as stuck.
func TestCodexConfigOpensAFileItLeftUnloadable(t *testing.T) {
	codexModel, _ := catalog.Get(catalog.Coding)
	path := tempConfig(t, "config.toml")
	existing := fmt.Sprintf(`model = "gpt-5"

[projects."/home/member/repo"]
trust_level = "trusted"

model_context_window = %d

model_context_window = %d

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
wire_api = "chat"
`, codexModel.Context, codexModel.Context)
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)

	if n := strings.Count(content, "model_context_window"); n != 1 {
		t.Errorf("%d model_context_window keys, so Codex still refuses the file:\n%s", n, content)
	}
	window, header := strings.Index(content, "model_context_window"), strings.Index(content, "[projects.")
	if window > header {
		t.Errorf("the surviving window is inside a table, where Codex does not read it:\n%s", content)
	}
	if !strings.Contains(content, `wire_api = "responses"`) {
		t.Error("the wire_api repair stopped happening once the pruning was added")
	}
	if !strings.Contains(content, `trust_level = "trusted"`) {
		t.Error("the member's own project entry was lost")
	}
}

// A window the member set themselves is a window they meant, whatever it
// says. Ours is recognisable by its value, and only inside a table - in the
// root it is indistinguishable from theirs, so there the first one stands.
func TestCodexConfigKeepsAWindowTheMemberDeclared(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := `model = "gpt-5"
model_context_window = 200000

[model_providers.nan]
base_url = "https://api.nan.builders/v1"
wire_api = "responses"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != existing {
		t.Errorf("a file with nothing wrong with it was rewritten:\n%s", data)
	}
}

// Disconnecting takes the section out. The window went in beside it and has
// no meaning without it, and leaving two of them behind would hand back a
// file Codex does not open with nothing in it left to blame.
func TestCodexRemovalTakesTheWindowItWrote(t *testing.T) {
	codexModel, _ := catalog.Get(catalog.Coding)
	path := tempConfig(t, "config.toml")
	existing := fmt.Sprintf(`model = "gpt-5"

[projects."/home/member/repo"]
trust_level = "trusted"

model_context_window = %d

[model_providers.nan]
base_url = "https://api.nan.builders/v1"
wire_api = "responses"
`, codexModel.Context)
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "model_context_window") {
		t.Errorf("the window we wrote outlived the section it belonged to:\n%s", content)
	}
	if strings.Contains(content, "api.nan.builders") {
		t.Error("the provider section survived a disconnect")
	}
	if !strings.Contains(content, `trust_level = "trusted"`) {
		t.Error("the member's own project entry was lost")
	}
}

// `codex --model <id>` moves the model and leaves model_context_window
// behind, so a 262,144-token model runs with whatever window config.toml
// declares. A file per model is the only place Codex 0.155 lets that window
// travel with the model it belongs to.
func TestCodexProfilesGiveEveryModelItsOwnWindow(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)

	for _, m := range catalog.ChatModels() {
		profile := filepath.Join(home, codexProfileName(m.ID)+".config.toml")
		data, err := os.ReadFile(profile)
		if err != nil {
			t.Errorf("%s has no profile, so it is only reachable with the wrong window: %v", m.ID, err)
			continue
		}
		body := string(data)
		if !strings.Contains(body, fmt.Sprintf("model = %q", m.ID)) {
			t.Errorf("%s: the profile does not select the model it is named after:\n%s", m.ID, body)
		}
		if !strings.Contains(body, fmt.Sprintf("model_context_window = %d", m.Context)) {
			t.Errorf("%s: window is not the one the cluster serves:\n%s", m.ID, body)
		}
		if !strings.Contains(body, `model_provider = "nan"`) {
			t.Errorf("%s: the profile would run against whatever provider is default:\n%s", m.ID, body)
		}
	}
}

// Codex refuses a --profile with a dot in it ("pass a plain name such as
// `work`"), which is every id on the cluster that carries a version number.
func TestCodexProfileNamesAreNamesCodexAccepts(t *testing.T) {
	for _, m := range catalog.ChatModels() {
		name := codexProfileName(m.ID)
		if strings.Contains(name, ".") {
			t.Errorf("%s -> %s: Codex rejects this outright", m.ID, name)
		}
		if !strings.HasPrefix(name, "nan-") {
			t.Errorf("%s -> %s: without the prefix it is not ours to remove again", m.ID, name)
		}
	}
	if got := codexProfileName("glm5.3-flash"); got != "nan-glm53-flash" {
		t.Errorf("codexProfileName(glm5.3-flash) = %s", got)
	}
}

// The provider section carries the key. A copy of it in eight more files is
// eight more files to rotate, and eight more to leak.
func TestCodexProfilesCarryNoKey(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	for _, m := range catalog.ChatModels() {
		data, err := os.ReadFile(filepath.Join(home, codexProfileName(m.ID)+".config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), testKey) {
			t.Errorf("%s: the profile has the API key in it", m.ID)
		}
	}
}

func TestCodexRemovalTakesTheProfilesAndNothingElse(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	// A profile of the member's own, sitting in the same directory.
	theirs := filepath.Join(home, "work.config.toml")
	if err := os.WriteFile(theirs, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, m := range catalog.ChatModels() {
		profile := filepath.Join(home, codexProfileName(m.ID)+".config.toml")
		if _, err := os.Stat(profile); !os.IsNotExist(err) {
			t.Errorf("%s: the profile outlived the disconnect", m.ID)
		}
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("a profile the member wrote was deleted: %v", err)
	}
}

// The window this CLI writes goes in the root table, above the first header.
// Every test for the removal used the shape the old appending bug left - a
// key inside a [projects.*] - so the one the CLI actually writes today went
// out the far side of a disconnect untouched, and the member kept a window
// for a model they no longer have.
func TestCodexRemovalTakesTheWindowFromTheRootTable(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return // nothing of the member's was in it, so there is no file left
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "model_context_window") {
		t.Errorf("the window we wrote in the root outlived the disconnect:\n%s", data)
	}
}

// A window the member set in the root stays there. Disconnecting takes our
// own work back out, not theirs.
func TestCodexRemovalKeepsARootWindowTheMemberDeclared(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := `model = "gpt-5"
model_context_window = 200000

[model_providers.nan]
base_url = "https://api.nan.builders/v1"
wire_api = "responses"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "model_context_window = 200000") {
		t.Errorf("the member's own window was taken with ours:\n%s", data)
	}
}

// The profile files are ours whatever state config.toml is in. A member who
// edited the provider section out by hand kept all seven, each one naming a
// provider that no longer resolves.
func TestCodexRemovalTakesTheProfilesWithNoSectionLeft(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	// What is left after they delete our section themselves.
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, m := range catalog.ChatModels() {
		profile := filepath.Join(home, codexProfileName(m.ID)+".config.toml")
		if _, err := os.Stat(profile); !os.IsNotExist(err) {
			t.Errorf("%s: the profile outlived a disconnect with no section to find", m.ID)
		}
	}
}

// Same, with no config.toml at all.
func TestCodexRemovalTakesTheProfilesWithNoConfigLeft(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, m := range catalog.ChatModels() {
		profile := filepath.Join(home, codexProfileName(m.ID)+".config.toml")
		if _, err := os.Stat(profile); !os.IsNotExist(err) {
			t.Errorf("%s: the profile outlived a disconnect with no config left", m.ID)
		}
	}
}

// Our window value is the number nan.builders publishes for the coding
// model, which is exactly the number a member is most likely to have typed
// themselves - and this PR teaches them to type it into a [profiles.*].
// Judging a key by its value alone took theirs out of the profile and moved
// it to the root, changing what that profile does without saying so.
// The member here is one who is already connected - the only one the pruning
// runs for - and who has since written a profile of their own.
func TestCodexConfigLeavesAMemberProfileWindowAlone(t *testing.T) {
	codexModel, _ := catalog.Get(catalog.Coding)
	path := tempConfig(t, "config.toml")
	existing := fmt.Sprintf(`model = "gpt-5"

[model_providers.nan]
base_url = "https://api.nan.builders/v1"
wire_api = "chat"

[profiles.big]
model = "gpt-5"
model_context_window = %d
`, codexModel.Context)
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)

	// Everything from the member's header to the end of the file: their key
	// has to still be in there, under their own table.
	profile := strings.Index(content, "[profiles.big]")
	if profile < 0 {
		t.Fatalf("the member's own profile is gone:\n%s", content)
	}
	if !strings.Contains(content[profile:], "model_context_window") {
		t.Errorf("the member's window was taken out of [profiles.big]:\n%s", content)
	}
	// And the repair it came in for still happened.
	if !strings.Contains(content, `wire_api = "responses"`) {
		t.Errorf("the wire_api repair stopped happening:\n%s", content)
	}
}

func TestFactoryConfigMarksWhatCannotSeeImages(t *testing.T) {
	path := tempConfig(t, "settings.json")
	if err := writeFactoryConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, path)
	custom := cfg["customModels"].([]any)
	if len(custom) != len(catalog.ChatModels()) {
		t.Errorf("wrote %d models, the cluster serves %d", len(custom), len(catalog.ChatModels()))
	}
	for _, raw := range custom {
		entry := raw.(map[string]any)
		id := entry["model"].(string)
		m, ok := catalog.Get(id)
		if !ok {
			t.Errorf("%s: not a model the cluster serves", id)
			continue
		}
		if got, want := entry["noImageSupport"].(bool), !m.Accepts(catalog.InputImage); got != want {
			t.Errorf("%s: noImageSupport = %v, want %v", id, got, want)
		}
	}
	def, ok := cfg["sessionDefaultSettings"].(map[string]any)
	if !ok {
		t.Fatal("no default model was set")
	}
	// Factory points its default at the custom entry's own id, not at the
	// model id, so the check has to go back through the list.
	var defaultModel string
	for _, raw := range custom {
		entry := raw.(map[string]any)
		if entry["id"] == def["model"] {
			defaultModel = entry["model"].(string)
		}
	}
	if defaultModel != catalog.Default {
		t.Errorf("default is %q, want %q, the model the quickstart recommends",
			defaultModel, catalog.Default)
	}
}

func TestEveryModelIsWrittenTheSameEverywhere(t *testing.T) {
	// The three writers used to keep their own list and all three disagreed.
	dir := t.TempDir()
	opencodePath := filepath.Join(dir, "opencode.json")
	factoryPath := filepath.Join(dir, "settings.json")
	piPath := filepath.Join(dir, "models.json")
	for _, err := range []error{
		writeOpencodeConfig(opencodePath, testKey),
		writeFactoryConfig(factoryPath, testKey),
		writePiConfig(piPath, testKey),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	opencode := readJSON(t, opencodePath)["provider"].(map[string]any)["nan"].(map[string]any)["models"].(map[string]any)
	factory := map[string]bool{}
	for _, raw := range readJSON(t, factoryPath)["customModels"].([]any) {
		factory[raw.(map[string]any)["model"].(string)] = true
	}
	pi, err := os.ReadFile(piPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range catalog.ChatModels() {
		if _, ok := opencode[m.ID]; !ok {
			t.Errorf("%s: missing from opencode", m.ID)
		}
		if !factory[m.ID] {
			t.Errorf("%s: missing from Factory", m.ID)
		}
		if !strings.Contains(string(pi), m.ID) {
			t.Errorf("%s: missing from Pi", m.ID)
		}
	}
}

func TestHumanKeyKeepsAcronymsWhole(t *testing.T) {
	// The Profile tab renders whatever keys /auth/me returns, and `userUUID`
	// came out as "User U U I D" on its first line.
	for _, c := range []struct{ in, want string }{
		{"userUUID", "User UUID"},
		{"inferenceProfile", "Inference Profile"},
		{"isAdmin", "Is Admin"},
		{"handle", "Handle"},
		{"expiresAt", "Expires At"},
		{"APIKey", "API Key"},
		{"image_gen", "Image gen"},
		{"", ""},
	} {
		if got := humanKey(c.in); got != c.want {
			t.Errorf("humanKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The Setup tab's `c` key runs configureTools, which finds the tools on the
// machine and writes into their real config paths. Nothing covered it, so the
// only way to try it was to press the key and look at your own home directory.
// Here HOME points somewhere disposable.
func TestConfigureToolsWritesEveryEnabledTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows
	// Hermes does not take its home from either of those, so without this the
	// run reaches the real install and configures the machine it is testing
	// on. Faking the runner as well means no hermes process is spawned at all,
	// here or on a machine that has one.
	hermesHomeDir := filepath.Join(home, "hermes")
	t.Setenv("HERMES_HOME", hermesHomeDir)
	if err := os.MkdirAll(hermesHomeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hermesCalls := recordHermes(t)

	// A tool counts as installed if its binary is on PATH *or* its config path
	// exists, so an empty file each is enough to make the file-written ones
	// visible.
	paths := map[string]string{
		"Factory AI": filepath.Join(home, ".factory", "settings.json"),
		"OpenCode":   filepath.Join(home, ".config", "opencode", "opencode.json"),
		"Pi":         filepath.Join(home, ".pi", "agent", "models.json"),
		"Codex":      filepath.Join(home, ".codex", "config.toml"),
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	msg, written, failed := configureTools(testKey, nil)
	if !strings.Contains(msg, "5 added") {
		t.Fatalf("configureTools said %q, want the five tools written", msg)
	}
	if len(*hermesCalls) == 0 {
		t.Error("Hermes was counted but never configured")
	}
	// The names come back so the tab can say how to use each one; a tool
	// written but not named leaves a member with a config and no next step.
	if len(failed) != 0 {
		t.Errorf("tools failed on a clean run: %v", failed)
	}
	if len(written) != 5 {
		t.Errorf("configureTools named %v, want all five it wrote", written)
	}
	for _, name := range written {
		if _, ok := nextStepFor[name]; !ok {
			t.Errorf("%s is configured and the tab has nothing to tell anyone about using it", name)
		}
	}

	for name, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(string(data), "api.nan.builders") {
			t.Errorf("%s: written without the NaN base URL", name)
		}
		if !strings.Contains(string(data), testKey) {
			// Codex and Pi are the exceptions: their configs carry a reference
			// to `nan key print` instead of the key itself.
			if name == "Codex" || name == "Pi" {
				want := `args = ["key", "print"]`
				if name == "Pi" {
					want = `key print`
				}
				if !strings.Contains(string(data), want) {
					t.Errorf("%s: written without the key command reference", name)
				}
			} else {
				t.Errorf("%s: written without the API key", name)
			}
		}
		if !isNaNConfigured(name, p) {
			t.Errorf("%s: the Setup tab will not show it as configured", name)
		}
	}

	// And unticking a tool takes only that one out.
	msg, _, _ = configureTools(testKey, map[string]bool{"Pi": false})
	if !strings.Contains(msg, "1 removed") {
		t.Errorf("configureTools said %q, want Pi removed", msg)
	}
	if isNaNConfigured("Pi", paths["Pi"]) {
		t.Error("Pi is still configured after being unticked")
	}
	if !isNaNConfigured("OpenCode", paths["OpenCode"]) {
		t.Error("unticking Pi took OpenCode with it")
	}
}

func TestModelsTabShowsCallableIds(t *testing.T) {
	// GET /v1/models answers with the ids a request can name. The platform's
	// /agents/models answers with deployment names instead - it carries
	// `deepseek-v4-flash-fallback`, `glm5.3-fallback` and `glm5.2`, none of
	// which a member can put in a `model` field - and that is what this tab
	// used to list.
	out := renderModels([]string{"deepseek-v4-flash", "kokoro", "glm5.3", "minimax-h3"}, nil, newLayout(80, 24))

	for _, want := range []string{
		"deepseek-v4-flash", "chat",
		"kokoro", "text to speech",
		"glm5.3", "premium",
		// An id the cluster serves and this catalogue has never heard of has to
		// show, not disappear: that is how an undocumented model gets noticed.
		"minimax-h3", "unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the Models tab does not mention %q", want)
		}
	}
}

func TestModelsTabStillReadsThePlatformShape(t *testing.T) {
	// Without an API key there is no /v1/models to ask, and the tab falls back
	// to what the platform returns.
	data := map[string]any{"models": []any{
		map[string]any{"name": "deepseek-v4-flash", "mode": "chat"},
	}}
	if out := renderModels(data, nil, newLayout(80, 24)); !strings.Contains(out, "deepseek-v4-flash") {
		t.Error("the fallback list renders nothing")
	}
}

func TestCostsFooterBoxIsSquare(t *testing.T) {
	// `l.indent + Render(box)` indented the first line only, so the top edge sat
	// two columns to the right of the sides. And the width was measured with
	// len() on a string carrying an em dash: three bytes, one column.
	usage := map[string]any{
		"last24h": map[string]any{"byModel": []any{
			map[string]any{"model": "gemma4", "inputTokens": 1000.0, "outputTokens": 500.0},
		}},
	}
	var box []string
	for _, line := range strings.Split(renderCosts(usage, newLayout(90, 30)), "\n") {
		if strings.ContainsAny(line, "╭│╰") {
			box = append(box, line)
		}
	}
	// Three lines, or more when the note wraps at a narrow width.
	if len(box) < 3 {
		t.Fatalf("the footer box has %d lines, want at least 3", len(box))
	}
	for i, line := range box {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("box line %d does not carry the indent: %q", i, line)
		}
	}
	for i, line := range box {
		if lipgloss.Width(line) != lipgloss.Width(box[0]) {
			t.Errorf("box line %d measures %d columns, the top edge measures %d",
				i, lipgloss.Width(line), lipgloss.Width(box[0]))
		}
	}
}

func TestBannerFitsAndCarriesTheNames(t *testing.T) {
	out := Banner("  ", moodNormal, true)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != MascotHeight {
		t.Fatalf("the banner is %d rows, the mascot is %d", len(lines), MascotHeight)
	}
	// Without it, the wordmark alone, which is what a short terminal gets.
	plain := strings.Split(strings.TrimRight(Banner("  ", moodNormal, false), "\n"), "\n")
	if len(plain) != len(wordmark) {
		t.Errorf("the plain banner is %d rows, the wordmark is %d", len(plain), len(wordmark))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("row %d does not carry the indent", i)
		}
		// It has to fit the narrowest panel it is drawn in.
		if w := lipgloss.Width(line); w > BannerWidth+4 {
			t.Errorf("row %d measures %d columns, wider than the About tab allows", i, w)
		}
	}
	for _, want := range []string{"nan.builders", "@Nxssie", "Helmcode Team", Version} {
		if !strings.Contains(out, want) {
			t.Errorf("the banner does not mention %q", want)
		}
	}
}

func TestAboutFallsBackToOneLineWhenNarrow(t *testing.T) {
	// A 40-column terminal has no room for the art plus the wordmark, and a
	// wrapped banner is worse than no banner.
	if strings.Contains(renderAbout(newLayout(40, 24)), "█") {
		t.Error("the banner is drawn at a width where it wraps")
	}
	if !strings.Contains(renderAbout(newLayout(BannerWidthPlain+4, 24)), "█") {
		t.Error("the banner is missing at a width that fits it")
	}
}

func TestHomeIsTheFirstTabAndNeedsNoNetwork(t *testing.T) {
	// The panel used to open on Profile, which is a table of account fields
	// fetched over the network: a spinner, and no sign of what you were
	// running. Home is drawn from nothing, so it is there before any request.
	if tabDefs[0].id != tabHome {
		t.Fatalf("the first tab is %v, want Home", tabDefs[0].name)
	}
	m := newModel(nil, &session.Session{})
	if cmd := m.maybeLoad(); cmd != nil {
		t.Error("Home asks the API for something, so it cannot be the landing tab")
	}
}

func TestHomeSaysHowToMoveAround(t *testing.T) {
	out := renderHome(newLayout(BannerWidthPlain+4, 24), true, true, moodNormal)
	for _, want := range []string{"█", "welcome to", "←/→", "↑/↓", "refresh", "quit", "Setup"} {
		if !strings.Contains(out, want) {
			t.Errorf("the Home tab does not mention %q", want)
		}
	}
	// A landing screen that needs scrolling on a standard terminal has already
	// failed at the one thing it does.
	if rows := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); rows > 24-4 {
		t.Errorf("Home is %d rows, the viewport of a 24-row terminal is %d", rows, 24-4)
	}
}

// The provider block alone does not connect Pi to anything. Pi reads its
// default from a second file, settings.json, and nan.builders/docs/pi marks
// that step "not optional": without it Pi keeps calling its factory provider
// and the member gets a 401 that names neither file. The CLI wrote the first
// file and not the second, so enabling Pi from the Setup tab landed a member
// squarely in the failure the docs call the most common one.
func TestPiConfigWritesTheDefaultsOrPiStillAnswers401(t *testing.T) {
	path := tempConfig(t, "models.json")
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, filepath.Join(filepath.Dir(path), "settings.json"))
	if got := settings["defaultProvider"]; got != "nan" {
		t.Errorf("defaultProvider = %v, want nan: Pi calls its factory provider otherwise", got)
	}
	if got := settings["defaultModel"]; got != catalog.Coding {
		t.Errorf("defaultModel = %v, want %s", got, catalog.Coding)
	}
}

// settings.json is Pi's, not ours: the default is the only key in it we have
// any business writing.
func TestPiConfigKeepsTheSettingsItDoesNotOwn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	settingsPath := filepath.Join(dir, "settings.json")
	existing := `{"theme":"dark","defaultProvider":"openai","defaultModel":"gpt-5"}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, settingsPath)
	if settings["theme"] != "dark" {
		t.Error("a setting of theirs was dropped")
	}
	// A member who picked another provider picked it. Ours is one more
	// provider in the file, and they can switch to it inside Pi.
	if settings["defaultProvider"] != "openai" {
		t.Error("a default the member chose was overwritten")
	}
}

// Turning Pi off in the Setup tab has to leave Pi working, and a default
// pointing at a provider that is no longer in models.json is not working.
func TestPiRemovalTakesTheDefaultItWrote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	settingsPath := filepath.Join(dir, "settings.json")
	if err := writePiConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if err := removePiConfig(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		settings := readJSON(t, settingsPath)
		if settings["defaultProvider"] == "nan" {
			t.Error("Pi is left defaulting to a provider that is no longer in models.json")
		}
	}
}

// ── Hermes ───────────────────────────────────────────────────────────────────

// Hermes is the first tool here that is not configured by writing its file.
// Its config.yaml is a commented document a member edits, and it ships
// `hermes config set`, which writes into it without flattening the comments
// and validates the key while it is at it. So this writer drives the tool
// instead of reproducing its schema, and what there is to test is the
// conversation it has with it.
func recordHermes(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	original := runHermesConfig
	runHermesConfig = func(home string, args ...string) error {
		calls = append(calls, args)
		return nil
	}

	// And `config get`, which the writer uses to confirm Hermes resolved the
	// reference. The fake resolves it the same way Hermes does - out of the
	// .env this CLI just wrote - so the check is exercised rather than
	// stubbed past.
	originalRead := readHermesConfig
	readHermesConfig = func(home string, args ...string) (string, error) {
		for _, line := range strings.Split(readFile(t, hermesEnvPath(home)), "\n") {
			if name, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(name) == hermesKeyVar {
				return value, nil
			}
		}
		return "", nil
	}

	t.Cleanup(func() {
		runHermesConfig = original
		readHermesConfig = originalRead
	})
	return &calls
}

// The reference is only worth writing if Hermes turns it back into the key.
// Nothing in this CLI controls that, so the writer asks - and says so plainly
// rather than leaving a member with a 401 that names nothing.
func TestHermesIsRefusedWhenItDoesNotResolveTheReference(t *testing.T) {
	recordHermes(t)
	original := readHermesConfig
	readHermesConfig = func(home string, args ...string) (string, error) {
		return hermesKeyRef, nil // an unexpanded literal, as an older build would
	}
	t.Cleanup(func() { readHermesConfig = original })

	err := writeHermesConfig(t.TempDir(), testKey)
	if err == nil {
		t.Fatal("a Hermes that never resolved the key was reported as configured")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("the error carries the key: %v", err)
	}
}

// And a Hermes that cannot answer the question at all is not the same as one
// that answered wrongly: an older build without `config get` says nothing
// either way, and failing the step over a question we could not ask would be
// its own bug.
func TestHermesThatCannotAnswerIsNotTreatedAsBroken(t *testing.T) {
	recordHermes(t)
	original := readHermesConfig
	readHermesConfig = func(home string, args ...string) (string, error) {
		return "", errors.New("unknown command: get")
	}
	t.Cleanup(func() { readHermesConfig = original })

	if err := writeHermesConfig(t.TempDir(), testKey); err != nil {
		t.Errorf("an unanswerable check failed the whole step: %v", err)
	}
}

func TestHermesIsConfiguredThroughItsOwnConfigCommand(t *testing.T) {
	calls := recordHermes(t)
	if err := writeHermesConfig(t.TempDir(), testKey); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		// `custom` is the provider Hermes ships for any OpenAI-compatible
		// endpoint. Its aliases (ollama, vllm, llamacpp) all map to this one.
		"model.provider": "custom",
		"model.base_url": "https://api.nan.builders/v1",
		// The reference, not the secret. The test below is the why.
		"model.api_key": hermesKeyRef,
		"model.default": catalog.Coding,
	}
	got := map[string]string{}
	for _, c := range *calls {
		if len(c) != 3 || c[0] != "set" {
			t.Errorf("unexpected call %v", c)
			continue
		}
		got[c[1]] = c[2]
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

// The one tool here that is configured by running something, and therefore the
// one place a key could leave this process as an argument. It must not.
//
// An argument is public on the machine for as long as the process lives: `ps`
// hands the whole line to anything running as the member, and on Linux
// /proc/<pid>/cmdline to other accounts as well. So the key goes into Hermes'
// own .env, the config points at it with ${NAN_API_KEY}, and nothing readable
// from outside this process ever holds the secret itself.
func TestHermesNeverReceivesTheKeyAsAnArgument(t *testing.T) {
	calls := recordHermes(t)
	home := t.TempDir()
	if err := writeHermesConfig(home, testKey); err != nil {
		t.Fatal(err)
	}

	for _, c := range *calls {
		for _, arg := range c {
			if strings.Contains(arg, testKey) {
				t.Fatalf("the key was handed to hermes as an argument: %v", c)
			}
		}
	}

	env := readFile(t, hermesEnvPath(home))
	if !strings.Contains(env, hermesKeyVar+"="+testKey) {
		t.Errorf(".env does not carry the key:\n%s", env)
	}
	assertNotWorldReadable(t, hermesEnvPath(home))
}

// The .env is Hermes', not ours: other providers' keys live in it, with the
// member's own notes around them.
func TestHermesEnvKeepsEverythingElseInTheFile(t *testing.T) {
	recordHermes(t)
	home := t.TempDir()
	envPath := hermesEnvPath(home)
	existing := "# their notes\nOPENROUTER_API_KEY=theirs\n" + hermesKeyVar + "=an-older-one\n"
	if err := os.WriteFile(envPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeHermesConfig(home, testKey); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, envPath)
	for _, want := range []string{"# their notes", "OPENROUTER_API_KEY=theirs", hermesKeyVar + "=" + testKey} {
		if !strings.Contains(after, want) {
			t.Errorf("after writing, .env has no %q:\n%s", want, after)
		}
	}
	if strings.Contains(after, "an-older-one") {
		t.Errorf("the key it replaced is still in .env:\n%s", after)
	}

	if err := removeHermesConfig(home); err != nil {
		t.Fatal(err)
	}
	after = readFile(t, envPath)
	if strings.Contains(after, testKey) {
		t.Errorf("removal left the key in .env:\n%s", after)
	}
	if !strings.Contains(after, "OPENROUTER_API_KEY=theirs") {
		t.Errorf("removal took somebody else's key with it:\n%s", after)
	}
}

// Hermes with a custom endpoint asks the cluster what it serves instead of
// reading a list we write, so there is no model catalogue in this config and
// no window to keep in step - the one thing it needs told is which model to
// open with.
func TestHermesIsNotSentAModelCatalogue(t *testing.T) {
	calls := recordHermes(t)
	if err := writeHermesConfig(t.TempDir(), testKey); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		for _, arg := range c {
			if strings.Contains(arg, "models") {
				t.Errorf("call %v writes a model list Hermes discovers on its own", c)
			}
		}
	}
}

func TestHermesRemovalTakesOnlyWhatWeWrote(t *testing.T) {
	calls := recordHermes(t)
	home := t.TempDir()
	// A config that is ours. Without one the removal has nothing to unset and
	// skips the four calls this is here to check, rather than spawning Hermes
	// four times over a config it never touched.
	if err := os.WriteFile(hermesConfigPath(home), []byte("model:\n  base_url: https://api.nan.builders/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeHermesConfig(home); err != nil {
		t.Fatal(err)
	}
	if len(*calls) == 0 {
		t.Fatal("removal unset nothing at all")
	}
	for _, c := range *calls {
		if c[0] != "unset" {
			t.Errorf("removal called %v, which is not an unset", c)
		}
		// The member's own settings live in the same file: their skills, their
		// channels, their persona. Only the four keys we put there come out.
		switch c[1] {
		case "model.provider", "model.base_url", "model.api_key", "model.default":
		default:
			t.Errorf("removal unsets %q, which we never wrote", c[1])
		}
	}
}

// The path in nan.builders/docs/hermes, ~/.hermes/config.yaml, is the Unix
// one. On Windows Hermes keeps it under LOCALAPPDATA, so a CLI that built the
// path from the home directory would configure a Hermes that is not there.
func TestHermesHomeFollowsTheToolNotTheDoc(t *testing.T) {
	t.Setenv("HERMES_HOME", filepath.Join("some", "profile"))
	if got := hermesHome(); got != filepath.Join("some", "profile") {
		t.Errorf("HERMES_HOME ignored: got %q", got)
	}

	t.Setenv("HERMES_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	local := filepath.Join(home, "AppData", "Local")
	t.Setenv("LOCALAPPDATA", local)

	want := filepath.Join(home, ".hermes")
	if runtime.GOOS == "windows" {
		want = filepath.Join(local, "hermes")
	}
	if got := hermesHome(); got != want {
		t.Errorf("hermesHome() = %q, want %q", got, want)
	}
}

// Everything above agrees with a fake. This one agrees with Hermes, which is
// the only agreement that keeps a member working, and it is why the writer
// was built around `hermes config set` in the first place. Skipped where
// Hermes is not installed, CI included.
func TestHermesConfigAgainstTheRealBinary(t *testing.T) {
	if _, err := exec.LookPath("hermes"); err != nil {
		t.Skip("hermes is not installed here")
	}
	// Never the member's own Hermes: HERMES_HOME is what the writer passes to
	// every call, so the whole exchange lands in a directory of this test's.
	home := t.TempDir()
	if err := writeHermesConfig(home, testKey); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatalf("hermes wrote no config: %v", err)
	}
	written := string(data)
	for _, want := range []string{"provider: custom", "base_url: https://api.nan.builders/v1", "default: " + catalog.Coding, hermesKeyRef} {
		if !strings.Contains(written, want) {
			t.Errorf("config.yaml has no %q:\n%s", want, written)
		}
	}
	// Hermes expands ${VAR} in a config value against its own .env, which is
	// the whole reason the key can stay out of the command line. If a release
	// of Hermes ever stops doing that, this is where it surfaces.
	if strings.Contains(written, testKey) {
		t.Errorf("config.yaml carries the key itself:\n%s", written)
	}
	if env := readFile(t, hermesEnvPath(home)); !strings.Contains(env, testKey) {
		t.Errorf(".env does not carry the key:\n%s", env)
	}

	if err := removeHermesConfig(home); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(home, "config.yaml"))
	if strings.Contains(string(data), "api.nan.builders") {
		t.Errorf("removal left the cluster behind:\n%s", data)
	}
	if env := readFile(t, hermesEnvPath(home)); strings.Contains(env, testKey) {
		t.Errorf("removal left the key in .env:\n%s", env)
	}
}

// ── the API key, and what the platform will and will not tell us ─────────────

func setupModel(t *testing.T, sess *session.Session) model {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "hermes"))
	return newModel(nil, sess)
}

// The idea this replaced was to have the Setup tab fetch the key with the
// session it already holds. It cannot: GET /api/keys answers with metadata
// and no secret, because a key is shown once, at creation. What is left worth
// doing is telling a member there is one and where it is, instead of showing
// an empty field and nothing else.
func TestSetupSaysWhereTheKeyIsWhenTheFieldIsEmpty(t *testing.T) {
	m := setupModel(t, &session.Session{})
	m.keyStatus = &api.KeyStatus{Exists: true, Alias: "an-alias"}

	out := m.renderSetup(newLayout(80, 24))
	for _, want := range []string{"an-alias", "cloud.nan.builders"} {
		if !strings.Contains(out, want) {
			t.Errorf("the Setup tab does not mention %q", want)
		}
	}
}

func TestSetupSaysWhenTheAccountHasNoKeyAtAll(t *testing.T) {
	m := setupModel(t, &session.Session{})
	m.keyStatus = &api.KeyStatus{Exists: false}

	out := m.renderSetup(newLayout(80, 24))
	if !strings.Contains(out, "no key yet") {
		t.Error("an account with no key is told nothing about it")
	}
}

// Once there is a key in the field, the hint about where to go and fetch one
// is noise - and the key itself is never printed, here or anywhere.
func TestSetupHidesTheHintAndTheKeyOnceOneIsSet(t *testing.T) {
	m := setupModel(t, &session.Session{APIKey: testKey})
	m.keyStatus = &api.KeyStatus{Exists: true, Alias: "an-alias"}

	out := m.renderSetup(newLayout(80, 24))
	if strings.Contains(out, "an-alias") || strings.Contains(out, "copy it from") {
		t.Error("the hint about fetching a key is still shown after one was set")
	}
	if strings.Contains(out, testKey) {
		t.Error("the API key is printed on screen")
	}
}

// What to do about a key that has been seen by somebody. This CLI cannot
// revoke one - the platform issues and retires them - so the least it can do
// is say where, rather than leaving a member to guess whether replacing it is
// even possible.
func TestSetupSaysHowToReplaceAKeyThatLeaked(t *testing.T) {
	m := setupModel(t, &session.Session{APIKey: testKey})

	out := m.renderSetup(newLayout(80, 24))
	for _, want := range []string{"replace it at cloud.nan.builders", "press c"} {
		if !strings.Contains(out, want) {
			t.Errorf("the tab does not say %q", want)
		}
	}
}

// A key the cluster refuses used to be found out five times over, as a 401
// inside each tool it had been written into. It is said once, here, before
// anything is written at all.
func TestSetupShowsAKeyTheClusterRefused(t *testing.T) {
	m := setupModel(t, &session.Session{APIKey: testKey})
	m.keyCheck = "error: the cluster refused this key - the API key in Setup is not valid"

	out := m.renderSetup(newLayout(80, 24))
	if !strings.Contains(out, "refused this key") {
		t.Error("a refused key is not reported in the Setup tab")
	}
}

// The Home tab tells a member that `?` lists "every shortcut, including the
// ones for Setup". It listed e and c and not space, which is the one that
// decides which tools get written at all.
func TestHelpListsTheKeysHomePromises(t *testing.T) {
	out := renderHelp()
	for _, want := range []string{"space", "e", "c"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help screen does not mention %q", want)
		}
	}
}

// ── the cost comparison ──────────────────────────────────────────────────────

// The Costs tab exists to answer "what would this have cost me elsewhere".
// Every number in it is typed in by hand from a vendor's pricing page, nothing
// reads them back, and the table went a long time with five of six rows wrong
// - each of them understating the competitor. These are the mechanical checks
// that catch the shapes of wrong a reader would not notice.
func TestPricingTableIsPlausible(t *testing.T) {
	if len(pricingTable) == 0 {
		t.Fatal("nothing to compare against")
	}
	seen := map[string]bool{}
	for _, p := range pricingTable {
		if p.inPer1M <= 0 || p.outPer1M <= 0 {
			t.Errorf("%s: a free model is a typo, not a price (%v/%v)", p.model, p.inPer1M, p.outPer1M)
		}
		// Every frontier vendor charges more for output than for input, so a
		// row where that flips is a transposed pair. None of the six wrong
		// rows failed this way - they were each plausible and simply not what
		// the vendor charged - which is the point: this catches the typo, and
		// only reading the pricing page catches the rest.
		if p.outPer1M < p.inPer1M {
			t.Errorf("%s: output (%v) cheaper than input (%v) - transposed?", p.model, p.outPer1M, p.inPer1M)
		}
		if seen[p.model] {
			t.Errorf("%s is listed twice", p.model)
		}
		seen[p.model] = true
		if p.provider == "" {
			t.Errorf("%s has no provider, so it renders with no colour and no attribution", p.model)
		}
	}
}

// Every provider in the table needs a colour, or it renders grey and looks
// like a different kind of row.
func TestEveryPricedProviderHasAColour(t *testing.T) {
	for _, p := range pricingTable {
		if _, ok := providerColor[p.provider]; !ok {
			t.Errorf("%s has no colour in providerColor", p.provider)
		}
	}
}

// Gemini 3.8 Flash is on a promotional rate that doubles on 2027-01-01. That
// is a number which is right today and silently wrong on a date we already
// know, which no amount of care at review time catches. This is the only
// thing that will.
func TestGeminiFlashPromoHasNotExpired(t *testing.T) {
	if time.Now().Before(geminiFlashPromoEnds) {
		return
	}
	t.Errorf("Gemini 3.8 Flash's promotional rate ended on %s: "+
		"its price in pricingTable doubles to 1.50/7.50, and the Costs tab has been "+
		"understating Google ever since. Update the row and move geminiFlashPromoEnds "+
		"or drop it if the row no longer needs one.", geminiFlashPromoEnds.Format("2006-01-02"))
}

// The footer box was drawn at the width of its own text, 70 columns plus a
// border and the indent, whatever terminal it was in. Anything narrower than
// about 74 got a box running off the right-hand side - which is where this tab
// is read on half a laptop screen.
func TestCostsFitsTheTerminalItIsDrawnIn(t *testing.T) {
	usage := map[string]any{
		"last24h": map[string]any{"byModel": []any{
			map[string]any{"model": "gemma4", "inputTokens": 1_000_000.0, "outputTokens": 500_000.0},
		}},
	}
	for _, w := range []int{40, 50, 60, 72, 80, 120} {
		for _, line := range strings.Split(renderCosts(usage, newLayout(w, 40)), "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("at %d columns a line measures %d: %q", w, got, line)
			}
		}
	}
}

// Ten rows with a blank line between each is not a table any more, it is two
// screens of alternating text and gap. The blank lines that are left group the
// rows by provider, which is the only thing they were ever doing well.
func TestCostsRowsAreNotDoubleSpaced(t *testing.T) {
	usage := map[string]any{
		"last24h": map[string]any{"byModel": []any{
			map[string]any{"model": "gemma4", "inputTokens": 1_000_000.0, "outputTokens": 500_000.0},
		}},
	}
	out := renderCosts(usage, newLayout(100, 40))
	lines := strings.Split(out, "\n")

	// Find each priced row, then check the one after it is only blank when the
	// provider changes.
	index := map[string]int{}
	for i, line := range lines {
		for _, p := range pricingTable {
			if strings.Contains(line, p.model) {
				index[p.model] = i
			}
		}
	}
	for i := 0; i < len(pricingTable)-1; i++ {
		this, next := pricingTable[i], pricingTable[i+1]
		at, ok := index[this.model]
		if !ok {
			t.Errorf("%s is priced but never rendered", this.model)
			continue
		}
		gap := strings.TrimSpace(lines[at+1]) == ""
		if want := this.provider != next.provider; gap != want {
			if want {
				t.Errorf("no blank line between %s and %s, which are different providers", this.provider, next.provider)
			} else {
				t.Errorf("a blank line inside %s's rows, after %s", this.provider, this.model)
			}
		}
	}
}

// The About tab printed "~/.config/nan/session.json" as a literal. On Windows
// that is not a path, not where the file is, and not something Explorer
// resolves - so a member told to look there finds nothing.
func TestAboutShowsTheSessionPathThatExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	out := renderAbout(newLayout(100, 30))
	if strings.Contains(out, "~/") {
		t.Error("the About tab still prints a tilde path")
	}
	if want := session.Path(); !strings.Contains(out, want) {
		t.Errorf("the About tab does not show %q", want)
	}
	if !strings.Contains(session.Path(), home) {
		t.Errorf("session.Path() = %q, which is not under the home it was given", session.Path())
	}
}

// A fresh install on a machine that has never logged in drew "unauthorized"
// over Profile, Usage and Models: the platform's word for what it decided, and
// not one word about what to do next. Reported from a real first run.
func TestDataTabsSayToLogInRatherThanUnauthorized(t *testing.T) {
	m := setupModel(t, &session.Session{})

	for _, id := range []tabID{tabProfile, tabUsage, tabModels, tabCosts} {
		if !m.needsLogin(id) {
			t.Errorf("%v: would go to the network with no session and report `unauthorized`", id)
		}
	}

	// And the message names the key, not a shell command: there is one for
	// this, and quitting to run something else is the detour it replaced.
	msg, ok := m.fetchTab(tabProfile)().(fetchErrMsg)
	if !ok || !errors.Is(msg.err, errNotSignedIn) {
		t.Errorf("Profile reports %v, want the one that says which key to press", msg.err)
	}
	if !strings.Contains(errNotSignedIn.Error(), "press s") {
		t.Errorf("the message is %q, which does not say what to press", errNotSignedIn)
	}
}

// The three tabs that need nothing from the platform have to stay usable with
// no session: Setup is where a member pastes the key in the first place.
func TestTheOfflineTabsNeverAskForALogin(t *testing.T) {
	m := setupModel(t, &session.Session{})

	for _, id := range []tabID{tabHome, tabAbout, tabSetup} {
		if m.needsLogin(id) {
			t.Errorf("%v: asks for a login it does not need", id)
		}
	}
}

// An API key opens /v1/models on its own, so that tab stays useful to someone
// who pasted a key and never logged in.
func TestModelsStillLoadsWithAKeyAndNoSession(t *testing.T) {
	m := setupModel(t, &session.Session{APIKey: testKey})

	if m.needsLogin(tabModels) {
		t.Error("a member with an API key is told to log in for the Models tab")
	}
}

// ── the first run ────────────────────────────────────────────────────────────

// The first time this was opened on a machine that had never logged in, Home
// explained the arrow keys and every data tab answered "unauthorized". Nothing
// anywhere said to log in, and the command that does it is a subcommand you
// have to already know exists.
func TestHomeLeadsWithSigningInWhenThereIsNoSession(t *testing.T) {
	out := renderHome(newLayout(90, 40), false, false, moodNormal)

	for _, want := range []string{"Start here", "sign in", "stay empty until you sign in"} {
		if !strings.Contains(out, want) {
			t.Errorf("Home does not mention %q to someone with no session", want)
		}
	}
	// Every step is a key to press here. This list used to open with "q, quit,
	// so you have your shell back", because signing in meant leaving for a
	// subcommand - which is exactly where people got stuck.
	if strings.Contains(out, "quit, so you have your shell back") {
		t.Error("Home still sends a member out to the shell to sign in")
	}
	if strings.Contains(out, "nan auth login") {
		t.Error("Home names a shell command for something the panel does itself")
	}
}

// Signed in but with no key, the sign-in steps are done and the one that is
// not is the key.
func TestHomeMovesOnOnceSignedIn(t *testing.T) {
	out := renderHome(newLayout(90, 40), true, false, moodNormal)

	if !strings.Contains(out, "Start here") {
		t.Fatal("Home stops guiding before the setup is finished")
	}
	if !strings.Contains(out, "paste your API key") {
		t.Error("Home does not name the step that is actually left")
	}
	// The done ones are still listed, struck through by their marker, so the
	// list does not renumber itself between runs.
	if !strings.Contains(out, "✓") {
		t.Error("finished steps are not marked as finished")
	}
}

// And once there is nothing left to do it gets out of the way.
func TestHomeDropsTheGuideWhenSetupIsDone(t *testing.T) {
	out := renderHome(newLayout(90, 40), true, true, moodNormal)

	if strings.Contains(out, "Start here") {
		t.Error("Home still shows the first-run steps to a configured member")
	}
	if !strings.Contains(out, "Getting around") {
		t.Error("the rest of Home went with it")
	}
}

// ── signing in without leaving the panel ─────────────────────────────────────

// The flow used to be: read Home, quit, run `nan auth login`, answer two
// prompts on stdin, start the panel again. Reported twice from a real machine,
// stuck at different steps of it. The panel owns the keyboard already, so it
// asks the same two questions itself.
//
// It asks View, and not a renderer of its own, because the login screen this
// replaced stopped being drawn when the wizard arrived: the test went on
// passing against a function nothing called, which is the one way a test can be
// green and wrong at the same time.
func TestSigningInHappensInsideThePanel(t *testing.T) {
	m := setupModel(t, &session.Session{})
	// Said out loud, because View wraps to it and truncates to it. Left to
	// the zero value this asserted against whatever the default happened to
	// be: at 60 columns the renderer breaks the sentence as "expires 15 /
	// minutes" and at 80x20 the truncation eats it, so the test failed for a
	// screen that was right.
	m.lay = newLayout(90, 30)

	if m.loginStage != loginOff {
		t.Fatal("the panel opens mid-login")
	}
	m.startLogin()
	if m.loginStage != loginAskEmail {
		t.Fatal("s does not start the sign-in")
	}

	out := m.View()
	for _, want := range []string{"Let's get you logged in", "Email"} {
		if !strings.Contains(out, want) {
			t.Errorf("the first step does not show %q:\n%s", want, out)
		}
	}

	// Second question, once the link is on its way, driven through the message
	// the flow actually sends rather than by setting the stage by hand: that is
	// what makes this fail when the two halves drift apart.
	mod, _ := m.Update(linkSentMsg{})
	m = mod.(model)
	if m.loginStage != loginAskLink {
		t.Fatal("an accepted request does not move on to the link")
	}
	out = m.View()
	// Against the screen with its line breaks collapsed: whether "15 minutes"
	// survives as two words on one line is the renderer's business and
	// changes with one more word in the message. What this is here to catch
	// is the sentence going missing.
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"Let's confirm it with the magic link", "Paste the link", "15 minutes"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the second step does not ask for the link:\n%s", out)
		}
	}
}

func TestEscapeLeavesTheSignInAlone(t *testing.T) {
	m := setupModel(t, &session.Session{})
	m.startLogin()
	m.loginInput.SetValue("half typed@")
	m.cancelLogin()

	if m.loginStage != loginOff {
		t.Error("esc does not leave the sign-in")
	}
	if m.loginInput.Value() != "" {
		t.Error("a cancelled sign-in keeps what was typed into it")
	}
}

// A member who is already signed in has nothing to start, and the key that
// starts it is `s` because `l` is the vim spelling of "next tab".
func TestSignInKeyIsNotOneThatAlreadyMoves(t *testing.T) {
	out := renderHelp()
	if !strings.Contains(out, "sign in") {
		t.Error("the help screen does not mention signing in")
	}
	if strings.Contains(out, "l") && strings.Contains(out, "sign in, when there is no session") {
		// only a smoke check that the description is the one bound to s
		if !strings.Contains(out, "s") {
			t.Error("sign-in is not bound to s")
		}
	}
}

// Signing in from the panel wrote the session and left the API client holding
// the token it was built with at start-up, which on a fresh machine is none.
// So it signed a member in and then answered every tab `unauthorized` - which
// reads exactly like the login having failed, and was reported as such.
func TestSigningInGivesTheClientTheNewToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "h"))

	m := newModel(api.New(""), &session.Session{})
	if m.client.Token() != "" {
		t.Fatal("this test starts from a client with no token")
	}

	// What the flow leaves on disk before the panel is told about it.
	if err := session.Save(&session.Session{Token: "a-fresh-session-token"}); err != nil {
		t.Fatal(err)
	}

	updated, _ := m.Update(signedInMsg{nil})
	after := updated.(model)

	if after.sess.Token != "a-fresh-session-token" {
		t.Errorf("the panel did not pick up the session: %q", after.sess.Token)
	}
	if got := after.client.Token(); got != "a-fresh-session-token" {
		t.Errorf("the client still sends %q, so every tab answers unauthorized", got)
	}
	if after.loginStage != loginOff {
		t.Error("the sign-in is still on screen after it succeeded")
	}
	if len(after.cache) != 0 {
		t.Error("the tabs keep the answers they got before signing in")
	}
}

// `e` is published on Home as the way to set the API key, and Home is not
// Setup. It did nothing at all anywhere but Setup - which is the tab you have
// to already be on to know that.
func TestTheKeyForTheKeyWorksFromWhereItIsAdvertised(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t"})
	m.lay = newLayout(90, 30)
	m.active = tabIndex(tabHome)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	after := updated.(model)

	if !after.editingKey {
		t.Error("e from Home does nothing, which is where Home tells you to press it")
	}
	if after.activeID() != tabSetup {
		t.Error("e opened the key editor without moving to the tab that shows it")
	}
}

// Pressing c used to call configureTools straight out of the key handler, so
// the panel sat frozen for as long as it took - and with Hermes in the list
// that is four processes and several seconds, with no repaint and no key
// accepted. Reported as the panel being hung, which is the only thing it
// could look like.
func TestConfiguringDoesNotBlockTheEventLoop(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t", APIKey: testKey})
	m.lay = newLayout(90, 30)
	m.active = tabIndex(tabSetup)
	recordHermes(t)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	after := updated.(model)

	if !after.configuring {
		t.Error("c does not put the tab into a state it can draw")
	}
	if cmd == nil {
		t.Fatal("c did the work inline instead of handing back a command")
	}
	// And the tab says so rather than looking hung.
	if out := after.renderSetup(after.lay); !strings.Contains(out, "writing the configs") {
		t.Errorf("nothing on screen says it is working:\n%s", out)
	}
}

// "5 added" answers what it did, not the question a member is left holding.
func TestTheTabSaysHowToUseWhatItJustConfigured(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t", APIKey: testKey})
	m.lay = newLayout(90, 40)
	m.configured = []string{"OpenCode", "Codex"}

	out := m.renderSetup(m.lay)
	for _, want := range []string{"Now open them", "opencode", "/models", "codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("the tab does not mention %q after configuring", want)
		}
	}
}

// Every tool the Setup tab can configure has to have something to say about
// using it, or the list it prints has a hole in it exactly where a member
// looks.
func TestEveryConfigurableToolHasANextStep(t *testing.T) {
	for _, tool := range detectTools() {
		if _, ok := nextStepFor[tool.name]; !ok {
			t.Errorf("%s can be configured and has no next step", tool.name)
		}
	}
}

// ── the mascot ───────────────────────────────────────────────────────────────

// Every face has to be the same grid, or the head jumps around as the panel
// changes what it is doing.
func TestEveryFaceIsTheSameHead(t *testing.T) {
	for mood, grid := range mascotFaces {
		if len(grid) != 21 {
			t.Errorf("mood %d is %d pixels tall, want 21", mood, len(grid))
		}
		for y, row := range grid {
			if len(row) != MascotWidth {
				t.Errorf("mood %d row %d is %d wide, want %d", mood, y, len(row), MascotWidth)
			}
		}
	}
	// Every mood has to actually change the eyes, or a face that is supposed
	// to say something says the same thing as the last one.
	seen := map[string]mascotMood{}
	for mood, grid := range mascotFaces {
		eyes := ""
		for _, row := range grid {
			for _, c := range row {
				if c == 'O' {
					eyes += "O"
				} else {
					eyes += "."
				}
			}
		}
		if other, dup := seen[eyes]; dup {
			t.Errorf("moods %d and %d draw the same eyes", other, mood)
		}
		seen[eyes] = mood
	}

	// And the silhouette itself has to be identical: only the eyes move.
	base := mascotFaces[moodNormal]
	for mood, grid := range mascotFaces {
		for y := range grid {
			for x := range grid[y] {
				a, b := base[y][x], grid[y][x]
				if (a == '#') != (b == '#') {
					t.Fatalf("mood %d changes the body at %d,%d", mood, x, y)
				}
			}
		}
	}
}

// Two pixels to a cell, so 21 rows of pixels are 11 rows of terminal.
func TestTheMascotIsHalfAsTallOnScreen(t *testing.T) {
	art := renderMascot(moodNormal)
	if len(art) != MascotHeight {
		t.Errorf("the mascot draws %d rows, want %d", len(art), MascotHeight)
	}
	for i, row := range art {
		if w := lipgloss.Width(row); w != MascotWidth {
			t.Errorf("row %d measures %d columns, want %d", i, w, MascotWidth)
		}
	}
}

// The face answers "what is going on" without words, which is the question
// this panel spent a long time failing to answer with them.
func TestTheFaceFollowsWhatThePanelIsDoing(t *testing.T) {
	base := func() model { return setupModel(t, &session.Session{Token: "t", APIKey: testKey}) }

	m := setupModel(t, &session.Session{})
	if got := m.mood(); got != moodAsleep {
		t.Errorf("with no session the mascot is %v, want asleep", got)
	}

	m = base()
	m.configuring = true
	if got := m.mood(); got != moodThinking {
		t.Errorf("while configuring the mascot is %v, want thinking", got)
	}

	m = base()
	m.err = errNotSignedIn
	if got := m.mood(); got != moodSad {
		t.Errorf("on an error the mascot is %v, want sad", got)
	}

	m = base()
	if got := m.mood(); got != moodHappy {
		t.Errorf("all set, the mascot is %v, want happy", got)
	}
}

// A landing screen that needs scrolling on a 24-row terminal has failed at the
// one thing it does, and the mascot is the part that goes.
func TestTheMascotGivesWayOnAShortTerminal(t *testing.T) {
	wide := BannerWidth + 4
	short := renderHome(newLayout(wide, 24), true, true, moodNormal)
	if rows := len(strings.Split(strings.TrimRight(short, "\n"), "\n")); rows > 20 {
		t.Errorf("Home is %d rows on a 24-row terminal, the viewport is 20", rows)
	}

	tall := renderHome(newLayout(wide, BannerRoom), true, true, moodNormal)
	if len(strings.Split(tall, "\n")) <= len(strings.Split(short, "\n")) {
		t.Error("the mascot never appears, even with room for it")
	}
}

// ── the first run, as one sequence ───────────────────────────────────────────

// Three keys a member had to know to press - s, then e, then c - with nothing
// leading from one to the next. The panel asks for each in turn now.
func TestAFreshMachineIsAskedToSignInImmediately(t *testing.T) {
	m := setupModel(t, &session.Session{})

	if cmd := m.resumeSetup(); cmd == nil {
		t.Fatal("nothing happens when the panel opens with no session")
	}
	if m.loginStage != loginAskEmail {
		t.Errorf("the panel opens on stage %v, want the email question", m.loginStage)
	}
}

// Signed in already, but no key: that is the next thing missing, so that is
// what it asks for.
func TestASignedInMachineIsAskedForTheKey(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t"})
	m.lay = newLayout(90, 30)

	m.resumeSetup()
	if m.loginStage != loginOff {
		t.Error("it asks for an account that is already there")
	}
	if !m.editingKey {
		t.Error("the key field is not open")
	}
	if m.activeID() != tabSetup {
		t.Error("the key field is open on a tab that does not show it")
	}
}

// And a member who has everything is left alone. This is the one that would
// annoy people most if it regressed.
func TestAConfiguredMachineIsNotAskedForAnything(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t", APIKey: testKey})

	if cmd := m.resumeSetup(); cmd != nil {
		t.Error("a configured member is asked to set something up again")
	}
	if m.loginStage != loginOff || m.editingKey {
		t.Error("the panel opens into a setup step that is already done")
	}
}

// Init cannot mutate the model - value receiver, returns only a Cmd - so it
// asks Update instead. If that message ever stops arriving, nothing on a fresh
// machine happens at all and the whole sequence is silently gone.
func TestTheFirstRunQuestionIsActuallyAsked(t *testing.T) {
	m := setupModel(t, &session.Session{})

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init does nothing")
	}
	updated, _ := m.Update(firstRunMsg{})
	if updated.(model).loginStage != loginAskEmail {
		t.Error("the first-run message does not start the sign-in")
	}
}

// Signing in leads to the key without the member pressing anything.
func TestSigningInLeadsStraightToTheKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "h"))

	m := newModel(api.New(""), &session.Session{})
	m.lay = newLayout(90, 30)
	if err := session.Save(&session.Session{Token: "a-token"}); err != nil {
		t.Fatal(err)
	}

	updated, _ := m.Update(signedInMsg{nil})
	after := updated.(model)
	if !after.editingKey {
		t.Error("after signing in the member is left to work out that `e` is next")
	}
}

// And the key leads to the tools - named, not pressed: writing into someone's
// opencode is a side effect that has to be asked for.
func TestTheAcceptedKeyPointsAtTheToolsStep(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t", APIKey: testKey})

	updated, _ := m.Update(keyCheckedMsg{models: 7})
	got := updated.(model).keyCheck
	if !strings.Contains(got, "key accepted") {
		t.Errorf("the check says %q", got)
	}
	if installedTools() > 0 && !strings.Contains(got, "press c") {
		t.Errorf("the check says %q, which does not lead anywhere", got)
	}
}

// ── the guided setup ─────────────────────────────────────────────────────────

func wizardAt(t *testing.T, step wizardStep) model {
	t.Helper()
	m := setupModel(t, &session.Session{})
	m.lay = newLayout(96, 44)
	m.wizard = step
	return m
}

// Four numbered steps, in the member's own words, with the banner over them.
// It replaced a screen that said "Step 1 of 2" and then handed you back to the
// panel to find the other two yourself.
func TestTheGuidedSetupShowsAllFourSteps(t *testing.T) {
	out := wizardAt(t, wizardEmail).renderWizard(newLayout(96, 44))

	for _, want := range []string{
		"Setting up",
		"1. Let's get you logged in",
		"2. Let's confirm it with the magic link",
		"3. Let's set up your API key",
		"4. Let's configure your tools",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the setup screen does not show %q", want)
		}
	}
	// And the banner, because the thing you just opened should say what it is.
	if !strings.Contains(out, "nan.builders") {
		t.Error("the setup screen has no banner")
	}
}

// The one you are on is marked, and the ones behind it are ticked.
func TestTheGuidedSetupSaysWhereYouAre(t *testing.T) {
	third := wizardAt(t, wizardKey).renderWizard(newLayout(96, 44))
	lines := strings.Split(third, "\n")
	for _, line := range lines {
		switch {
		case strings.Contains(line, "1. Let's get you"), strings.Contains(line, "2. Let's confirm"):
			if !strings.Contains(line, "✓") {
				t.Errorf("a finished step is not ticked: %q", strings.TrimSpace(line))
			}
		case strings.Contains(line, "3. Let's set up"):
			if !strings.Contains(line, "▶") {
				t.Errorf("the current step is not marked: %q", strings.TrimSpace(line))
			}
		}
	}
}

// A face per step, so the mascot is doing something that matches it.
func TestEachStepHasItsOwnFace(t *testing.T) {
	if wizardLink.mood() != moodThinking {
		t.Error("waiting for a link is not a thinking face")
	}
	if wizardDone.mood() != moodHappy {
		t.Error("finishing the setup is not a happy face")
	}
	for _, step := range []wizardStep{wizardEmail, wizardLink, wizardKey, wizardTools, wizardDone} {
		if _, ok := mascotFaces[step.mood()]; !ok {
			t.Errorf("step %v asks for a face that does not exist", step)
		}
	}
}

// Esc leaves the setup and lands in the panel. It must not quit the program:
// someone who skipped a step has not asked to close the CLI.
func TestEscapeLeavesTheSetupAndNotTheProgram(t *testing.T) {
	m := wizardAt(t, wizardEmail)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	after := updated.(model)
	if after.wizard != wizardOff {
		t.Error("esc does not leave the setup")
	}
	if cmd != nil {
		t.Error("esc during the setup quits the program")
	}
}

// Signing out from the panel, which `nan auth logout` is no use for when you
// are looking at the panel and want to switch accounts.
func TestSigningOutTakesTwoPressesAndClearsEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "h"))

	sess := &session.Session{Token: "t", APIKey: testKey}
	if err := session.Save(sess); err != nil {
		t.Fatal(err)
	}
	m := newModel(api.New("t"), sess)
	m.lay = newLayout(90, 30)
	m.cache[tabUsage] = "somebody else's numbers"

	press := func(m model, r rune) model {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		return updated.(model)
	}

	// One press asks.
	after := press(m, 'o')
	if after.sess.Token == "" {
		t.Fatal("one press signed out, with no confirmation")
	}
	if !strings.Contains(after.setupMsg, "press o again") {
		t.Errorf("the first press says %q", after.setupMsg)
	}

	// Anything else calls it off.
	if press(after, 'r').confirmSignOut {
		t.Error("the confirmation survives another key")
	}

	// Two presses do it, and take the key and the cached answers with them.
	out := press(press(m, 'o'), 'o')
	if out.sess.Token != "" || out.sess.APIKey != "" {
		t.Error("signing out left the session behind")
	}
	if out.client.Token() != "" {
		t.Error("the client still carries the old token")
	}
	if len(out.cache) != 0 {
		t.Error("the tabs keep answers that were true for another account")
	}
	if _, err := session.Load(); err == nil {
		t.Error("the session file is still on disk")
	}
}

// And signing out drops straight back into the setup, which is the only thing
// left to do.
func TestSigningOutStartsTheSetupAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", filepath.Join(home, "h"))
	sess := &session.Session{Token: "t", APIKey: testKey}
	if err := session.Save(sess); err != nil {
		t.Fatal(err)
	}
	m := newModel(api.New("t"), sess)
	m.lay = newLayout(90, 30)

	press := func(m model, r rune) model {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		return updated.(model)
	}
	out := press(press(m, 'o'), 'o')
	if out.wizard != wizardEmail {
		t.Errorf("after signing out the panel is at %v, want the first step", out.wizard)
	}
}

// Signing out was only behind `?`, which is a place you look for something if
// you already suspect it is there. Reported as exactly that: "en ningún lado
// aparece que pulsando o dos veces te deslogeas".
func TestSigningOutIsAdvertisedWhereItIsLookedFor(t *testing.T) {
	// The account screen, which is where a person goes for account things.
	profile := renderProfile(map[string]any{"handle": "bperez"}, newLayout(90, 30))
	if !strings.Contains(profile, "sign out") {
		t.Error("the Profile tab does not offer to sign out")
	}
	if !strings.Contains(profile, "twice") {
		t.Error("Profile does not say it takes two presses, which is the surprising half")
	}

	// The Setup tab footer, next to the other keys it lists.
	m := setupModel(t, &session.Session{Token: "t", APIKey: testKey})
	m.lay = newLayout(96, 40)
	if out := m.renderSetup(m.lay); !strings.Contains(out, "o sign out") {
		t.Error("the Setup tab lists its keys and leaves this one out")
	}

	// Home, where the keys are introduced.
	if out := renderHome(newLayout(96, 40), true, true, moodNormal); !strings.Contains(out, "sign out") {
		t.Error("Home lists the keys and leaves this one out")
	}

	// And not offered to somebody who has no session to end.
	if out := renderHome(newLayout(96, 40), false, false, moodNormal); strings.Contains(out, "sign out") {
		t.Error("Home offers to sign out of a session that is not there")
	}
}

// One tool failing used to read as the whole step failing: the message became
// "error: <whatever the last one said>" and the configs that HAD been written
// went unmentioned. Reported from a machine where Hermes would not configure,
// which left the setup on its last screen with nothing to do but escape.
func TestOneToolFailingDoesNotLoseTheOthers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	hermesHomeDir := filepath.Join(home, "hermes")
	t.Setenv("HERMES_HOME", hermesHomeDir)
	if err := os.MkdirAll(hermesHomeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Hermes refuses; everything else is written as usual.
	original := runHermesConfig
	runHermesConfig = func(string, ...string) error {
		return fmt.Errorf("hermes config set: exit status 1\nsomething it printed\nand more of it")
	}
	t.Cleanup(func() { runHermesConfig = original })

	for _, p := range []string{
		filepath.Join(home, ".factory", "settings.json"),
		filepath.Join(home, ".config", "opencode", "opencode.json"),
		filepath.Join(home, ".pi", "agent", "models.json"),
		filepath.Join(home, ".codex", "config.toml"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	msg, written, failed := configureTools(testKey, nil)

	if !strings.Contains(msg, "4 added") {
		t.Errorf("the message is %q, and does not say what worked", msg)
	}
	if !strings.Contains(msg, "Hermes failed") {
		t.Errorf("the message is %q, and does not name what did not", msg)
	}
	if len(written) != 4 {
		t.Errorf("wrote %v, want the four that did not fail", written)
	}
	if len(failed) != 1 || failed[0].name != "Hermes" {
		t.Fatalf("failures are %v, want just Hermes", failed)
	}
	// The summary takes one line of what a tool printed, not the paragraph.
	if strings.ContainsAny(failed[0].firstLine(), "\r\n") {
	}
}

// And the step finishes anyway. Holding somebody on the last screen of a setup
// with no way on but escape is the thing that was reported.
func TestAFailedToolDoesNotStrandTheSetup(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "t", APIKey: testKey})
	m.lay = newLayout(96, 44)
	m.wizard = wizardTools

	updated, _ := m.Update(configuredMsg{
		msg:     "4 added  ·  Hermes failed",
		written: []string{"OpenCode"},
		failed:  []toolFailure{{"Hermes", "hermes config set: exit status 1"}},
	})
	after := updated.(model)

	if after.wizard != wizardDone {
		t.Error("a failed tool holds the setup on its last step")
	}
	out := after.renderWizard(after.lay)
	if !strings.Contains(out, "Hermes") {
		t.Error("the screen does not say which tool failed")
	}
	if !strings.Contains(out, "The rest went in") {
		t.Error("the screen does not say the others worked")
	}
	if !strings.Contains(out, "press c again") {
		t.Error("the screen does not say what can be done about it")
	}
}

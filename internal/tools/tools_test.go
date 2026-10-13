package tools

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

	catalog "github.com/nxssie/nan-cli/internal/models"
)

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
	if !strings.Contains(content, `experimental_bearer_token = "nan-old"`) {
		t.Error("a repair that should touch one line rewrote the member's key")
	}
	// Another provider's wire_api is that provider's business.
	openai := content[strings.Index(content, "[model_providers.openai]"):strings.Index(content, "[model_providers.nan]")] //nolint:gocritic // both headers are written by this test
	if !strings.Contains(openai, `wire_api = "chat"`) {
		t.Error("a wire_api outside our own section was rewritten")
	}
	nan := content[strings.Index(content, "[model_providers.nan]"):] //nolint:gocritic // written by this test
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
// With the sub-agent namespace or web_search in the request, /responses
// answers qwen3.8-flash and mimo with a lone response.completed, and Codex
// ends the turn empty. The profiles are what keeps those tools out.
func TestCodexProfilesLeaveOutTheToolsTheClusterCannotStream(t *testing.T) {
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
		body := string(data)
		if !strings.Contains(body, `web_search = "disabled"`) {
			t.Errorf("%s: the profile still sends web_search:\n%s", m.ID, body)
		}
		if !strings.Contains(body, "[features]\nmulti_agent = false") {
			t.Errorf("%s: the profile still sends the sub-agent namespace:\n%s", m.ID, body)
		}
		// Before the first header, or TOML puts it inside [features].
		if strings.Index(body, "web_search") > strings.Index(body, "[features]") {
			t.Errorf("%s: web_search landed inside [features], where Codex does not read it:\n%s", m.ID, body)
		}
	}
}

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
	for _, tool := range Registry() {
		if tool.Open == "" || tool.NextStep == "" {
			t.Errorf("%s is configured and nothing says how to use it", tool.Name)
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
			t.Errorf("%s: written without the API key", name)
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
	runHermesConfig = func(_ string, args ...string) error {
		calls = append(calls, args)
		return nil
	}

	// And `config get`, which the writer uses to confirm Hermes resolved the
	// reference. The fake resolves it the same way Hermes does - out of the
	// .env this CLI just wrote - so the check is exercised rather than
	// stubbed past.
	originalRead := readHermesConfig
	readHermesConfig = func(home string, _ ...string) (string, error) {
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
	readHermesConfig = func(_ string, _ ...string) (string, error) {
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
	readHermesConfig = func(_ string, _ ...string) (string, error) {
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
	if len(failed) != 1 || failed[0].Tool != "Hermes" {
		t.Fatalf("failures are %v, want just Hermes", failed)
	}
	// The summary takes one line of what a tool printed, not the paragraph.
	if strings.ContainsAny(failed[0].FirstLine(), "\r\n") {
		t.Errorf("the summary carries a line break: %q", failed[0].FirstLine())
	}
}

// The tools' configs are theirs, not ours: by the time `nan` writes a key into
// one it usually exists already, created by the tool itself and commonly 0644.
// os.WriteFile does not change the mode of a file it did not create, so the
// key went into a world-readable file and the 0o600 in the call did nothing.
func TestWritingAKeyTightensAConfigThatWasWideOpen(t *testing.T) {
	home := toolHome(t)
	for _, path := range toolPaths(home) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// As the tool would have left it.
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on a clean run: %v", failed)
	}
	for name, path := range toolPaths(home) {
		if !strings.Contains(readFile(t, path), testKey) {
			t.Errorf("%s was not configured at all", name)
			continue
		}
		assertNotWorldReadable(t, path)
	}
}

// The tools that are already pointing at the cluster are the ones that have
// held a key the longest, and the writers leave them alone: there is nothing
// to change. Their mode is still whatever it was when the key went in, which
// for a config the tool created itself is world-readable.
func TestConfiguringTightensAToolThatNeedsNothingWritten(t *testing.T) {
	home := toolHome(t)
	paths := toolPaths(home)
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on a clean run: %v", failed)
	}
	// As an older version of this CLI would have left them.
	for _, path := range paths {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A second run has nothing to write, and has to fix them anyway.
	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on the second run: %v", failed)
	}
	for name, path := range paths {
		if !strings.Contains(readFile(t, path), testKey) {
			t.Errorf("%s lost its key on the second run", name)
		}
		assertNotWorldReadable(t, path)
	}
}

// A config this CLI cannot parse is the one case where it must not write: the
// readers used to discard the unmarshal error, read the file as nothing, and
// write it again from scratch - taking every other provider in it, and their
// keys, with them.
func TestAConfigThatDoesNotParseIsLeftExactlyAsItIs(t *testing.T) {
	const theirs = `{"provider": {"anthropic": {"apiKey": "theirs"}},,,`

	for _, c := range []struct {
		name  string
		file  string
		write func(path, key string) error
	}{
		{"Factory AI", "settings.json", writeFactoryConfig},
		{"OpenCode", "opencode.json", writeOpencodeConfig},
		{"Pi", "models.json", writePiConfig},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := tempConfig(t, c.file)
			if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
				t.Fatal(err)
			}

			err := c.write(path, testKey)
			if err == nil {
				t.Fatal("a config that does not parse was written over without a word")
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("the error carries the key: %v", err)
			}
			if got := readFile(t, path); got != theirs {
				t.Errorf("the file was rewritten:\n%s", got)
			}
		})
	}
}

// Signing out deletes session.json, which holds the token and the key. It used
// to stop there, and the key was still in every tool configured from the
// panel - working, billing, and out of reach of anyone who thought signing out
// was the end of it.
func TestSigningOutTakesTheKeyOutOfTheTools(t *testing.T) {
	home := toolHome(t)
	for _, path := range toolPaths(home) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed on a clean run: %v", failed)
	}

	removed, failed := RemoveNanFromTools()
	if len(failed) != 0 {
		t.Fatalf("removal failed: %v", failed)
	}
	if len(removed) == 0 {
		t.Fatal("removal found nothing to take out, having just written five")
	}
	for name, path := range toolPaths(home) {
		if strings.Contains(readFile(t, path), testKey) {
			t.Errorf("%s still carries the key after signing out", name)
		}
	}
	if strings.Contains(readFile(t, hermesEnvPath(filepath.Join(home, "hermes"))), testKey) {
		t.Error("Hermes still carries the key after signing out")
	}
}

// The reason a failed `hermes config set` prints. The value is the key.
func TestHermesFailuresDoNotPrintTheValue(t *testing.T) {
	err := hermesConfigError([]string{"set", "model.api_key", testKey}, "unknown key\n")
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("the key is in the error the panel renders: %v", err)
	}
	for _, want := range []string{"set", "model.api_key", "unknown key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error no longer says %q: %v", want, err)
		}
	}
}

// ── the shared harness ───────────────────────────────────────────────────────

// A home directory of this test's own, Hermes included, so nothing here
// reaches the machine it is running on.
func toolHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows
	hermesDir := filepath.Join(home, "hermes")
	t.Setenv("HERMES_HOME", hermesDir)
	if err := os.MkdirAll(hermesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	recordHermes(t) // no hermes process is spawned, here or anywhere
	return home
}

func toolPaths(home string) map[string]string {
	return map[string]string{
		"Factory AI": filepath.Join(home, ".factory", "settings.json"),
		"OpenCode":   filepath.Join(home, ".config", "opencode", "opencode.json"),
		"Pi":         filepath.Join(home, ".pi", "agent", "models.json"),
		"Codex":      filepath.Join(home, ".codex", "config.toml"),
	}
}

// ── the old names, over the new API ─────────────────────────────────────────
//
// These tests were written against the dashboard's own functions before the
// writers moved here. Three thin adapters keep each test reading as it did,
// so what they pin is visibly the same behaviour.

func isNaNConfigured(name, path string) bool {
	for _, t := range Registry() {
		if t.Name == name {
			return t.configured(path)
		}
	}
	return false
}

func configureTools(apiKey string, ticked map[string]bool) (string, []string, []Failure) {
	r := Apply(apiKey, ticked)
	return r.Summary(), r.Written, r.Failed
}

func RemoveNanFromTools() (removed []string, failed []string) {
	removed, failures := RemoveAll()
	for _, f := range failures {
		failed = append(failed, f.Tool+" ("+f.Err.Error()+")")
	}
	return removed, failed
}

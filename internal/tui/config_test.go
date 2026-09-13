package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	catalog "github.com/nxssie/nan-cli/internal/models"
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
	if len(written) != len(catalog.All) {
		t.Errorf("wrote %d models, the cluster serves %d", len(written), len(catalog.All))
	}

	for _, m := range catalog.All {
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
	for _, m := range catalog.All {
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

func TestCodexConfigSpeaksTheStreamingEndpoint(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// /responses answers in one terminal event on this cluster, so with
	// wire_api = "responses" the whole reply lands at once at the end.
	if !strings.Contains(content, `wire_api = "chat"`) {
		t.Error(`wire_api is not "chat", so Codex will not stream`)
	}
	model, _ := catalog.Get(catalog.Coding)
	if !strings.Contains(content, `model = "`+model.ID+`"`) {
		t.Errorf("the default model is not %s", model.ID)
	}
	if strings.Contains(content, "model_context_window = 131072") {
		t.Error("still declaring a window no model on the cluster has")
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

func TestFactoryConfigMarksWhatCannotSeeImages(t *testing.T) {
	path := tempConfig(t, "settings.json")
	if err := writeFactoryConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, path)
	custom := cfg["customModels"].([]any)
	if len(custom) != len(catalog.All) {
		t.Errorf("wrote %d models, the cluster serves %d", len(custom), len(catalog.All))
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

	for _, m := range catalog.All {
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

	// A tool counts as installed if its binary is on PATH *or* its config path
	// exists, so an empty file each is enough to make all four visible.
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

	if msg := configureTools(testKey, nil); !strings.Contains(msg, "4 added") {
		t.Fatalf("configureTools said %q, want the four tools written", msg)
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
	if msg := configureTools(testKey, map[string]bool{"Pi": false}); !strings.Contains(msg, "1 removed") {
		t.Errorf("configureTools said %q, want Pi removed", msg)
	}
	if isNaNConfigured("Pi", paths["Pi"]) {
		t.Error("Pi is still configured after being unticked")
	}
	if !isNaNConfigured("OpenCode", paths["OpenCode"]) {
		t.Error("unticking Pi took OpenCode with it")
	}
}

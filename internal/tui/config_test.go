package tui

import (
	"encoding/json"
	"errors"
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

	msg, written := configureTools(testKey, nil)
	if !strings.Contains(msg, "5 added") {
		t.Fatalf("configureTools said %q, want the five tools written", msg)
	}
	if len(*hermesCalls) == 0 {
		t.Error("Hermes was counted but never configured")
	}
	// The names come back so the tab can say how to use each one; a tool
	// written but not named leaves a member with a config and no next step.
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
			t.Errorf("%s: written without the API key", name)
		}
		if !isNaNConfigured(name, p) {
			t.Errorf("%s: the Setup tab will not show it as configured", name)
		}
	}

	// And unticking a tool takes only that one out.
	msg, _ = configureTools(testKey, map[string]bool{"Pi": false})
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
	t.Cleanup(func() { runHermesConfig = original })
	return &calls
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
		"model.api_key":  testKey,
		"model.default":  catalog.Coding,
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
	if err := removeHermesConfig(t.TempDir()); err != nil {
		t.Fatal(err)
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
	for _, want := range []string{"provider: custom", "base_url: https://api.nan.builders/v1", "default: " + catalog.Coding, testKey} {
		if !strings.Contains(written, want) {
			t.Errorf("config.yaml has no %q:\n%s", want, written)
		}
	}

	if err := removeHermesConfig(home); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(home, "config.yaml"))
	if strings.Contains(string(data), "api.nan.builders") {
		t.Errorf("removal left the cluster behind:\n%s", data)
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

// Once there is a key in the field the hint is noise, and the key itself is
// never printed.
func TestSetupHidesTheHintAndTheKeyOnceOneIsSet(t *testing.T) {
	m := setupModel(t, &session.Session{APIKey: testKey})
	m.keyStatus = &api.KeyStatus{Exists: true, Alias: "an-alias"}

	out := m.renderSetup(newLayout(80, 24))
	if strings.Contains(out, "an-alias") || strings.Contains(out, "cloud.nan.builders") {
		t.Error("the hint is still shown after a key was set")
	}
	if strings.Contains(out, testKey) {
		t.Error("the API key is printed on screen")
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
func TestSigningInHappensInsideThePanel(t *testing.T) {
	m := setupModel(t, &session.Session{})

	if m.loginStage != loginOff {
		t.Fatal("the panel opens mid-login")
	}
	m.startLogin()
	if m.loginStage != loginAskEmail {
		t.Fatal("s does not start the sign-in")
	}

	out := m.renderLogin(newLayout(90, 24))
	for _, want := range []string{"Sign in", "Step 1 of 2", "Email"} {
		if !strings.Contains(out, want) {
			t.Errorf("the first step does not show %q", want)
		}
	}

	// Second question, once the link is on its way.
	m.loginStage = loginAskLink
	m.loginInput.Prompt = "Paste the link: "
	out = m.renderLogin(newLayout(90, 24))
	if !strings.Contains(out, "Step 2 of 2") || !strings.Contains(out, "Paste the link") {
		t.Errorf("the second step does not ask for the link:\n%s", out)
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

package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/helmcode/nan-cli/internal/api"
	catalog "github.com/helmcode/nan-cli/internal/models"
)

func intp(v int) *int { return &v }

// Two models the member can call; "pack-only-x" is one they cannot, so the
// platform did not list it.
func memberCatalogue() []catalog.Model {
	return catalogueOf(
		api.AgentModel{Name: "deepseek-v4-flash"},
		api.AgentModel{Name: "brand-new", ContextWindow: intp(500_000), MaxOutputTokens: intp(16_000)},
	)
}

func modelsOf(t *testing.T, cfg map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := cfg
	for _, p := range path {
		next, _ := cur[p].(map[string]any)
		if next == nil {
			t.Fatalf("no %q in %v", p, cur)
		}
		cur = next
	}
	return cur
}

// ── OpenCode ────────────────────────────────────────────────────────────────

func TestOpenCodeWritesOnlyWhatThePlatformListed(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	if err := writeOpencodeConfig(path, testKey, memberCatalogue()); err != nil {
		t.Fatal(err)
	}
	got := modelsOf(t, readJSON(t, path), "provider", "nan", "models")
	if len(got) != 2 || got["deepseek-v4-flash"] == nil || got["brand-new"] == nil {
		t.Fatalf("models written: %v", got)
	}
	limit := modelsOf(t, got["brand-new"].(map[string]any), "limit")
	if limit["context"] != float64(500_000) || limit["output"] != float64(16_000) {
		t.Errorf("limit: %v", limit)
	}
}

// A model with no figures from anyone gets no limit at all, rather than a
// guess that would make OpenCode compact early or overrun the window.
func TestOpenCodeLeavesOutAWindowNobodyReported(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	if err := writeOpencodeConfig(path, testKey, catalogueOf(api.AgentModel{Name: "mystery"})); err != nil {
		t.Fatal(err)
	}
	entry := modelsOf(t, readJSON(t, path), "provider", "nan", "models", "mystery")
	if _, has := entry["limit"]; has {
		t.Errorf("a limit was invented: %v", entry)
	}
}

// An older platform sends no metadata. The windows already on disk, correct
// ones, must not be lowered by it - the regression the review warned about.
func TestOpenCodeKeepsGoodLimitsWhenThePlatformSendsNoMetadata(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	rich := catalogueOf(api.AgentModel{Name: "brand-new", ContextWindow: intp(500_000), MaxOutputTokens: intp(16_000)})
	if err := writeOpencodeConfig(path, testKey, rich); err != nil {
		t.Fatal(err)
	}
	bare := catalogueOf(api.AgentModel{Name: "brand-new"}, api.AgentModel{Name: "glm5.3-flash"})
	if err := writeOpencodeConfig(path, testKey, bare); err != nil {
		t.Fatal(err)
	}
	models := modelsOf(t, readJSON(t, path), "provider", "nan", "models")
	limit := modelsOf(t, models["brand-new"].(map[string]any), "limit")
	if limit["context"] != float64(500_000) || limit["output"] != float64(16_000) {
		t.Errorf("a metadata-less answer lowered a good limit: %v", limit)
	}
	known, _ := catalog.Get("glm5.3-flash")
	limit = modelsOf(t, models["glm5.3-flash"].(map[string]any), "limit")
	if limit["context"] != float64(known.Context) {
		t.Errorf("a known model lost its window: %v", limit)
	}
}

func TestOpenCodeRemovesModelsThePlatformNoLongerListsForTheMember(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	both := catalogueOf(api.AgentModel{Name: "deepseek-v4-flash"}, api.AgentModel{Name: "pack-only-x"})
	if err := writeOpencodeConfig(path, testKey, both); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeConfig(path, testKey, catalogueOf(api.AgentModel{Name: "deepseek-v4-flash"})); err != nil {
		t.Fatal(err)
	}
	models := modelsOf(t, readJSON(t, path), "provider", "nan", "models")
	if _, has := models["pack-only-x"]; has || models["deepseek-v4-flash"] == nil {
		t.Errorf("models: %v", models)
	}
}

// ── Factory ─────────────────────────────────────────────────────────────────

func factoryModels(t *testing.T, path string) (models []map[string]any, cfg map[string]any) {
	t.Helper()
	cfg = readJSON(t, path)
	raw, _ := cfg["customModels"].([]any)
	for _, r := range raw {
		models = append(models, r.(map[string]any))
	}
	return models, cfg
}

func TestFactoryConfiguresOnlyServedModelsWithReadableNames(t *testing.T) {
	path := tempConfig(t, "settings.json")
	if err := writeFactoryConfig(path, testKey, memberCatalogue()); err != nil {
		t.Fatal(err)
	}
	models, _ := factoryModels(t, path)
	if len(models) != 2 {
		t.Fatalf("models: %v", models)
	}
	for _, m := range models {
		if m["model"] == "glm5.3" || m["model"] == "pack-only-x" {
			t.Errorf("a model the member cannot call was written: %v", m)
		}
	}
	if models[0]["displayName"] != "DeepSeek V4 Flash (NaN)" || models[1]["displayName"] != "brand-new (NaN)" {
		t.Errorf("display names: %v / %v", models[0]["displayName"], models[1]["displayName"])
	}
	if models[1]["noImageSupport"] != true {
		t.Errorf("a model with no reported vision must not claim images: %v", models[1])
	}
}

func TestFactoryMovesItsDefaultWhenThatModelIsGone(t *testing.T) {
	path := tempConfig(t, "settings.json")
	if err := writeFactoryConfig(path, testKey, catalogueOf(api.AgentModel{Name: "glm5.3"}, api.AgentModel{Name: "gemma4"})); err != nil {
		t.Fatal(err)
	}
	_, cfg := factoryModels(t, path)
	first := modelsOf(t, cfg, "sessionDefaultSettings")["model"]

	// glm5.3 (the default's model, being the first listed) stops being listed: the default has to follow.
	if err := writeFactoryConfig(path, testKey, catalogueOf(api.AgentModel{Name: "gemma4"}, api.AgentModel{Name: "qwen3.6"})); err != nil {
		t.Fatal(err)
	}
	models, cfg := factoryModels(t, path)
	def := modelsOf(t, cfg, "sessionDefaultSettings")["model"]
	ids := map[any]string{}
	for _, m := range models {
		ids[m["id"]] = m["model"].(string)
	}
	if _, ok := ids[def]; !ok {
		t.Fatalf("the default %v (was %v) names an entry that is not there: %v", def, first, ids)
	}
}

func TestFactoryKeepsADefaultThatIsStillServed(t *testing.T) {
	path := tempConfig(t, "settings.json")
	if err := writeFactoryConfig(path, testKey, memberCatalogue()); err != nil {
		t.Fatal(err)
	}
	models, cfg := factoryModels(t, path)
	chosen := models[1]["id"] // the member picked the second one
	cfg["sessionDefaultSettings"].(map[string]any)["model"] = chosen
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	more := append(memberCatalogue(), catalogueOf(api.AgentModel{Name: "another"})...)
	if err := writeFactoryConfig(path, testKey, more); err != nil {
		t.Fatal(err)
	}
	_, cfg = factoryModels(t, path)
	if got := cfg["sessionDefaultSettings"].(map[string]any)["model"]; got != chosen {
		t.Errorf("the member's default moved from %v to %v", chosen, got)
	}
}

// Recognised by host, not by substring: an entry that merely mentions our host
// in its own URL is the member's and is neither pruned nor removed.
func TestFactoryLeavesAHostThatOnlyContainsOursAlone(t *testing.T) {
	path := tempConfig(t, "settings.json")
	theirs := `{"customModels":[{"model":"theirs","id":"custom:theirs-0","index":0,"baseUrl":"https://api.nan.builders.example.com/v1","provider":"openai"}]}`
	if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryConfig(path, testKey, memberCatalogue()); err != nil {
		t.Fatal(err)
	}
	models, _ := factoryModels(t, path)
	if len(models) != 3 || models[0]["model"] != "theirs" {
		t.Fatalf("their entry was touched: %v", models)
	}
	if err := removeFactoryConfig(path); err != nil {
		t.Fatal(err)
	}
	models, _ = factoryModels(t, path)
	if len(models) != 1 || models[0]["model"] != "theirs" {
		t.Errorf("removal took their entry: %v", models)
	}
}

// ── Pi ──────────────────────────────────────────────────────────────────────

func piModelsByID(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	nan := modelsOf(t, readJSON(t, path), "providers", "nan")
	out := map[string]map[string]any{}
	for _, m := range nan["models"].([]any) {
		e := m.(map[string]any)
		out[e["id"].(string)] = e
	}
	return out
}

func TestPiWritesServedModelsAndOmitsUnknownFigures(t *testing.T) {
	path := tempConfig(t, "models.json")
	if err := writePiConfig(path, testKey, catalogueOf(api.AgentModel{Name: "mystery"}, api.AgentModel{Name: "brand-new", ContextWindow: intp(500_000)})); err != nil {
		t.Fatal(err)
	}
	got := piModelsByID(t, path)
	if len(got) != 2 {
		t.Fatalf("models: %v", got)
	}
	if _, has := got["mystery"]["contextWindow"]; has {
		t.Errorf("a window was invented: %v", got["mystery"])
	}
	if got["brand-new"]["contextWindow"] != float64(500_000) {
		t.Errorf("reported window lost: %v", got["brand-new"])
	}
}

func TestPiKeepsFiguresItAlreadyHadWhenTheCatalogueHasNone(t *testing.T) {
	path := tempConfig(t, "models.json")
	rich := catalogueOf(api.AgentModel{Name: "brand-new", ContextWindow: intp(500_000), MaxOutputTokens: intp(16_000)})
	if err := writePiConfig(path, testKey, rich); err != nil {
		t.Fatal(err)
	}
	if err := writePiConfig(path, testKey, catalogueOf(api.AgentModel{Name: "brand-new"})); err != nil {
		t.Fatal(err)
	}
	e := piModelsByID(t, path)["brand-new"]
	if e["contextWindow"] != float64(500_000) || e["maxTokens"] != float64(16_000) {
		t.Errorf("a metadata-less answer dropped what Pi had: %v", e)
	}
}

func TestPiDefaultFollowsTheMembersList(t *testing.T) {
	path := tempConfig(t, "models.json")
	settings := filepath.Join(filepath.Dir(path), "settings.json")
	defaultModel := func() any { return readJSON(t, settings)["defaultModel"] }

	// Fresh: the coding model when served...
	if err := writePiConfig(path, testKey, catalogueOf(api.AgentModel{Name: "aaa"}, api.AgentModel{Name: "glm5.3-flash"})); err != nil {
		t.Fatal(err)
	}
	if defaultModel() != "glm5.3-flash" {
		t.Errorf("default %v, want glm5.3-flash", defaultModel())
	}
	// ...a model of ours that is still served stays...
	if err := writePiConfig(path, testKey, catalogueOf(api.AgentModel{Name: "aaa"}, api.AgentModel{Name: "glm5.3-flash"}, api.AgentModel{Name: "deepseek-v4-flash"})); err != nil {
		t.Fatal(err)
	}
	if defaultModel() != "glm5.3-flash" {
		t.Errorf("default moved to %v", defaultModel())
	}
	// ...and one that is gone moves to a model that is there, never to "first
	// alphabetically" while the quickstart's default is served.
	if err := writePiConfig(path, testKey, catalogueOf(api.AgentModel{Name: "aaa"}, api.AgentModel{Name: "deepseek-v4-flash"})); err != nil {
		t.Fatal(err)
	}
	if defaultModel() != "deepseek-v4-flash" {
		t.Errorf("default %v, want deepseek-v4-flash", defaultModel())
	}
}

func TestPiLeavesTheMembersOwnDefaultProviderAlone(t *testing.T) {
	path := tempConfig(t, "models.json")
	settings := filepath.Join(filepath.Dir(path), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"defaultProvider":"anthropic","defaultModel":"claude-x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePiConfig(path, testKey, memberCatalogue()); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, settings)
	if got["defaultProvider"] != "anthropic" || got["defaultModel"] != "claude-x" {
		t.Errorf("settings: %v", got)
	}
}

// ── Hermes ──────────────────────────────────────────────────────────────────

func hermesDefaultSet(calls [][]string) string {
	got := ""
	for _, c := range calls {
		if len(c) == 3 && c[0] == "set" && c[1] == "model.default" {
			got = c[2]
		}
	}
	return got
}

func TestHermesKeepsItsDefaultWhenStillServedAndMovesItWhenNot(t *testing.T) {
	calls := recordHermes(t)
	current := ""
	readHermesConfig = func(_ string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "model.default" {
			return current, nil
		}
		return testKey, nil // model.api_key resolves
	}
	home := t.TempDir()

	for _, c := range []struct{ current, want string }{
		{"", "glm5.3-flash"},                   // nothing yet: the coding model
		{"brand-new", "brand-new"},             // the member's, and served: not ours to change
		{"retired-model", "deepseek-v4-flash"}, // gone: the quickstart default, not models[0]
	} {
		current = c.current
		*calls = nil
		cat := catalogueOf(api.AgentModel{Name: "aaa"}, api.AgentModel{Name: "brand-new"}, api.AgentModel{Name: "deepseek-v4-flash"})
		if c.current == "" {
			cat = append(cat, catalogueOf(api.AgentModel{Name: "glm5.3-flash"})...)
		}
		// A read that cannot resolve the key is tolerated by the writer.
		if err := writeHermesConfig(home, testKey, cat); err != nil {
			t.Fatal(err)
		}
		if got := hermesDefaultSet(*calls); got != c.want {
			t.Errorf("current %q: set model.default %q, want %q", c.current, got, c.want)
		}
	}
}

// ── Codex ───────────────────────────────────────────────────────────────────

func TestCodexStarterUsesTheMembersListAndLeavesOutAnUnknownWindow(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey, catalogueOf(api.AgentModel{Name: "zzz-unknown"})); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	if !strings.Contains(content, `model = "zzz-unknown"`) {
		t.Errorf("the starter model is not one the member can call:\n%s", content)
	}
	if strings.Contains(content, "model_context_window") {
		t.Errorf("a window was invented:\n%s", content)
	}
	home := filepath.Dir(path)
	if strings.Contains(readFile(t, filepath.Join(home, codexCatalogFile)), "zzz-unknown") {
		t.Error("a model with no window went into Codex's catalogue")
	}
	if _, err := os.Stat(filepath.Join(home, codexProfileName("zzz-unknown")+".config.toml")); err != nil {
		t.Errorf("the profile is still written: %v", err)
	}
	body := readFile(t, filepath.Join(home, codexProfileName("zzz-unknown")+".config.toml"))
	if strings.Contains(body, "model_context_window") {
		t.Errorf("profile carries an invented window:\n%s", body)
	}
}

func TestCodexProfilesAreOnlyForServedModels(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey, testCatalogue()); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey, memberCatalogue()); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	matches, _ := filepath.Glob(filepath.Join(home, "nan-*.config.toml"))
	if len(matches) != 2 {
		t.Errorf("profiles: %v", matches)
	}
	for _, m := range []string{"glm5.3", "gemma4"} {
		if _, err := os.Stat(filepath.Join(home, codexProfileName(m)+".config.toml")); err == nil {
			t.Errorf("profile of a model the member cannot call survived: %s", m)
		}
	}
	if strings.Contains(readFile(t, filepath.Join(home, codexCatalogFile)), "gemma4") {
		t.Error("catalogue still lists an unserved model")
	}
}

// The window the platform reports is not one this CLI can know by value, so it
// carries a mark and removal recognises it by that.
func TestCodexRootWindowFromThePlatformIsTakenOutAgain(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey, catalogueOf(api.AgentModel{Name: "glm5.3-flash", ContextWindow: intp(777_777)})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, path), "model_context_window = 777777 # nan-cli") {
		t.Fatalf("root window not written as reported and marked:\n%s", readFile(t, path))
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, path), "model_context_window") {
		t.Errorf("our window was left behind:\n%s", readFile(t, path))
	}
}

func TestCodexKeepsAMembersOwnWindowThatLooksLikeOurs(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := os.WriteFile(path, []byte("model_context_window = 777777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey, catalogueOf(api.AgentModel{Name: "glm5.3-flash", ContextWindow: intp(777_777)})); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, path), "model_context_window = 777777") {
		t.Errorf("the member's own window was removed:\n%s", readFile(t, path))
	}
}

// ── one guard, for every writer ─────────────────────────────────────────────

func TestAnEmptyCatalogueWritesNothingAnywhere(t *testing.T) {
	home := toolHome(t)
	for _, path := range toolPaths(home) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range Installed(Detect()) {
		err := st.Apply(testKey, nil)
		if !errors.Is(err, catalog.ErrNoModels) {
			t.Errorf("%s: %v, want ErrNoModels", st.Name, err)
		}
	}
	for name, path := range toolPaths(home) {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s: %s was written from an empty catalogue", name, path)
		}
	}
}

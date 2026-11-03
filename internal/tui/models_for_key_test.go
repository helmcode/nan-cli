package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

var errOffline = errors.New("offline")

func TestMain(m *testing.M) {
	listModels = func(string) ([]string, error) { return nil, errOffline }
	os.Exit(m.Run())
}

func stubListModels(t *testing.T, ids []string, err error) {
	t.Helper()
	previous := listModels
	listModels = func(string) ([]string, error) { return ids, err }
	t.Cleanup(func() { listModels = previous })
}

func modelIDs(models []catalog.Model) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestModelsForKeyKeepsOnlyTheChatModelsTheKeyServes(t *testing.T) {
	stubListModels(t, []string{"glm5.3-flash", "deepseek-v4-flash", "flux-2-klein", "minimax-h3"}, nil)

	got := modelIDs(modelsForKey(testKey))
	want := []string{"deepseek-v4-flash", "glm5.3-flash"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("modelsForKey = %v, want %v", got, want)
	}
}

func TestModelsForKeyFallsBackToTheCatalog(t *testing.T) {
	full := modelIDs(catalog.ChatModels())
	for _, c := range []struct {
		name string
		ids  []string
		err  error
	}{
		{"the request fails", nil, errOffline},
		{"the key serves no chat model the catalog knows", []string{"flux-2-klein"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubListModels(t, c.ids, c.err)
			if got := modelIDs(modelsForKey(testKey)); !reflect.DeepEqual(got, full) {
				t.Errorf("modelsForKey = %v, want the whole catalog %v", got, full)
			}
		})
	}
}

func TestSetupWritesOnlyTheModelsTheKeyServes(t *testing.T) {
	home := toolHome(t)
	paths := toolPaths(home)
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stubListModels(t, []string{"deepseek-v4-flash", "glm5.3-flash"}, nil)

	if _, _, failed := configureTools(testKey, nil); len(failed) != 0 {
		t.Fatalf("tools failed: %v", failed)
	}

	want := map[string]bool{"deepseek-v4-flash": true, "glm5.3-flash": true}

	opencode := readJSON(t, paths["OpenCode"])
	nan := opencode["provider"].(map[string]any)["nan"].(map[string]any)
	if got := keysOf(nan["models"].(map[string]any)); !reflect.DeepEqual(got, want) {
		t.Errorf("OpenCode models = %v, want %v", got, want)
	}

	pi := readJSON(t, paths["Pi"])
	piModels := map[string]bool{}
	for _, m := range pi["providers"].(map[string]any)["nan"].(map[string]any)["models"].([]any) {
		piModels[m.(map[string]any)["id"].(string)] = true
	}
	if !reflect.DeepEqual(piModels, want) {
		t.Errorf("Pi models = %v, want %v", piModels, want)
	}

	factory := readJSON(t, paths["Factory AI"])
	factoryModels := map[string]bool{}
	for _, m := range factory["customModels"].([]any) {
		factoryModels[m.(map[string]any)["model"].(string)] = true
	}
	if !reflect.DeepEqual(factoryModels, want) {
		t.Errorf("Factory models = %v, want %v", factoryModels, want)
	}

	codexHome := filepath.Dir(paths["Codex"])
	for _, m := range catalog.ChatModels() {
		profile := filepath.Join(codexHome, codexProfileName(m.ID)+".config.toml")
		_, err := os.Stat(profile)
		if want[m.ID] && err != nil {
			t.Errorf("Codex profile for %s is missing: %v", m.ID, err)
		}
		if !want[m.ID] && err == nil {
			t.Errorf("Codex has a profile for %s, which the key cannot call", m.ID)
		}
	}
}

func TestOpencodeDropsCatalogModelsTheKeyNoLongerServes(t *testing.T) {
	path := tempConfig(t, "opencode.json")
	if err := writeOpencodeConfig(path, testKey, catalog.ChatModels()); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, path)
	nan := cfg["provider"].(map[string]any)["nan"].(map[string]any)
	nan["models"].(map[string]any)["their-own-model"] = map[string]any{"name": "Theirs"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	served := catalog.ChatModelsIn([]string{"deepseek-v4-flash", "glm5.3-flash"})
	if err := writeOpencodeConfig(path, testKey, served); err != nil {
		t.Fatal(err)
	}

	nan = readJSON(t, path)["provider"].(map[string]any)["nan"].(map[string]any)
	got := keysOf(nan["models"].(map[string]any))
	want := map[string]bool{"deepseek-v4-flash": true, "glm5.3-flash": true, "their-own-model": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OpenCode models = %v, want %v", got, want)
	}
}

func keysOf(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

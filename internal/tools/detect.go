package tools

import (
	"encoding/json"
	"os"
	"strings"
)

// nanHost is how a config is recognised as pointing at NaN.
const nanHost = "api.nan.builders"

func factoryConfigured(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return false
	}
	var cfg struct {
		CustomModels []map[string]any `json:"customModels"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	for _, m := range cfg.CustomModels {
		if base, _ := m["baseUrl"].(string); strings.Contains(base, nanHost) {
			return true
		}
	}
	return false
}

func opencodeConfigured(path string) bool {
	cfg := readJSONMap(path)
	providers, _ := cfg["provider"].(map[string]any)
	nan, _ := providers["nan"].(map[string]any)
	opts, _ := nan["options"].(map[string]any)
	base, _ := opts["baseURL"].(string)
	return strings.Contains(base, nanHost)
}

func piConfigured(path string) bool {
	cfg := readJSONMap(path)
	providers, _ := cfg["providers"].(map[string]any)
	nan, _ := providers["nan"].(map[string]any)
	base, _ := nan["baseUrl"].(string)
	return strings.Contains(base, nanHost)
}

// mentionsNaN is the check for the configs that are not JSON: Codex's TOML,
// edited as text to keep its comments, and Hermes' YAML, written by Hermes.
func mentionsNaN(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a config file at a path this program chose
	return err == nil && strings.Contains(string(data), nanHost)
}

func readJSONMap(path string) map[string]any {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	return cfg
}

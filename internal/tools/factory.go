package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

// factoryCustomID is the id Factory knows one of our models by, built the way
// Factory builds its own: the display name with dashes, and the position.
func factoryCustomID(displayName string, index int) string {
	return fmt.Sprintf("custom:%s-%d", strings.ReplaceAll(displayName, " ", "-"), index)
}

func writeFactoryConfig(cfgPath, apiKey string) error {
	// A map, so unknown top-level fields (logoAnimation, etc.) survive.
	cfg, err := readJSONConfig(cfgPath)
	if err != nil {
		return err
	}

	// Extract existing customModels
	var models []map[string]any
	if raw, ok := cfg["customModels"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				models = append(models, m)
			}
		}
	}

	// Index NaN model IDs already present, dropping the ones the cluster no
	// longer serves.
	existingIDs := map[string]bool{}
	changed := false
	kept := models[:0]
	nextIndex := 0
	for _, m := range models {
		if base, _ := m["baseUrl"].(string); strings.Contains(base, "api.nan.builders") {
			id, _ := m["model"].(string)
			if catalog.IsRetired(id) {
				changed = true
				continue
			}
			if id != "" {
				existingIDs[id] = true
			}
		}
		// JSON numbers come back as float64. The next index goes past the
		// highest one, not at len(models): with an entry taken out, len lands
		// on an index someone already holds.
		if i, ok := m["index"].(float64); ok && int(i) >= nextIndex {
			nextIndex = int(i) + 1
		}
		kept = append(kept, m)
	}
	models = kept
	if len(models) > nextIndex {
		nextIndex = len(models)
	}

	// Append only missing models
	for _, nm := range catalog.ChatModels() {
		if !existingIDs[nm.ID] {
			idx := nextIndex
			nextIndex++
			display := nm.Name + " (NaN)"
			models = append(models, map[string]any{
				"model":          nm.ID,
				"id":             factoryCustomID(display, idx),
				"index":          idx,
				"baseUrl":        "https://api.nan.builders/v1",
				"apiKey":         apiKey,
				"displayName":    display,
				"noImageSupport": !nm.Accepts(catalog.InputImage),
				"provider":       "openai",
			})
			changed = true
		}
	}
	if !changed {
		return nil
	}
	cfg["customModels"] = models

	// Leave it pointing at the model the quickstart starts everyone with. It
	// used to be qwen3.6, which the docs now call the previous generation.
	if _, ok := cfg["sessionDefaultSettings"]; !ok {
		for _, m := range models {
			if id, _ := m["model"].(string); id == catalog.Default {
				cfg["sessionDefaultSettings"] = map[string]any{
					"model":           m["id"],
					"reasoningEffort": "none",
					"autonomyMode":    "normal",
				}
				break
			}
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(cfgPath, data)
}

func removeFactoryConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	raw, _ := cfg["customModels"].([]any)
	var filtered []map[string]any
	var removedIDs []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if base, _ := m["baseUrl"].(string); strings.Contains(base, "api.nan.builders") {
			if id, _ := m["id"].(string); id != "" {
				removedIDs = append(removedIDs, id)
			}
		} else {
			filtered = append(filtered, m)
		}
	}
	if len(removedIDs) == 0 {
		return nil
	}
	cfg["customModels"] = filtered

	// Clear sessionDefaultSettings.model if it pointed to a removed NaN model
	if sds, ok := cfg["sessionDefaultSettings"].(map[string]any); ok {
		if model, _ := sds["model"].(string); model != "" {
			for _, rid := range removedIDs {
				if model == rid {
					delete(sds, "model")
					cfg["sessionDefaultSettings"] = sds
					break
				}
			}
		}
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(cfgPath, out)
}

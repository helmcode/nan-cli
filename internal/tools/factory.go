package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	catalog "github.com/helmcode/nan-cli/internal/models"
)

// factoryCustomID is the id Factory knows one of our models by, built the way
// Factory builds its own: the display name with dashes, and the position.
func factoryCustomID(displayName string, index int) string {
	return fmt.Sprintf("custom:%s-%d", strings.ReplaceAll(displayName, " ", "-"), index)
}

func writeFactoryConfig(cfgPath, apiKey string, cat []catalog.Model) error {
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

	served := make(map[string]bool, len(cat))
	for _, m := range cat {
		served[m.ID] = true
	}

	// Index NaN model IDs already present, dropping the ones the platform no
	// longer lists for this member (retired, or not theirs to call): picking
	// one answers with an error that reads like a broken key.
	existingIDs := map[string]bool{}
	removedIDs := map[string]bool{}
	changed := false
	kept := models[:0]
	nextIndex := 0
	for _, m := range models {
		if base, _ := m["baseUrl"].(string); isNaNBase(base) {
			id, _ := m["model"].(string)
			if !served[id] {
				if cid, _ := m["id"].(string); cid != "" {
					removedIDs[cid] = true
				}
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
	for _, nm := range cat {
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

	// Leave it pointing at a model the member can call: the quickstart's
	// default when it is served. A default that pointed at an entry just
	// taken out is moved rather than left dangling.
	sds, hasDefaults := cfg["sessionDefaultSettings"].(map[string]any)
	current, _ := sds["model"].(string)
	if !hasDefaults || removedIDs[current] {
		if pick := factoryEntryFor(models, catalog.Pick(cat, catalog.Default)); pick != "" {
			if !hasDefaults {
				sds = map[string]any{"reasoningEffort": "none", "autonomyMode": "normal"}
			}
			sds["model"] = pick
			cfg["sessionDefaultSettings"] = sds
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(cfgPath, data)
}

// factoryEntryFor is the Factory id of our entry for a model id.
func factoryEntryFor(models []map[string]any, modelID string) string {
	for _, m := range models {
		if base, _ := m["baseUrl"].(string); !isNaNBase(base) {
			continue
		}
		if id, _ := m["model"].(string); id == modelID {
			cid, _ := m["id"].(string)
			return cid
		}
	}
	return ""
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
		if base, _ := m["baseUrl"].(string); isNaNBase(base) {
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

package tools

import (
	"encoding/json"
	"os"
	"strings"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

func writeOpencodeConfig(cfgPath, apiKey string) error {
	cfg, err := readJSONConfig(cfgPath)
	if err != nil {
		return err
	}

	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	// `limit` is not decoration: without it opencode falls back to its own
	// guess at the window and compacts a 1M-token session as if it were a
	// small one. An unknown key (this used to be written as `contextWindow`)
	// raises nothing anyone sees, which is why nan.builders/docs/opencode
	// spells the field out and why this writes it.
	nanModels := map[string]any{}
	for _, m := range catalog.ChatModels() {
		nanModels[m.ID] = map[string]any{
			"name": m.Name,
			"limit": map[string]any{
				"context": m.Context,
				"output":  m.Output,
			},
			"modalities": map[string]any{
				"input":  m.Inputs,
				"output": []string{"text"},
			},
		}
	}

	if nan, ok := providers["nan"].(map[string]any); ok {
		if opts, ok := nan["options"].(map[string]any); ok {
			if base, _ := opts["baseURL"].(string); strings.Contains(base, "api.nan.builders") {
				// Already configured — merge any missing models
				existing, _ := nan["models"].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				changed := false
				for id := range existing {
					if catalog.IsRetired(id) {
						delete(existing, id)
						changed = true
					}
				}
				for id, m := range nanModels {
					found, ok := existing[id].(map[string]any)
					if !ok {
						existing[id] = m
						changed = true
						continue
					}
					// An entry written by an older version of this CLI has no
					// `limit` at all. Filling it in is the only way those
					// configs ever stop compacting early; anything the member
					// set themselves is left exactly as it is.
					for _, field := range []string{"limit", "modalities"} {
						if _, present := found[field]; !present {
							found[field] = m.(map[string]any)[field]
							changed = true
						}
					}
					if limit, ok := found["limit"].(map[string]any); ok {
						if ctx, ok := limit["context"].(float64); ok {
							for _, stale := range catalog.StaleContexts[id] {
								if int(ctx) == stale {
									limit["context"] = m.(map[string]any)["limit"].(map[string]any)["context"]
									changed = true
								}
							}
						}
					}
				}
				if !changed {
					return nil
				}
				nan["models"] = existing
				providers["nan"] = nan
				cfg["provider"] = providers
				data, err := json.MarshalIndent(cfg, "", "  ")
				if err != nil {
					return err
				}
				return writeConfigFile(cfgPath, data)
			}
		}
	}

	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	providers["nan"] = map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "NaN",
		"options": map[string]any{
			"baseURL": "https://api.nan.builders/v1",
			"apiKey":  apiKey,
		},
		"models": nanModels,
	}
	cfg["provider"] = providers

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(cfgPath, data)
}

func removeOpencodeConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		return nil
	}
	if _, ok := providers["nan"]; !ok {
		return nil // nothing to remove
	}
	delete(providers, "nan")
	cfg["provider"] = providers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(cfgPath, out)
}

// The sign-in questions, drawn where the tab content would be.
// The guided setup: the banner, the four steps with the one you are on marked,
// and whatever that step needs underneath.
//
// It replaced a sign-in screen that showed "Step 1 of 2" and then handed you
// back to the panel to find the other two yourself. Four numbered lines cost
// almost nothing and answer "how much is left", which is the question someone
// halfway through a setup is actually holding.

package tools

import (
	"encoding/json"
	"os"

	catalog "github.com/helmcode/nan-cli/internal/models"
)

// opencodeEntry is one served model as OpenCode's config spells it.
//
// `limit` is not decoration: without it opencode falls back to its own guess
// at the window and compacts a 1M-token session as if it were a small one. An
// unknown key (this used to be written as `contextWindow`) raises nothing
// anyone sees, which is why nan.builders/docs/opencode spells the field out
// and why this writes it. A figure nobody reported is left out rather than
// written as a guess.
func opencodeEntry(m catalog.Model) map[string]any {
	entry := map[string]any{
		"name": m.Name,
		"modalities": map[string]any{
			"input":  m.Inputs,
			"output": []string{"text"},
		},
	}
	limit := map[string]any{}
	if m.Context > 0 {
		limit["context"] = m.Context
	}
	if m.Output > 0 {
		limit["output"] = m.Output
	}
	if len(limit) > 0 {
		entry["limit"] = limit
	}
	return entry
}

func writeOpencodeConfig(cfgPath, apiKey string, cat []catalog.Model) error {
	cfg, err := readJSONConfig(cfgPath)
	if err != nil {
		return err
	}

	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	nanModels := map[string]map[string]any{}
	for _, m := range cat {
		nanModels[m.ID] = opencodeEntry(m)
	}

	if nan, ok := providers["nan"].(map[string]any); ok {
		if opts, ok := nan["options"].(map[string]any); ok {
			if base, _ := opts["baseURL"].(string); isNaNBase(base) {
				// Already configured: bring the models in line with what the
				// platform serves this member now.
				existing, _ := nan["models"].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				changed := false
				// An entry the platform no longer lists - retired, or one
				// this member cannot call - answers with a 401/402 that
				// reads like a broken key, so it goes.
				for id := range existing {
					if _, served := nanModels[id]; !served {
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
							if v, ok := m[field]; ok {
								found[field] = v
								changed = true
							}
						}
					}
					if limit, ok := found["limit"].(map[string]any); ok {
						if ctx, ok := limit["context"].(float64); ok {
							for _, stale := range catalog.StaleContexts[id] {
								if want, has := m["limit"].(map[string]any)["context"]; has && int(ctx) == stale {
									limit["context"] = want
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

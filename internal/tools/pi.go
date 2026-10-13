package tools

import (
	"encoding/json"
	"os"
	"path/filepath"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

// Pi takes a provider two ways: a models.json, which is data, or an extension
// that calls pi.registerProvider, which is code. Both are current in 0.85.1.
// This used to write the extension - a TypeScript file Pi loads and runs - and
// now writes the data, which is what nan.builders/docs/pi publishes: Pi
// validates models.json against its own schema and names the field that is
// wrong, nothing this CLI writes has to execute on a member's machine, and the
// two ways of setting Pi up stop being two things to keep in step.
func writePiConfig(cfgPath, apiKey string) error {
	// Unlike the extension file, models.json is shared: other providers live in
	// it and none of them are ours to touch.
	cfg, err := readJSONConfig(cfgPath)
	if err != nil {
		return err
	}

	providers, _ := cfg["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	models := make([]map[string]any, 0, len(catalog.ChatModels()))
	for _, m := range catalog.ChatModels() {
		models = append(models, map[string]any{
			"id":            m.ID,
			"name":          m.Name,
			"input":         piInputs(m),
			"reasoning":     m.Reasoning,
			"contextWindow": m.Context,
			"maxTokens":     m.Output,
		})
	}

	providers["nan"] = map[string]any{
		"name":    "NaN",
		"baseUrl": "https://api.nan.builders/v1",
		"apiKey":  apiKey,
		"api":     "openai-completions",
		"compat":  map[string]any{"supportsDeveloperRole": true},
		"models":  models,
	}
	cfg["providers"] = providers

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := writeConfigFile(cfgPath, data); err != nil {
		return err
	}
	return writePiDefaults(piSettingsPath(cfgPath))
}

// Pi reads the provider it calls from a second file, and until this existed
// the CLI wrote only the first one. nan.builders/docs/pi marks this step "not
// optional" for a reason: with models.json alone Pi goes on calling its
// factory provider, and what the member sees is a 401 that names neither file.
func writePiDefaults(settingsPath string) error {
	settings, err := readJSONConfig(settingsPath)
	if err != nil {
		return err
	}

	// A member who already picked a default picked it, and ours is one more
	// provider they can switch to from inside Pi. This only fills the gap that
	// leaves a fresh install calling nothing.
	if _, chosen := settings["defaultProvider"]; chosen {
		return nil
	}
	settings["defaultProvider"] = "nan"
	settings["defaultModel"] = catalog.Coding

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(settingsPath, out)
}

// settings.json sits next to models.json in Pi's agent directory.
func piSettingsPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "settings.json")
}

// Pi's schema takes "text" and "image" and nothing else, so mimo-v2.5 goes in
// without its audio: a third value fails validation, and Pi then refuses the
// whole file with every other provider in it.
func piInputs(m catalog.Model) []string {
	out := make([]string, 0, len(m.Inputs))
	for _, in := range m.Inputs {
		if in == catalog.InputText || in == catalog.InputImage {
			out = append(out, in)
		}
	}
	return out
}

// Deleting the file is no longer an option: models.json is where every Pi
// provider lives, and the extension it replaced was a file of ours alone.
func removePiConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath) //nolint:gosec // G304: a config file at a path this program chose
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	providers, _ := cfg["providers"].(map[string]any)
	if _, ours := providers["nan"]; !ours {
		return nil
	}
	delete(providers, "nan")

	// A default pointing at a provider that is no longer in models.json is
	// worse than no default: Pi starts and fails on the first message.
	if err := removePiDefaults(piSettingsPath(cfgPath)); err != nil {
		return err
	}

	// A models.json with nothing left in it is not a config Pi needs to read.
	if len(providers) == 0 && len(cfg) == 1 {
		return os.Remove(cfgPath)
	}
	cfg["providers"] = providers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(cfgPath, out)
}

// Only the default we wrote, and only while it still points at us: anything
// the member set themselves is theirs.
func removePiDefaults(settingsPath string) error {
	data, err := os.ReadFile(settingsPath) //nolint:gosec // G304: a config file at a path this program chose
	if err != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	var settings map[string]any
	if json.Unmarshal(data, &settings) != nil {
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	if settings["defaultProvider"] != "nan" {
		return nil
	}
	delete(settings, "defaultProvider")
	delete(settings, "defaultModel")

	// The file existed only to hold our default, so it goes with it.
	if len(settings) == 0 {
		return os.Remove(settingsPath)
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(settingsPath, out)
}

package tools

import (
	_ "embed"
	"encoding/json"
	"path/filepath"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

// Codex fills its /model picker from a catalogue of OpenAI's models, and the
// cluster's /v1/models answers in a shape Codex does not read as one. So a
// session on the nan provider offered only gpt-* models in /model, every one
// of which the cluster answers with a 401, and the only way to another NaN
// model was to quit and relaunch with another --profile.
//
// model_catalog_json replaces that catalogue. Pointed at this file from the
// nan-* profiles - and only from them, so the member's OpenAI sessions keep
// OpenAI's list - /model lists the cluster's chat models, each with the
// window it is served at, and the "Model metadata ... not found" warning goes
// away because now there is metadata.
//
// It cannot mix providers: model_provider is per session and a catalogue entry
// has no provider of its own. Going between gpt-* and NaN is still a relaunch.
const codexCatalogFile = "nan-models.json"

// Every catalogue entry has to carry the agent's system prompt in full: Codex
// refuses an entry without one, and an empty one goes out as no prompt at all.
// This is the prompt Codex sends a model it has no metadata for - which is what
// every NaN model got before the catalogue existed - so the catalogue changes
// the picker and the windows and leaves what the model is told exactly as it
// was. It is openai/codex's
// codex-rs/protocol/src/prompts/base_instructions/default.md at rust-v0.162.1
// with the "Planning" and "`update_plan`" sections taken out, the way Codex
// itself takes them out when that tool is not offered; compared byte for byte
// against a request Codex 0.162.1 sent. Apache-2.0, see codexprompt/LICENSE
// and codexprompt/NOTICE.
//
//go:embed codexprompt/base_instructions.md
var codexBaseInstructions string

type codexCatalogEntry struct {
	Slug                       string         `json:"slug"`
	DisplayName                string         `json:"display_name"`
	Description                string         `json:"description"`
	SupportedReasoningLevels   []any          `json:"supported_reasoning_levels"`
	ShellType                  string         `json:"shell_type"`
	Visibility                 string         `json:"visibility"`
	SupportedInAPI             bool           `json:"supported_in_api"`
	Priority                   int            `json:"priority"`
	SupportVerbosity           bool           `json:"support_verbosity"`
	TruncationPolicy           map[string]any `json:"truncation_policy"`
	ExperimentalSupportedTools []string       `json:"experimental_supported_tools"`
	BaseInstructions           string         `json:"base_instructions"`
	ContextWindow              int            `json:"context_window"`
	MaxContextWindow           int            `json:"max_context_window"`
	InputModalities            []string       `json:"input_modalities"`
	// Left to its default, an entry adds an "Apps (Connectors)" block to the
	// prompt that a model without metadata never got.
	IncludeAppsUsageInstructions bool `json:"include_apps_usage_instructions"`
}

func codexCatalog() ([]byte, error) {
	models := catalog.ChatModels()
	entries := make([]codexCatalogEntry, 0, len(models))
	for i, m := range models {
		entries = append(entries, codexCatalogEntry{
			Slug: m.ID,
			// No " (NaN)" as droid has: in a session on the nan provider
			// every model in the picker is one.
			DisplayName: m.Name,
			Description: "Served by the NaN cluster.",
			// What a model without metadata gets, so the request is the one
			// Codex was already sending.
			SupportedReasoningLevels:   []any{},
			ShellType:                  "unified_exec",
			Visibility:                 "list",
			SupportedInAPI:             true,
			Priority:                   i + 1,
			TruncationPolicy:           map[string]any{"mode": "tokens", "limit": 10000},
			ExperimentalSupportedTools: []string{},
			BaseInstructions:           codexBaseInstructions,
			ContextWindow:              m.Context,
			MaxContextWindow:           m.Context,
			// Codex takes text and image here and nothing else; audio and
			// video are not modalities it can send.
			InputModalities: piInputs(m),
		})
	}
	return json.MarshalIndent(map[string]any{"models": entries}, "", "  ")
}

// Forward slashes, because the path goes into a TOML basic string in the
// profiles and a Windows backslash there is an escape. Codex resolves
// C:/Users/... the same as C:\Users\....
func codexCatalogPath(codexHome string) string {
	return filepath.ToSlash(filepath.Join(codexHome, codexCatalogFile))
}

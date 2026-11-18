// Package models turns the platform's model list into what the tool writers
// need. There is no list in here: a hand-written one drifted from the cluster
// three times, so Setup asks GET /api/agents/models every time it runs and
// writes nothing when it cannot.
package models

import (
	"regexp"

	"github.com/nxssie/nan-cli/internal/api"
)

// Used when the platform does not report a figure. Short on purpose: a window
// written short makes a tool compact early, one written long makes it fill
// the conversation until the model refuses the request.
const (
	FallbackContext = 131_072
	FallbackOutput  = 8_192
)

// An id ends up in TOML strings, JSON keys and Codex profile file names, so
// anything that is not a plain model id is skipped rather than escaped.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Model struct {
	ID string
	// The window the proxy serves, in tokens.
	Context int
	// The answer budget a client should plan a turn against.
	Output    int
	Vision    bool
	Reasoning bool
}

// Inputs is what every harness takes: text, and image when the model sees.
// Nothing else is ever yielded: audio and video are not modalities the
// harnesses can send.
func (m Model) Inputs() []string {
	if m.Vision {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// FromAPI keeps the platform's order (the first entry is the default model)
// and fills what it did not report with the conservative defaults above.
func FromAPI(list []api.AgentModel) []Model {
	out := make([]Model, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, a := range list {
		if !validID.MatchString(a.Name) || seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		out = append(out, Model{
			ID:        a.Name,
			Context:   positiveOr(a.ContextWindow, FallbackContext),
			Output:    positiveOr(a.MaxOutputTokens, FallbackOutput),
			Vision:    a.SupportsVision != nil && *a.SupportsVision,
			Reasoning: a.SupportsReasoning != nil && *a.SupportsReasoning,
		})
	}
	return out
}

func positiveOr(v *int, fallback int) int {
	if v == nil || *v <= 0 {
		return fallback
	}
	return *v
}

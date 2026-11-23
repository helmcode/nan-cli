package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/helmcode/nan-cli/internal/api"
)

// What Setup configures the tools with.
//
// The list of WHICH models comes from the platform, never from this package:
// GET /api/agents/models already leaves out what the member cannot call (a
// canary audience, pack-only models, premium without access), reading the
// member live. The table in models.go is only what this CLI knows ABOUT a
// model the platform names - a display name and, for a platform that does not
// report them, the figures - so a model missing from the platform's list is
// never added from here.

// ErrNoModels is a platform and a key that between them named no model to
// configure. Nothing is written then: a config with no models in it, or with
// the models of an earlier day, is worse than the one the member has.
var ErrNoModels = errors.New("no model to configure")

// An id ends up in TOML strings, JSON keys and Codex profile file names, so
// anything that is not a plain model id is skipped rather than escaped.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// FromAPI turns the platform's list into what the writers take. It keeps the
// platform's order and skips ids that are unsafe to write, duplicates, and
// anything that is not a chat model.
//
// A figure the platform did not report is the one this CLI knows for that
// model, and for a model it does not know it is left at zero, which the
// writers read as "unknown" and leave out of the config rather than guess. A
// guessed window written short makes a tool compact early; one written long
// fills the conversation until the model refuses it.
func FromAPI(list []api.AgentModel) []Model {
	out := make([]Model, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, a := range list {
		if !validID.MatchString(a.Name) || seen[a.Name] || !chatMode(a.Mode) {
			continue
		}
		known, isKnown := Get(a.Name)
		if isKnown && known.Kind != Chat {
			continue
		}
		seen[a.Name] = true

		m := Model{ID: a.Name, Kind: Chat, Name: a.Name, Inputs: []string{InputText}}
		if isKnown {
			m = known
			m.Inputs = append([]string(nil), known.Inputs...)
		}
		if a.ContextWindow != nil && *a.ContextWindow > 0 {
			m.Context = *a.ContextWindow
		}
		if a.MaxOutputTokens != nil && *a.MaxOutputTokens > 0 {
			m.Output = *a.MaxOutputTokens
		}
		if a.SupportsReasoning != nil {
			m.Reasoning = *a.SupportsReasoning
		}
		if a.SupportsVision != nil {
			m.Inputs = withVision(m.Inputs, *a.SupportsVision)
		}
		out = append(out, m)
	}
	return out
}

// FromIDs is FromAPI for a list of bare ids, which is all the inference API's
// own /models says.
func FromIDs(ids []string) []Model {
	list := make([]api.AgentModel, 0, len(ids))
	for _, id := range ids {
		list = append(list, api.AgentModel{Name: id})
	}
	return FromAPI(list)
}

// The platform's list carries a mode, empty for a chat model. Everything else
// (embedding, speech, images) has no place in a coding agent's picker.
func chatMode(mode string) bool {
	return mode == "" || mode == "chat" || mode == "completion"
}

func withVision(inputs []string, vision bool) []string {
	out := make([]string, 0, len(inputs)+1)
	for _, in := range inputs {
		if in != InputImage {
			out = append(out, in)
		}
	}
	if !vision {
		return out
	}
	// After text, before audio, where the table writes it.
	if len(out) > 0 && out[0] == InputText {
		return append([]string{InputText, InputImage}, out[1:]...)
	}
	return append([]string{InputImage}, out...)
}

// Pick is the model a tool is left pointing at: the first of preferred (what
// the tool already has, then what it was meant to start with) that the member
// can call, otherwise the quickstart's default, then the coding model, then
// the first listed. Never an id outside the list.
func Pick(list []Model, preferred ...string) string {
	has := func(id string) bool {
		for _, m := range list {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	for _, id := range append(append([]string(nil), preferred...), Default, Coding) {
		if id != "" && has(id) {
			return id
		}
	}
	if len(list) > 0 {
		return list[0].ID
	}
	return ""
}

// Load is the list Setup configures from: the platform's, as the member's
// session sees it. Without a session, or when the platform cannot answer or
// has nothing to say, it is what the key itself can call - the inference
// API's own list, the strictest view there is - so a machine with only a key
// still sets up. When neither answers it fails and nothing should be written.
func Load(ctx context.Context, c *api.Client, key string) ([]Model, error) {
	var platformErr error
	if c != nil && c.Token() != "" {
		list, err := c.AgentModels(ctx)
		if err == nil {
			if m := FromAPI(list); len(m) > 0 {
				return m, nil
			}
		}
		platformErr = err
	}
	keyIDs, keyErr := api.ListModels(ctx, key)
	if keyErr == nil {
		if m := FromIDs(keyIDs); len(m) > 0 {
			return m, nil
		}
		keyErr = ErrNoModels
	}
	if platformErr != nil {
		return nil, fmt.Errorf("the platform said: %w; the cluster said: %w", platformErr, keyErr)
	}
	return nil, keyErr
}

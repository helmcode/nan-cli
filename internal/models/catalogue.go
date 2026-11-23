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
// WHICH models is the key's to say, never this package's: the inference API's
// own /models answers with what that key's team can call, the same check the
// call itself passes. GET /api/agents/models lists every model NaN serves and
// only DESCRIBES them (window, output, vision, reasoning); it does not decide
// who may call what. The table in models.go is only what this CLI knows ABOUT
// a model - a display name and, where nobody reports them, the figures - so a
// model the key does not list is never added from here.
//
// The key's list is also not a list of CHAT models: it carries image
// generators and whatever else the team can call, and a bare id says nothing
// about what it is. Configured as chat, such an id shows in the tool's picker
// and fails on the first request. So when the platform's list was read, a key
// id is configured only if the platform describes it as chat (see chatMode:
// an empty mode is chat) or this CLI's table knows it as chat; an id neither
// of them vouches for is skipped. With no platform list (signed out, or it failed) there is
// nothing to tell them apart by, and refusing every bare id would break
// signed-out setup, so those stay, minus the ids the table knows are not chat.

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

// Load is the list Setup configures from: the models the key can call, as the
// inference API's own /models says, each described by the platform's list
// when the member has a session. Only when the key's list cannot be read is
// the platform's list used as is, so a machine with a flaky route to the
// cluster still sets up. A key that lists nothing gets nothing written. When
// neither answers it fails and nothing should be written.
func Load(ctx context.Context, c *api.Client, key string) ([]Model, error) {
	var (
		platform    []api.AgentModel
		platformErr error
	)
	if c != nil && c.Token() != "" {
		platform, platformErr = c.AgentModels(ctx)
	}
	keyIDs, keyErr := api.ListModels(ctx, key)
	if keyErr == nil {
		if m := FromAPI(describe(keyIDs, platform)); len(m) > 0 {
			return m, nil
		}
		return nil, ErrNoModels
	}
	if platformErr == nil {
		if m := FromAPI(platform); len(m) > 0 {
			return m, nil
		}
	}
	if platformErr != nil {
		return nil, fmt.Errorf("the platform said: %w; the cluster said: %w", platformErr, keyErr)
	}
	return nil, keyErr
}

// describe is the key's ids, in the key's order, each with what the platform
// says about it. A platform entry whose name is not a valid id is ignored on
// the way in, so a list with no usable entry counts as no list. An id the
// platform does not describe is a bare entry, which FromAPI reads as chat, so
// when the platform's list is there it is kept only if the table knows it as
// chat; otherwise it is dropped. Without a platform list every id is a bare
// entry.
func describe(ids []string, platform []api.AgentModel) []api.AgentModel {
	byName := make(map[string]api.AgentModel, len(platform))
	for _, a := range platform {
		if !validID.MatchString(a.Name) {
			continue
		}
		if _, ok := byName[a.Name]; !ok {
			byName[a.Name] = a
		}
	}
	out := make([]api.AgentModel, 0, len(ids))
	for _, id := range ids {
		if a, ok := byName[id]; ok {
			out = append(out, a)
			continue
		}
		if len(byName) > 0 {
			if known, ok := Get(id); !ok || known.Kind != Chat {
				continue
			}
		}
		out = append(out, api.AgentModel{Name: id})
	}
	return out
}

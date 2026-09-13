// Package models is the CLI's one list of what the cluster serves.
//
// It existed three times before, written by hand inside each tool writer in
// internal/tui, and all three had drifted to the same stale set: qwen3.6,
// gemma4 and deepseek-v4-flash. The cluster serves seven chat models, so a
// member who ran `nan` got a tool configured for three of them and no way to
// discover the rest. The windows were worse than stale: Pi was configured with
// 128000 tokens on models served at 1,048,576, so it compacted the
// conversation at an eighth of the real window.
//
// The numbers here are the ones nan.builders publishes on the setup guides
// (/docs/opencode, /docs/pi), measured against the proxy on 2026-09-11. When
// the platform moves one, this is the file to change.
package models

type Model struct {
	ID string
	// As the setup guides name it, so a picker in one tool reads like the
	// picker in the next.
	Name string
	// The window the proxy serves, in tokens.
	Context int
	// The answer budget a client should plan a turn against. Not a server cap:
	// vLLM bounds a completion by the window minus the prompt.
	Output int
	// Input modalities. Output is text on every model here.
	Inputs []string
	// Whether it reasons before answering.
	Reasoning bool
	// Callable only with a key on the premium tier. Left in the configs it
	// writes, with the tier in its display name, because leaving it out is how
	// a member on premium ends up not knowing they have it.
	Premium bool
}

const (
	InputText  = "text"
	InputImage = "image"
	InputAudio = "audio"
)

var All = []Model{
	{
		ID:        "deepseek-v4-flash",
		Name:      "DeepSeek V4 Flash",
		Context:   1_048_575,
		Output:    32_768,
		Inputs:    []string{InputText, InputImage},
		Reasoning: true,
	},
	{
		ID:        "glm5.3-flash",
		Name:      "GLM 5.3 Flash",
		Context:   1_048_576,
		Output:    32_768,
		Inputs:    []string{InputText, InputImage},
		Reasoning: true,
	},
	{
		ID:        "qwen3.8-flash",
		Name:      "Qwen 3.8 Flash",
		Context:   262_144,
		Output:    32_768,
		Inputs:    []string{InputText, InputImage},
		Reasoning: true,
	},
	{
		ID:        "mimo-v2.5",
		Name:      "Xiaomi MiMo V2.5",
		Context:   1_048_576,
		Output:    32_768,
		Inputs:    []string{InputText, InputImage, InputAudio},
		Reasoning: true,
	},
	{
		ID:        "gemma4",
		Name:      "Gemma 4",
		Context:   262_144,
		Output:    65_536,
		Inputs:    []string{InputText, InputImage},
		Reasoning: true,
	},
	{
		ID:        "qwen3.6",
		Name:      "Qwen 3.6",
		Context:   262_144,
		Output:    65_536,
		Inputs:    []string{InputText, InputImage},
		Reasoning: true,
	},
	{
		ID:        "glm5.3",
		Name:      "GLM 5.3 (premium)",
		Context:   1_048_576,
		Output:    32_768,
		Inputs:    []string{InputText},
		Reasoning: true,
		Premium:   true,
	},
}

// Default is what a tool is left pointing at when it has no preference of its
// own: the model the quickstart on nan.builders starts everyone with.
const Default = "deepseek-v4-flash"

// Coding is for tools whose whole job is an editor or an agent in a repo.
const Coding = "glm5.3-flash"

func (m Model) Accepts(input string) bool {
	for _, i := range m.Inputs {
		if i == input {
			return true
		}
	}
	return false
}

func Get(id string) (Model, bool) {
	for _, m := range All {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

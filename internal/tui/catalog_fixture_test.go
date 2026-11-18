package tui

import (
	"github.com/nxssie/nan-cli/internal/api"
	catalog "github.com/nxssie/nan-cli/internal/models"
)

// What GET /api/agents/models answers, including a model the old hand-written
// list never had (kimi-k3.1-pro) and one the platform knows nothing about.
func testAPIModels() []api.AgentModel {
	i := func(n int) *int { return &n }
	b := func(v bool) *bool { return &v }
	return []api.AgentModel{
		{Name: "deepseek-v4-flash", Mode: "chat", ContextWindow: i(1_048_575), MaxOutputTokens: i(32_768), SupportsVision: b(true), SupportsReasoning: b(true)},
		{Name: "glm5.3", Mode: "chat", ContextWindow: i(1_048_576), MaxOutputTokens: i(32_768), SupportsVision: b(false), SupportsReasoning: b(true)},
		{Name: "kimi-k3.1-pro", Mode: "chat", ContextWindow: i(262_144), MaxOutputTokens: i(65_536), SupportsVision: b(true), SupportsReasoning: b(true)},
		{Name: "bare-model", Mode: "chat"},
	}
}

var testModels = catalog.FromAPI(testAPIModels())

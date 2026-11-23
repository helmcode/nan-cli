package tools

import (
	"github.com/helmcode/nan-cli/internal/api"
	catalog "github.com/helmcode/nan-cli/internal/models"
)

// testCatalogue is what a platform that serves every model this CLI knows as
// a chat model would hand over, with no metadata of its own.
func testCatalogue() []catalog.Model {
	chat := catalog.ChatModels()
	list := make([]api.AgentModel, 0, len(chat))
	for _, m := range chat {
		list = append(list, api.AgentModel{Name: m.ID})
	}
	return catalog.FromAPI(list)
}

// catalogueOf is a platform answer naming exactly these models.
func catalogueOf(list ...api.AgentModel) []catalog.Model { return catalog.FromAPI(list) }

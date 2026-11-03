package models

import (
	"reflect"
	"testing"
)

func TestChatModelsInKeepsCatalogOrderAndDropsEverythingElse(t *testing.T) {
	got := ChatModelsIn([]string{"glm5.3-flash", "flux-2-klein", "minimax-h3", "deepseek-v4-flash"})

	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	want := []string{"deepseek-v4-flash", "glm5.3-flash"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ChatModelsIn = %v, want %v", ids, want)
	}
}

func TestChatModelsInWithNoIdsIsEmpty(t *testing.T) {
	if got := ChatModelsIn(nil); len(got) != 0 {
		t.Errorf("ChatModelsIn(nil) = %v, want none", got)
	}
}

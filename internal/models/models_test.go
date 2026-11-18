package models

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

func TestFromAPI(t *testing.T) {
	i := func(n int) *int { return &n }
	b := func(v bool) *bool { return &v }

	t.Run("copies the known fields and keeps the order", func(t *testing.T) {
		got := FromAPI([]api.AgentModel{
			{Name: "zeta", ContextWindow: i(262_144), MaxOutputTokens: i(65_536), SupportsVision: b(true), SupportsReasoning: b(true)},
			{Name: "alpha", ContextWindow: i(1_048_576), MaxOutputTokens: i(32_768), SupportsVision: b(false), SupportsReasoning: b(false)},
		})
		want := []Model{
			{ID: "zeta", Context: 262_144, Output: 65_536, Vision: true, Reasoning: true},
			{ID: "alpha", Context: 1_048_576, Output: 32_768},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("falls back when a field is not reported", func(t *testing.T) {
		got := FromAPI([]api.AgentModel{{Name: "bare"}, {Name: "zero", ContextWindow: i(0), MaxOutputTokens: i(-1)}})
		for _, m := range got {
			if m.Context != FallbackContext || m.Output != FallbackOutput || m.Vision || m.Reasoning {
				t.Errorf("%s = %+v, want the conservative defaults", m.ID, m)
			}
		}
		if len(got) != 2 {
			t.Errorf("got %d models, want 2", len(got))
		}
	})

	t.Run("skips empty, duplicate and unsafe names", func(t *testing.T) {
		var list []api.AgentModel
		for _, name := range []string{"", "ok", "ok", "../x", "a b", `a"b`, "a/b", "a:b", `a\b`, strings.Repeat("a", 200), ".hidden", "fine-1.2_x"} {
			list = append(list, api.AgentModel{Name: name})
		}
		var ids []string
		for _, m := range FromAPI(list) {
			ids = append(ids, m.ID)
		}
		if want := []string{"ok", "fine-1.2_x"}; !reflect.DeepEqual(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})
}

func TestInputs(t *testing.T) {
	if got := (Model{}).Inputs(); !reflect.DeepEqual(got, []string{"text"}) {
		t.Errorf("text-only inputs = %v", got)
	}
	if got := (Model{Vision: true}).Inputs(); !reflect.DeepEqual(got, []string{"text", "image"}) {
		t.Errorf("vision inputs = %v", got)
	}
}

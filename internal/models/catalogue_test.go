package models

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/helmcode/nan-cli/internal/api"
)

func ip(v int) *int   { return &v }
func bp(v bool) *bool { return &v }
func ids(l []Model) string {
	out := make([]string, 0, len(l))
	for _, m := range l {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

func TestFromAPIUsesWhatThePlatformReports(t *testing.T) {
	got := FromAPI([]api.AgentModel{{
		Name: "brand-new", ContextWindow: ip(500_000), MaxOutputTokens: ip(16_000),
		SupportsVision: bp(true), SupportsReasoning: bp(true),
	}})
	if len(got) != 1 {
		t.Fatalf("got %d models", len(got))
	}
	m := got[0]
	if m.Context != 500_000 || m.Output != 16_000 || !m.Reasoning || !m.Accepts(InputImage) {
		t.Errorf("metadata not applied: %+v", m)
	}
	if m.Name != "brand-new" {
		t.Errorf("an unknown model is named by its id, got %q", m.Name)
	}
}

// An older platform sends no metadata. A model this CLI knows must keep the
// values it knows - not be rewritten with a zero or a guess.
func TestFromAPIWithoutMetadataKeepsKnownValues(t *testing.T) {
	known, _ := Get("glm5.3-flash")
	got := FromAPI([]api.AgentModel{{Name: "glm5.3-flash"}})
	if len(got) != 1 {
		t.Fatal("model dropped")
	}
	m := got[0]
	if m.Context != known.Context || m.Output != known.Output || m.Name != known.Name || !m.Accepts(InputImage) {
		t.Errorf("known values overwritten: got %+v want %+v", m, known)
	}
}

// A model nobody documented and the platform did not describe has no window:
// 0 means unknown, and the writers omit it instead of inventing one.
func TestFromAPIUnknownModelWithoutMetadataHasNoWindow(t *testing.T) {
	m := FromAPI([]api.AgentModel{{Name: "mystery"}})[0]
	if m.Context != 0 || m.Output != 0 || m.Reasoning || m.Accepts(InputImage) {
		t.Errorf("nothing may be invented: %+v", m)
	}
	if !m.Accepts(InputText) {
		t.Errorf("every model takes text: %+v", m)
	}
}

func TestFromAPIZeroOrNegativeFiguresAreNotReported(t *testing.T) {
	known, _ := Get("gemma4")
	m := FromAPI([]api.AgentModel{{Name: "gemma4", ContextWindow: ip(0), MaxOutputTokens: ip(-5)}})[0]
	if m.Context != known.Context || m.Output != known.Output {
		t.Errorf("a zero/negative figure replaced a known one: %+v", m)
	}
}

func TestFromAPIVisionFalseOverridesKnown(t *testing.T) {
	m := FromAPI([]api.AgentModel{{Name: "gemma4", SupportsVision: bp(false)}})[0]
	if m.Accepts(InputImage) {
		t.Errorf("the platform says no vision: %+v", m)
	}
}

func TestFromAPISkipsUnsafeDuplicateAndNonChatEntries(t *testing.T) {
	list := []api.AgentModel{
		{Name: "ok-1"},
		{Name: "ok-1"},
		{Name: "../escape"},
		{Name: "a/b"},
		{Name: "-flag"},
		{Name: "quote\"d"},
		{Name: "new\nline"},
		{Name: ""},
		{Name: strings.Repeat("a", 200)},
		{Name: "embed-x", Mode: "embedding"},
		{Name: "tts-x", Mode: "audio_speech"},
		{Name: "rerank"}, // known as a reranker, whatever the mode says
		{Name: "ok-2", Mode: "chat"},
	}
	if got := ids(FromAPI(list)); got != "ok-1,ok-2" {
		t.Errorf("got %q, want ok-1,ok-2", got)
	}
}

// The platform already narrowed the list to what the member can call. The CLI
// must not put anything back from its own table.
func TestFromAPIDoesNotAddModelsTheListLacks(t *testing.T) {
	if got := FromAPI([]api.AgentModel{{Name: "deepseek-v4-flash"}}); ids(got) != "deepseek-v4-flash" {
		t.Errorf("got %q", ids(got))
	}
	if got := FromAPI(nil); len(got) != 0 {
		t.Errorf("an empty list stays empty, got %v", got)
	}
}

// Which plan reaches a model is the platform's to decide, not this CLI's: a
// model is named the same for every member.
func TestFromAPINamesNoPlan(t *testing.T) {
	m := FromAPI([]api.AgentModel{{Name: "glm5.3"}})[0]
	if strings.Contains(strings.ToLower(m.Name), "premium") {
		t.Errorf("a plan leaked into the model's name: %+v", m)
	}
}

func TestPick(t *testing.T) {
	mk := func(names ...string) []Model {
		l := make([]api.AgentModel, 0, len(names))
		for _, n := range names {
			l = append(l, api.AgentModel{Name: n})
		}
		return FromAPI(l)
	}
	cases := []struct {
		name    string
		list    []Model
		current string
		want    string
	}{
		{"current is served", mk("a", "deepseek-v4-flash", "b"), "b", "b"},
		{"current is gone: the quickstart default", mk("a", "deepseek-v4-flash"), "gone", "deepseek-v4-flash"},
		{"no current: the quickstart default", mk("a", "deepseek-v4-flash"), "", "deepseek-v4-flash"},
		{"default missing: the coding model", mk("a", "glm5.3-flash"), "", "glm5.3-flash"},
		{"neither: the first listed", mk("glm5.3", "zzz", "yyy"), "", "glm5.3"},
		{"unknown first model is not preferred over a known default", mk("aaa", "glm5.3-flash"), "", "glm5.3-flash"},
		{"empty", nil, "x", ""},
	}
	for _, c := range cases {
		if got := Pick(c.list, c.current); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestLoad(t *testing.T) {
	platform := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	cluster := func(status int, body string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		t.Setenv("NAN_INFERENCE_URL", srv.URL)
	}

	t.Run("the key decides which, the platform describes them", func(t *testing.T) {
		p := platform(200, `{"models":[{"name":"x-1","context_window":4096},{"name":"x-2"},{"name":"img","mode":"image_generation"}]}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"x-1"},{"id":"x-3"},{"id":"img"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != "x-1" || got[0].Context != 4096 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("key list unreadable: the platform list", func(t *testing.T) {
		p := platform(200, `{"models":[{"name":"x-1","context_window":4096}]}`)
		defer p.Close()
		cluster(500, "")
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != "x-1" || got[0].Context != 4096 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("key lists nothing: nothing to write, whatever the platform says", func(t *testing.T) {
		p := platform(200, `{"models":[{"name":"x-1"}]}`)
		defer p.Close()
		cluster(200, `{"data":[]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if !errors.Is(err, ErrNoModels) || len(got) != 0 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("platform down: the key's own list", func(t *testing.T) {
		p := platform(500, `{"error":"boom"}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"y-1"},{"id":"../bad"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != "y-1" {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("no session: the key's own list", func(t *testing.T) {
		cluster(200, `{"data":[{"id":"y-1"}]}`)
		got, err := Load(context.Background(), api.New(""), "key")
		if err != nil || ids(got) != "y-1" {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("platform empty: the key's own list", func(t *testing.T) {
		p := platform(200, `{"models":[]}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"y-1"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != "y-1" {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("both fail: error, nothing to write", func(t *testing.T) {
		p := platform(500, `x`)
		defer p.Close()
		cluster(500, ``)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err == nil || len(got) != 0 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("malformed bodies count as no list", func(t *testing.T) {
		for _, body := range []string{`<html>`, `{}`, `{"models":null}`, `{"models":[{"name":"../x"}]}`} {
			p := platform(200, body)
			cluster(200, `{"data":[]}`)
			got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
			p.Close()
			if !errors.Is(err, ErrNoModels) || len(got) != 0 {
				t.Errorf("%q: got %v %v", body, got, err)
			}
		}
	})

	t.Run("platform read: ids it does not describe are not chat models", func(t *testing.T) {
		p := platform(200, `{"models":[{"name":"gemma4"},{"name":"qwen3.6","mode":"chat"},{"name":"other","mode":"image_generation"}]}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"gemma4"},{"id":"qwen-image-2.1"},{"id":"minimax-h3"},{"id":"qwen3.6"},{"id":"other"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != "gemma4,qwen3.6" {
			t.Fatalf("got %v %v", ids(got), err)
		}
	})
	t.Run("platform read: an id the table knows as chat is kept without a description", func(t *testing.T) {
		known := ChatModels()[0].ID
		p := platform(200, `{"models":[{"name":"gemma4"}]}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"`+known+`"},{"id":"minimax-h3"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != known {
			t.Fatalf("got %v %v", ids(got), err)
		}
	})
	t.Run("platform read: never adds a model the key does not list", func(t *testing.T) {
		p := platform(200, `{"models":[{"name":"gemma4"},{"name":"qwen3.6"}]}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"gemma4"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if err != nil || ids(got) != "gemma4" {
			t.Fatalf("got %v %v", ids(got), err)
		}
	})
	t.Run("platform read: nothing described as chat leaves no models", func(t *testing.T) {
		p := platform(200, `{"models":[{"name":"gemma4"}]}`)
		defer p.Close()
		cluster(200, `{"data":[{"id":"qwen-image-2.1"}]}`)
		got, err := Load(context.Background(), api.New("s", api.WithBaseURL(p.URL)), "key")
		if !errors.Is(err, ErrNoModels) || len(got) != 0 {
			t.Fatalf("got %v %v", got, err)
		}
	})
	t.Run("no platform list: bare ids stay, ids the table knows as non-chat go", func(t *testing.T) {
		cluster(200, `{"data":[{"id":"minimax-h3"},{"id":"kokoro"},{"id":"gemma4"}]}`)
		got, err := Load(context.Background(), api.New(""), "key")
		if err != nil || ids(got) != "minimax-h3,gemma4" {
			t.Fatalf("got %v %v", ids(got), err)
		}
	})
}

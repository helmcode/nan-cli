package tui

import (
	"reflect"
	"testing"
)

func TestCodexLineReadsTOMLNotText(t *testing.T) {
	cases := []struct {
		line  string
		key   string
		value any
		ok    bool
	}{
		{`model_provider = "nan"`, "model_provider", "nan", true},
		{`"model_provider" = 'nan' # ours`, "model_provider", "nan", true},
		{`model_provider = "n\u0061n"`, "model_provider", "nan", true},
		{`web_search="disabled"`, "web_search", "disabled", true},
		{`# model_provider = "nan"`, "", nil, false},
		{`[agents]`, "", nil, false},
		{`model = """`, "", nil, false},
		{``, "", nil, false},
	}
	for _, c := range cases {
		k, v, ok := codexLine(c.line)
		if ok != c.ok || k != c.key || !reflect.DeepEqual(v, c.value) {
			t.Errorf("%q: got %q %v %v, want %q %v %v", c.line, k, v, ok, c.key, c.value, c.ok)
		}
	}
}

func TestCodexTablePathReadsHeadersAsTOML(t *testing.T) {
	cases := map[string]string{
		"[agents]":              "agents",
		"[agents] # settings":   "agents",
		`["agents"]`:            "agents",
		"[ agents ]":            "agents",
		"[model_providers.nan]": "model_providers.nan",
		`[projects."/a/b"]`:     "projects./a/b",
	}
	for line, want := range cases {
		if got, ok := codexTablePath(line); !ok || got != want {
			t.Errorf("%q: got %q %v, want %q", line, got, ok, want)
		}
	}
	for _, line := range []string{"[[agents]]", "agents = 1", "[1, 2],", ""} {
		if got, ok := codexTablePath(line); ok {
			t.Errorf("%q read as table %q", line, got)
		}
	}
}

func TestCodexEditMeansRejectsAChangeOfMeaning(t *testing.T) {
	want, _ := codexTOML("a = 1\n")
	if !codexEditMeans("a = 1 # same\n", want) {
		t.Error("an edit that kept the meaning was refused")
	}
	if codexEditMeans("a = 2\n", want) || codexEditMeans("a = \n", want) {
		t.Error("an edit that changed or broke the meaning was accepted")
	}
}

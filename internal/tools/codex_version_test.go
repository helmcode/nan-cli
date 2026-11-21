package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	catalog "github.com/helmcode/nan-cli/internal/models"
)

func withCodexVersion(t *testing.T, version string) {
	t.Helper()
	original := codexVersion
	codexVersion = func() string { return version }
	t.Cleanup(func() { codexVersion = original })
}

func TestCodexSubAgentSwitchFollowsTheVersion(t *testing.T) {
	cases := map[string]string{
		"codex-cli 0.169.0": "[agents]\nenabled = false\n",
		"codex-cli 0.168.0": "[agents]\nenabled = false\n",
		"codex-cli 1.0.0":   "[agents]\nenabled = false\n",
		"codex-cli 0.167.2": "[features]\nmulti_agent = false\n",
		"codex-cli 0.155.0": "[features]\nmulti_agent = false\n",
		"":                  "[agents]\nenabled = false\n",
		"garbage":           "[agents]\nenabled = false\n",
		"codex-cli x.y.z":   "[agents]\nenabled = false\n",
	}
	for version, want := range cases {
		if use, _ := codexSubAgentSwitches(version); use.off() != want {
			t.Errorf("%q: got %q, want %q", version, use.off(), want)
		}
	}
}

func TestCodexProfileGolden(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	m := catalog.ChatModels()[0]
	got := readFile(t, filepath.Join(home, codexProfileName(m.ID)+".config.toml"))
	want := fmt.Sprintf(`# Written by the NaN CLI. Disconnecting Codex removes it.
model = %q
model_provider = "nan"
model_context_window = %d
model_catalog_json = %q
web_search = "disabled"

[agents]
enabled = false
`, m.ID, m.Context, codexCatalogPath(home))
	if got != want {
		t.Errorf("profile:\ngot:\n%s\nwant:\n%s", got, want)
	}
	assertCodexTOML(t, got)
}

// A Codex older than 0.168 keeps the key it understands in its profiles.
func TestCodexProfilesOnAnOlderCodexKeepMultiAgent(t *testing.T) {
	withCodexVersion(t, "codex-cli 0.159.0")
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	m := catalog.ChatModels()[0]
	body := readFile(t, filepath.Join(filepath.Dir(path), codexProfileName(m.ID)+".config.toml"))
	if !strings.Contains(body, "[features]\nmulti_agent = false") || strings.Contains(body, "[agents]") {
		t.Errorf("profile for Codex 0.159:\n%s", body)
	}
}

// A fake `codex` first on PATH, so the real probe runs without the real tool.
func fakeCodex(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script is not a codex on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { //nolint:gosec // G306: a test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCodexVersionProbeReadsTheVersion(t *testing.T) {
	fakeCodex(t, `echo "codex-cli 0.167.2"`)
	if got := probeCodexVersion(5 * time.Second); got != "codex-cli 0.167.2" {
		t.Errorf("got %q", got)
	}
}

func TestCodexVersionProbeWithNoCodexIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := probeCodexVersion(5 * time.Second); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestCodexVersionProbeThatFailsIsUnknown(t *testing.T) {
	fakeCodex(t, `echo "codex-cli 0.150.0"; exit 3`)
	if got := probeCodexVersion(5 * time.Second); got != "" {
		t.Errorf("a failed codex still gave a version: %q", got)
	}
}

// npm's codex is a wrapper: the process the probe starts is killed at the
// timeout, but the child it left holds the output pipe. The probe has to come
// back anyway - a member waiting on a setup screen for a hung codex is the
// failure this guards against.
func TestCodexVersionProbeThatHangsGivesUpAtTheTimeout(t *testing.T) {
	fakeCodex(t, `sleep 30`)
	start := time.Now()
	got := probeCodexVersion(200 * time.Millisecond)
	if got != "" {
		t.Errorf("got %q", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the probe waited %v on a hung codex", elapsed)
	}
}

// Codex moved between 0.167 and 0.169 on the same machine: each configure
// leaves exactly the switch that Codex reads in the profiles.
func TestCodexProfilesFollowAnUpgradeAndADowngrade(t *testing.T) {
	path := tempConfig(t, "config.toml")
	m := catalog.ChatModels()[0]
	profile := filepath.Join(filepath.Dir(path), codexProfileName(m.ID)+".config.toml")
	for _, s := range []struct{ version, has, hasNot string }{
		{"codex-cli 0.167.0", "multi_agent", "[agents]"},
		{"codex-cli 0.169.0", "[agents]", "multi_agent"},
		{"codex-cli 0.167.0", "multi_agent", "[agents]"},
	} {
		withCodexVersion(t, s.version)
		if err := writeCodexConfig(path, testKey); err != nil {
			t.Fatal(err)
		}
		body := readFile(t, profile)
		if !strings.Contains(body, s.has) || strings.Contains(body, s.hasNot) {
			t.Errorf("%s:\n%s", s.version, body)
		}
	}
}

// Golden for the file this CLI creates when Codex had no config: the default
// `codex` run is a NaN session, so it gets what a --profile run gets - the
// catalogue (no "Model metadata ... not found" warning) and neither web
// search nor sub-agents.
func TestCodexStarterConfigGolden(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	codexModel, _ := catalog.Get(catalog.Coding)
	want := fmt.Sprintf(`model = %q
model_provider = "nan"
model_context_window = %d
model_catalog_json = %q
web_search = "disabled"

[agents]
enabled = false

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "responses"
`, codexModel.ID, codexModel.Context, codexCatalogPath(filepath.Dir(path)), testKey)
	got := readFile(t, path)
	if got != want {
		t.Errorf("config.toml:\ngot:\n%s\nwant:\n%s", got, want)
	}
	assertCodexTOML(t, got)

	// A second configure changes nothing, byte for byte.
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, path); again != got {
		t.Errorf("a second configure changed the file:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

// A member whose Codex has its own provider keeps it, and keeps OpenAI's
// model list, web search and sub-agents: none of the NaN-only keys go into
// their root.
func TestCodexMemberRootGetsNoNaNOnlyKeys(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\nmodel_provider = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	doc, ok := codexTOML(readFile(t, path))
	if !ok {
		t.Fatal("not valid TOML")
	}
	for _, key := range []string{"model_catalog_json", "web_search", "agents", "features"} {
		if _, has := doc[key]; has {
			t.Errorf("%s went into a member's root", key)
		}
	}
}

// A member configured by an earlier version has the starter config without
// these keys; their next configure adds them, and the one after adds nothing.
func TestCodexStarterConfigOfAnEarlierVersionIsBroughtUpToDate(t *testing.T) {
	codexModel, _ := catalog.Get(catalog.Coding)
	path := tempConfig(t, "config.toml")
	old := fmt.Sprintf("model = %q\nmodel_provider = \"nan\"\nmodel_context_window = %d\n\n"+
		"[model_providers.nan]\nname = \"NaN\"\nbase_url = \"https://api.nan.builders/v1\"\n"+
		"experimental_bearer_token = %q\nwire_api = \"responses\"\n", codexModel.ID, codexModel.Context, testKey)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	fresh := tempConfig(t, "config.toml")
	if err := writeCodexConfig(fresh, testKey); err != nil {
		t.Fatal(err)
	}
	// Same as a file created today, apart from the catalogue path's home.
	if want := strings.ReplaceAll(readFile(t, fresh), filepath.ToSlash(filepath.Dir(fresh)), filepath.ToSlash(filepath.Dir(path))); first != want {
		t.Errorf("an earlier starter config was not brought up to date:\ngot:\n%s\nwant:\n%s", first, want)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if second := readFile(t, path); second != first {
		t.Errorf("a second configure changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// The value at "table.key" (or "key") in a parsed config.toml, nil if absent.
func codexTOMLGet(doc map[string]any, dotted string) any {
	var cur any = doc
	for _, part := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// Codex moved between 0.167 and 0.169 on the same machine: each configure
// leaves exactly the switch that Codex reads in config.toml, and never both.
func TestCodexSubAgentSwitchFollowsAnUpgradeAndADowngrade(t *testing.T) {
	path := tempConfig(t, "config.toml")
	steps := []struct {
		version     string
		has, hasNot string // table.key
	}{
		{"codex-cli 0.167.0", "features.multi_agent", "agents.enabled"},
		{"codex-cli 0.169.0", "agents.enabled", "features.multi_agent"},
		{"codex-cli 0.167.0", "features.multi_agent", "agents.enabled"},
	}
	for _, s := range steps {
		withCodexVersion(t, s.version)
		if err := writeCodexConfig(path, testKey); err != nil {
			t.Fatal(err)
		}
		content := readFile(t, path)
		doc, ok := codexTOML(content)
		if !ok {
			t.Fatalf("%s: not valid TOML:\n%s", s.version, content)
		}
		if codexTOMLGet(doc, s.has) != false {
			t.Errorf("%s: %s is not false:\n%s", s.version, s.has, content)
		}
		if codexTOMLGet(doc, s.hasNot) != nil {
			t.Errorf("%s: %s is still there:\n%s", s.version, s.hasNot, content)
		}
	}
}

// Tables the member already has, with keys of their own: the switch goes in
// beside them, nothing of theirs is lost, and no table is defined twice.
func TestCodexSubAgentSwitchJoinsTheMembersTables(t *testing.T) {
	base := "model = \"m\"\nmodel_provider = \"nan\"\n\n"
	cases := []struct{ name, tables, check string }{
		{"features with another flag", "[features]\nother_flag = true\n", "features.other_flag"},
		{"agents with a comment on the header", "[agents] # mine\nmax_threads = 4\n", "agents.max_threads"},
		{"quoted agents header", "[\"agents\"]\nmax_threads = 4\n", "agents.max_threads"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := tempConfig(t, "config.toml")
			if err := os.WriteFile(path, []byte(base+c.tables), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeCodexConfig(path, testKey); err != nil {
				t.Fatal(err)
			}
			content := readFile(t, path)
			doc, ok := codexTOML(content)
			if !ok {
				t.Fatalf("not valid TOML:\n%s", content)
			}
			if codexTOMLGet(doc, "agents.enabled") != false {
				t.Errorf("sub-agents not switched off:\n%s", content)
			}
			if codexTOMLGet(doc, c.check) == nil {
				t.Errorf("the member's %s was lost:\n%s", c.check, content)
			}
		})
	}
}

// Defined some other way - an inline table - the switch is not added, and
// the file stays valid rather than defining agents twice.
func TestCodexSubAgentSwitchLeavesAnInlineTableAlone(t *testing.T) {
	path := tempConfig(t, "config.toml")
	in := "model = \"m\"\nmodel_provider = \"nan\"\nagents = { max_threads = 4 }\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	doc, ok := codexTOML(content)
	if !ok {
		t.Fatalf("not valid TOML:\n%s", content)
	}
	if codexTOMLGet(doc, "agents.max_threads") != int64(4) {
		t.Errorf("the member's inline table changed:\n%s", content)
	}
}

// A root key the member already has, quoted or not, is not added again.
func TestCodexNaNSessionDefaultsKeepTheMembersValues(t *testing.T) {
	path := tempConfig(t, "config.toml")
	in := "model = \"m\"\nmodel_provider = \"nan\"\n\"web_search\" = \"live\"\n\"model_catalog_json\" = \"/mine.json\"\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	doc, ok := codexTOML(readFile(t, path))
	if !ok {
		t.Fatal("not valid TOML")
	}
	if doc["web_search"] != "live" || doc["model_catalog_json"] != "/mine.json" {
		t.Errorf("the member's values were replaced: %v %v", doc["web_search"], doc["model_catalog_json"])
	}
}

// ── disconnecting takes back what the NaN root was given ─────────────────────

func appendToFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// A disconnect takes all of it back, and the member's own content stays.
func TestCodexRemovalTakesTheNaNOnlyRootKeys(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	appendToFile(t, path, "\n[projects.\"/home/member/repo\"]\ntrust_level = \"trusted\"\n")
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	if want := "[projects.\"/home/member/repo\"]\ntrust_level = \"trusted\"\n"; content != want {
		t.Errorf("after the disconnect:\ngot:\n%q\nwant:\n%q", content, want)
	}
}

// The older-Codex variant comes back out the same way.
func TestCodexRemovalTakesTheMultiAgentSwitch(t *testing.T) {
	withCodexVersion(t, "codex-cli 0.159.0")
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err == nil {
		t.Errorf("config.toml outlived the disconnect:\n%s", data)
	}
}

// The root was switched to the member's own provider after we wrote the
// starter config. The catalogue file goes with the disconnect, so the root
// line pointing at it must go too; and since the catalogue line marks the
// rest as ours, web search and the sub-agent switch go with it.
func TestCodexRemovalAfterTheRootWasSwitchedToTheMembersProvider(t *testing.T) {
	for _, version := range []string{"codex-cli 0.169.0", "codex-cli 0.159.0"} {
		t.Run(version, func(t *testing.T) {
			withCodexVersion(t, version)
			path := tempConfig(t, "config.toml")
			if err := writeCodexConfig(path, testKey); err != nil {
				t.Fatal(err)
			}
			home := filepath.Dir(path)
			content := strings.Replace(readFile(t, path), `model_provider = "nan"`, `model_provider = "openai"`, 1)
			content = strings.Replace(content, fmt.Sprintf("model = %q", func() string { m, _ := catalog.Get(catalog.Coding); return m.ID }()), `model = "gpt-5"`, 1)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := removeCodexConfig(path); err != nil {
				t.Fatal(err)
			}
			got := readFile(t, path)
			assertCodexTOML(t, got)
			doc, _ := codexTOML(got)
			for _, key := range []string{"model_catalog_json", "web_search", "agents", "features"} {
				if _, has := doc[key]; has {
					t.Errorf("%s survived the disconnect:\n%s", key, got)
				}
			}
			if doc["model_provider"] != "openai" || doc["model"] != "gpt-5" {
				t.Errorf("the member's provider and model were touched:\n%s", got)
			}
			if _, err := os.Stat(filepath.Join(home, codexCatalogFile)); err == nil {
				t.Error("the catalogue file is still there")
			}
		})
	}
}

// Without our catalogue line nothing marks web search or the sub-agent switch
// as ours when the root is the member's: they are theirs.
func TestCodexRemovalKeepsAMembersOwnWebSearchAndAgentsOnTheirProvider(t *testing.T) {
	path := tempConfig(t, "config.toml")
	in := "model_provider = \"openai\"\nweb_search = \"disabled\"\n\n[agents]\nenabled = false\n\n" +
		"[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	if want := "model_provider = \"openai\"\nweb_search = \"disabled\"\n\n[agents]\nenabled = false\n"; readFile(t, path) != want {
		t.Errorf("got:\n%s", readFile(t, path))
	}
}

// A catalogue path that is not ours is the member's, on any root.
func TestCodexRemovalKeepsAMembersCatalogOnTheirProvider(t *testing.T) {
	path := tempConfig(t, "config.toml")
	in := "model_provider = \"openai\"\nmodel_catalog_json = \"/mine.json\"\n\n[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	doc, _ := codexTOML(readFile(t, path))
	if doc["model_catalog_json"] != "/mine.json" {
		t.Errorf("the member's catalogue was taken:\n%s", readFile(t, path))
	}
}

// An [agents] table the member wrote with more in it than ours keeps the rest.
func TestCodexRemovalKeepsAMembersAgentsTable(t *testing.T) {
	path := tempConfig(t, "config.toml")
	in := "model = \"m\"\nmodel_provider = \"nan\"\n\n[agents]\nenabled = false\nmax_threads = 4\n\n" +
		"[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	doc, ok := codexTOML(got)
	if !ok || doc["model_provider"] != nil {
		t.Errorf("the root provider was not taken:\n%s", got)
	}
	if codexTOMLGet(doc, "agents.max_threads") != int64(4) {
		t.Errorf("the member's [agents] table was taken:\n%s", got)
	}
	if codexTOMLGet(doc, "agents.enabled") != nil {
		t.Errorf("agents.enabled, ours, stayed:\n%s", got)
	}
}

// The catalogue this CLI wrote at the root goes on a disconnect, a path the
// member chose does not - and neither does the web search they chose.
func TestCodexRemovalKeepsAMembersOwnCatalogAndWebSearch(t *testing.T) {
	path := tempConfig(t, "config.toml")
	in := "model_provider = \"nan\"\nmodel_catalog_json = \"/mine.json\"\nweb_search = \"live\"\n\n" +
		"[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	doc, ok := codexTOML(got)
	if !ok || doc["model_provider"] != nil {
		t.Errorf("the root provider was not taken:\n%s", got)
	}
	if doc["model_catalog_json"] != "/mine.json" || doc["web_search"] != "live" {
		t.Errorf("the member's own values were taken:\n%s", got)
	}
}

// The refusal names every root key a hand edit has to take, not only the two
// the disconnect used to add.
func TestCodexRemovalRefusalListsEveryNaNRootKey(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := os.WriteFile(path, []byte(codexMultiLineModel), 0o600); err != nil {
		t.Fatal(err)
	}
	err := removeCodexConfig(path)
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, want := range []string{"model_provider", "model_catalog_json", "web_search", "[agents]", "[features]", "multi_agent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

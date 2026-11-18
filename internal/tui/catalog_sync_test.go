package tui

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
	catalog "github.com/nxssie/nan-cli/internal/models"
	"github.com/nxssie/nan-cli/internal/session"
)

// platformClient is a client against a fake platform that answers
// /agents/models with the given status and body, and records what it was
// asked.
func platformClient(t *testing.T, token string, status int, body string) (*api.Client, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return api.New(token).WithBaseURL(srv.URL), &asked
}

func catalogBody(t *testing.T, models []api.AgentModel) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// filesUnder lists every regular file below dir.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// ── what each harness carries ────────────────────────────────────────────────

func TestEachHarnessIsWrittenFromTheAPICatalog(t *testing.T) {
	wantIDs := []string{"deepseek-v4-flash", "glm5.3", "kimi-k3.1-pro", "bare-model"}
	sameIDs := func(t *testing.T, got []string) {
		t.Helper()
		if len(got) != len(wantIDs) {
			t.Fatalf("wrote %v, want exactly %v", got, wantIDs)
		}
		have := map[string]bool{}
		for _, id := range got {
			have[id] = true
		}
		for _, id := range wantIDs {
			if !have[id] {
				t.Errorf("%s is missing from %v", id, got)
			}
		}
	}

	t.Run("Factory", func(t *testing.T) {
		path := tempConfig(t, "settings.json")
		if err := writeFactoryConfig(path, testKey, testModels); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, raw := range readJSON(t, path)["customModels"].([]any) {
			e := raw.(map[string]any)
			id := e["model"].(string)
			ids = append(ids, id)
			if e["displayName"] != id+" (NaN)" {
				t.Errorf("%s: displayName = %v", id, e["displayName"])
			}
			wantNoImage := id == "glm5.3" || id == "bare-model"
			if e["noImageSupport"] != wantNoImage {
				t.Errorf("%s: noImageSupport = %v, want %v", id, e["noImageSupport"], wantNoImage)
			}
		}
		sameIDs(t, ids)
	})

	t.Run("OpenCode", func(t *testing.T) {
		path := tempConfig(t, "opencode.json")
		if err := writeOpencodeConfig(path, testKey, testModels); err != nil {
			t.Fatal(err)
		}
		models := readJSON(t, path)["provider"].(map[string]any)["nan"].(map[string]any)["models"].(map[string]any)
		var ids []string
		for id := range models {
			ids = append(ids, id)
		}
		sameIDs(t, ids)
		limit := func(id string) (int, int) {
			l := models[id].(map[string]any)["limit"].(map[string]any)
			return int(l["context"].(float64)), int(l["output"].(float64))
		}
		if c, o := limit("kimi-k3.1-pro"); c != 262_144 || o != 65_536 {
			t.Errorf("kimi limit = %d/%d", c, o)
		}
		if c, o := limit("bare-model"); c != 131_072 || o != 8_192 {
			t.Errorf("bare-model limit = %d/%d, want the fallbacks", c, o)
		}
		input := func(id string) []any {
			return models[id].(map[string]any)["modalities"].(map[string]any)["input"].([]any)
		}
		if got := input("kimi-k3.1-pro"); len(got) != 2 || got[1] != "image" {
			t.Errorf("kimi input = %v", got)
		}
		if got := input("bare-model"); len(got) != 1 || got[0] != "text" {
			t.Errorf("bare-model input = %v", got)
		}
	})

	t.Run("Pi", func(t *testing.T) {
		path := tempConfig(t, "models.json")
		if err := writePiConfig(path, testKey, testModels); err != nil {
			t.Fatal(err)
		}
		written := piModels(t, path)
		var ids []string
		for id := range written {
			ids = append(ids, id)
		}
		sameIDs(t, ids)
		kimi := written["kimi-k3.1-pro"]
		if int(kimi["contextWindow"].(float64)) != 262_144 || int(kimi["maxTokens"].(float64)) != 65_536 {
			t.Errorf("kimi = %v", kimi)
		}
		if kimi["reasoning"] != true || len(kimi["input"].([]any)) != 2 {
			t.Errorf("kimi reasoning/input = %v/%v", kimi["reasoning"], kimi["input"])
		}
		bare := written["bare-model"]
		if bare["reasoning"] != false || len(bare["input"].([]any)) != 1 {
			t.Errorf("bare-model reasoning/input = %v/%v, want false and text only", bare["reasoning"], bare["input"])
		}
		if int(bare["contextWindow"].(float64)) != 131_072 || int(bare["maxTokens"].(float64)) != 8_192 {
			t.Errorf("bare-model = %v, want the fallbacks", bare)
		}
	})

	t.Run("Codex", func(t *testing.T) {
		path := tempConfig(t, "config.toml")
		if err := writeCodexConfig(path, testKey, testModels); err != nil {
			t.Fatal(err)
		}
		home := filepath.Dir(path)
		var cat struct {
			Models []struct {
				Slug     string   `json:"slug"`
				Context  int      `json:"context_window"`
				Modality []string `json:"input_modalities"`
			} `json:"models"`
		}
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(home, codexCatalogFile))), &cat); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range cat.Models {
			ids = append(ids, m.Slug)
			switch m.Slug {
			case "kimi-k3.1-pro":
				if m.Context != 262_144 || len(m.Modality) != 2 {
					t.Errorf("kimi catalogue entry = %+v", m)
				}
			case "bare-model":
				if m.Context != 131_072 || len(m.Modality) != 1 || m.Modality[0] != "text" {
					t.Errorf("bare-model catalogue entry = %+v", m)
				}
			}
		}
		sameIDs(t, ids)
		kimi := readFile(t, filepath.Join(home, codexProfileName("kimi-k3.1-pro")+".config.toml"))
		if !strings.Contains(kimi, "model_context_window = 262144") {
			t.Errorf("kimi profile window:\n%s", kimi)
		}
		bare := readFile(t, filepath.Join(home, codexProfileName("bare-model")+".config.toml"))
		if !strings.Contains(bare, "model_context_window = 131072") {
			t.Errorf("bare-model profile window:\n%s", bare)
		}
		var profiles int
		for _, f := range filesUnder(t, home) {
			if strings.HasPrefix(filepath.Base(f), "nan-") && strings.HasSuffix(f, ".config.toml") {
				profiles++
			}
		}
		if profiles != len(wantIDs) {
			t.Errorf("%d profiles, want %d", profiles, len(wantIDs))
		}
	})

	t.Run("Hermes", func(t *testing.T) {
		toolHome(t)
		calls := recordHermes(t)
		if err := writeHermesConfig(t.TempDir(), testKey, testModels); err != nil {
			t.Fatal(err)
		}
		var def string
		for _, c := range *calls {
			if len(c) == 3 && c[1] == "model.default" {
				def = c[2]
			}
		}
		if def != testModels[0].ID {
			t.Errorf("model.default = %q, want %q", def, testModels[0].ID)
		}
	})
}

// ── Setup reads the catalogue from the account and fails closed ──────────────

func TestSetupSignedOutAsksToSignInAndWritesNothing(t *testing.T) {
	home := toolHome(t)
	m := newModel(nil, &session.Session{APIKey: testKey})
	m.lay = newLayout(90, 30)
	m.active = tabIndex(tabSetup)

	m, _ = pressKey(m, 'c')

	if m.loginStage == loginOff {
		t.Error("c with no session did not open the sign-in")
	}
	if m.configuring {
		t.Error("c started configuring with no session")
	}
	if !strings.Contains(m.View(), "sign in first") {
		t.Errorf("the sign-in does not say why it opened:\n%s", m.View())
	}
	if files := filesUnder(t, home); len(files) != 0 {
		t.Errorf("signed out, Setup wrote %v", files)
	}
}

func TestSetupWritesNothingWithoutACatalog(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"server error": {500, `{"error":"boom"}`},
		"empty list":   {200, `{"models":[]}`},
	} {
		t.Run(name, func(t *testing.T) {
			home := toolHome(t)
			paths := toolPaths(home)
			for _, p := range paths {
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// OpenCode is already configured and must stay so.
			if err := writeOpencodeConfig(paths["OpenCode"], testKey, testModels); err != nil {
				t.Fatal(err)
			}
			before := filesUnder(t, home)
			opencode := readFile(t, paths["OpenCode"])

			client, _ := platformClient(t, "tok", tc.status, tc.body)
			msg, ok := syncTools(client, testKey, nil).(configuredMsg)
			if !ok {
				t.Fatal("syncTools did not answer with a configuredMsg")
			}
			if !strings.HasPrefix(msg.msg, "error:") || !strings.Contains(msg.msg, "nothing was written") {
				t.Errorf("message = %q", msg.msg)
			}
			if len(msg.written) != 0 || len(msg.failed) != 0 || msg.expired != "" {
				t.Errorf("unexpected result: %+v", msg)
			}
			after := filesUnder(t, home)
			if len(after) != len(before) {
				t.Errorf("files before %v, after %v", before, after)
			}
			if readFile(t, paths["OpenCode"]) != opencode || !isNaNConfigured("OpenCode", paths["OpenCode"]) {
				t.Error("a configured OpenCode was changed or removed without a catalogue")
			}
		})
	}
}

func TestSetupWritesWhatThePlatformServes(t *testing.T) {
	home := toolHome(t)
	paths := toolPaths(home)
	if err := os.MkdirAll(filepath.Dir(paths["OpenCode"]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths["OpenCode"], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	client, asked := platformClient(t, "tok", 200, catalogBody(t, testAPIModels()))

	msg := syncTools(client, testKey, nil).(configuredMsg)
	if strings.HasPrefix(msg.msg, "error:") {
		t.Fatalf("message = %q", msg.msg)
	}
	if len(*asked) != 1 || (*asked)[0] != "/agents/models" {
		t.Errorf("asked %v, want only the chat-only list", *asked)
	}
	models := readJSON(t, paths["OpenCode"])["provider"].(map[string]any)["nan"].(map[string]any)["models"].(map[string]any)
	if _, ok := models["kimi-k3.1-pro"]; !ok {
		t.Errorf("a model the old list never had was not written: %v", models)
	}
}

func TestSetupWithAnExpiredSessionSignsOut(t *testing.T) {
	toolHome(t)
	m := newModel(nil, &session.Session{Token: "stale", APIKey: testKey})
	client, _ := platformClient(t, "stale", http.StatusUnauthorized, `{"error":"unauthorized"}`)
	m.client = client
	m.configuring = true

	msg, ok := syncTools(client, testKey, nil).(configuredMsg)
	if !ok || msg.expired != "stale" {
		t.Fatalf("syncTools = %+v, want expired = the token it asked with", msg)
	}
	updated, _ := m.Update(msg)
	after := updated.(model)
	if after.sess.Token != "" {
		t.Error("the refused session is still held")
	}
	if after.configuring {
		t.Error("the spinner is still on")
	}
	if after.sess.APIKey != testKey {
		t.Error("the key went with the session")
	}
}

func TestConfigureToolsRefusesAnEmptyCatalog(t *testing.T) {
	home := toolHome(t)
	paths := toolPaths(home)
	if err := os.MkdirAll(filepath.Dir(paths["OpenCode"]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeConfig(paths["OpenCode"], testKey, testModels); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, paths["OpenCode"])
	files := filesUnder(t, home)

	msg, written, failed := configureTools(testKey, nil, nil)
	if !strings.HasPrefix(msg, "error:") || !strings.Contains(msg, "nothing was written") {
		t.Errorf("message = %q", msg)
	}
	if len(written) != 0 || len(failed) != 0 {
		t.Errorf("written %v, failed %v", written, failed)
	}
	if readFile(t, paths["OpenCode"]) != before || len(filesUnder(t, home)) != len(files) {
		t.Error("an empty catalogue changed the machine")
	}
}

func TestModelsTabSignsOutOnAnExpiredSessionEvenWithAKey(t *testing.T) {
	m := setupModel(t, &session.Session{Token: "stale", APIKey: testKey})
	client, _ := platformClient(t, "stale", http.StatusUnauthorized, `{"error":"unauthorized"}`)
	m.client = client

	msg := m.fetchTabData(tabModels)()
	e, ok := msg.(fetchErrMsg)
	if !ok || e.err != api.ErrSessionExpired {
		t.Fatalf("fetchTabData answered %#v, want fetchErrMsg{ErrSessionExpired}", msg)
	}
	wrapped := m.fetchTab(tabModels)()
	if got, ok := wrapped.(sessionExpiredMsg); !ok || got.token != "stale" {
		t.Errorf("the wrapper answered %#v, want sessionExpiredMsg", wrapped)
	}
}

// ── the Codex root window ────────────────────────────────────────────────────

func TestCodexWindowCarriesTheMark(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	want := "model_context_window = 1048575 # nan-cli"
	if got := readFile(t, path); !strings.Contains(got, want) {
		t.Errorf("starter file has no marked window %q:\n%s", want, got)
	}

	// Connecting again changes nothing.
	first := readFile(t, path)
	if err := writeCodexConfig(path, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != first {
		t.Errorf("connecting twice changed the file:\n%s\n---\n%s", first, got)
	}

	// Same for an existing file that gets the section appended.
	other := tempConfig(t, "config.toml")
	if err := os.WriteFile(other, []byte("model = \"kimi-k3.1-pro\"\nmodel_provider = \"nan\"\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(other, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	second := readFile(t, other)
	if err := writeCodexConfig(other, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, other); got != second {
		t.Errorf("connecting twice changed an existing file:\n%s\n---\n%s", second, got)
	}
}

func TestCodexRemovalTakesALegacyUnmarkedWindow(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"gpt-5\"\nmodel_context_window = 1048576\n\n[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\nwire_api = \"responses\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "model_context_window") || !strings.Contains(got, `model = "gpt-5"`) {
		t.Errorf("legacy window not taken, or too much taken:\n%s", got)
	}
}

func TestCodexRemovalKeepsAMemberWindowWithTheLegacyValueButAComment(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"gpt-5\"\nmodel_context_window = 1048576 # mine\n\n[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\nwire_api = \"responses\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.Contains(got, "model_context_window = 1048576 # mine") {
		t.Errorf("the member's own window was taken:\n%s", got)
	}
}

func TestCodexRemovalSweepsAnOrphanWindow(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"gpt-5\"\nmodel_context_window = 1 # nan-cli\nsandbox_mode = \"read-only\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	want := "model = \"gpt-5\"\nsandbox_mode = \"read-only\"\n"
	if got := readFile(t, path); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Removal runs on every sign-out, against files that may never have been ours.
// Without our section an unmarked 1048576 is just as likely the member's own
// (a Gemini window is exactly that), so only the marked line counts there.
func TestCodexRemovalLeavesAnUnmarkedWindowInAFileThatWasNeverOurs(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"gemini-2.5-pro\"\nmodel_context_window = 1048576\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCodexConfig(path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != existing {
		t.Errorf("a file that was never ours changed:\n%s", got)
	}
}

// A file can mention api.nan.builders without carrying our section - a table
// the member wrote by hand, or a comment. That file takes the "already
// configured" path, and the member's own unmarked 1048576 must survive it.
func TestCodexConfigKeepsAnUnmarkedWindowInAFileThatOnlyMentionsUs(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"gpt-5\"\nmodel_provider = \"openai\"\nmodel_context_window = 1048576\n\n" +
		"[model_providers.nanbuilders]\nbase_url = \"https://api.nan.builders/v1\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.Contains(got, "model_context_window = 1048576\n") {
		t.Errorf("the member's own window was removed:\n%s", got)
	}
}

func TestCodexRootWindowFollowsTheRootModel(t *testing.T) {
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	// The root model's window changed, and the list came back in another order.
	changed := []catalog.Model{testModels[2], testModels[1], testModels[0], testModels[3]}
	changed[2].Context = 999_999
	if err := writeCodexConfig(path, testKey, changed); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if n := strings.Count(got, "model_context_window"); n != 1 {
		t.Fatalf("%d window lines:\n%s", n, got)
	}
	if !strings.Contains(got, "model_context_window = 999999 # nan-cli") {
		t.Errorf("the root window is not the root model's new one:\n%s", got)
	}
}

func TestCodexConfigWritesNoRootWindowForAModelThatIsNotOurs(t *testing.T) {
	roots := map[string]string{
		"openai model":        "model = \"gpt-5\"\n",
		"our id, other prov.": "model = \"glm5.3\"\nmodel_provider = \"openai\"\n",
		"our prov., no id":    "model = \"not-served\"\nmodel_provider = \"nan\"\n",
	}
	for name, root := range roots {
		for _, withSection := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/section=%v", name, withSection), func(t *testing.T) {
				path := tempConfig(t, "config.toml")
				content := root
				if withSection {
					content += "\n[model_providers.nan]\nbase_url = \"https://api.nan.builders/v1\"\nwire_api = \"responses\"\n"
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := writeCodexConfig(path, testKey, testModels); err != nil {
					t.Fatal(err)
				}
				rootPart, _, _ := strings.Cut(readFile(t, path), "[")
				if strings.Contains(rootPart, "model_context_window") {
					t.Errorf("a window was forced on a model that is not routed through us:\n%s", readFile(t, path))
				}
			})
		}
	}
}

func TestCodexExistingConfigSweepsAnOrphanWindow(t *testing.T) {
	path := tempConfig(t, "config.toml")
	existing := "model = \"kimi-k3.1-pro\"\nmodel_provider = \"nan\"\nmodel_context_window = 1048576\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexConfig(path, testKey, testModels); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if n := strings.Count(got, "model_context_window"); n != 1 {
		t.Fatalf("%d window lines, want one:\n%s", n, got)
	}
	if !strings.Contains(got, "model_context_window = 262144 # nan-cli") {
		t.Errorf("the surviving window is not the marked one:\n%s", got)
	}
}

func TestCodexProfilesSkipAnIdThatCollides(t *testing.T) {
	models := []catalog.Model{
		{ID: "a.b", Context: 111}, {ID: "ab", Context: 222},
		{ID: "Kimi", Context: 333}, {ID: "kimi", Context: 444},
	}
	path := tempConfig(t, "config.toml")
	if err := writeCodexConfig(path, testKey, models); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(path)
	var profiles []string
	for _, f := range filesUnder(t, home) {
		if strings.HasPrefix(filepath.Base(f), "nan-") && strings.HasSuffix(f, ".config.toml") {
			profiles = append(profiles, f)
		}
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles = %v, want one per distinct file name", profiles)
	}
	first := readFile(t, filepath.Join(home, "nan-ab.config.toml"))
	if !strings.Contains(first, `model = "a.b"`) {
		t.Errorf("the first model in API order lost its profile:\n%s", first)
	}
	if !strings.Contains(readFile(t, filepath.Join(home, codexCatalogFile)), `"ab"`) {
		t.Error("the model without a profile is not in the catalogue")
	}
}

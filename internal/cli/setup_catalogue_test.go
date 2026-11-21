package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catalogueEnv stands up the two servers Setup reads - the platform's model
// list, and the cluster's /v1/models for the key - with OpenCode "installed"
// (its config directory exists) and nothing else.
func catalogueEnv(t *testing.T, platformStatus int, platformBody, inferenceBody string) (home string) {
	t.Helper()
	home = fakeHome(t, `{"token":"session"}`)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The tool counts as installed when its config is there to be found.
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents/models" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(platformStatus)
		_, _ = w.Write([]byte(platformBody))
	}))
	t.Cleanup(platform.Close)
	swap(t, &apiBaseURL, platform.URL)

	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if inferenceBody == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(inferenceBody))
	}))
	t.Cleanup(cluster.Close)
	t.Setenv("NAN_INFERENCE_URL", cluster.URL+"/v1")
	return home
}

func openCodeModels(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("OpenCode was not configured: %v", err)
	}
	var ids []string
	for _, id := range []string{"glm5.3-flash", "deepseek-v4-flash", "pack-only-x", "glm5.3"} {
		if strings.Contains(string(data), `"`+id+`"`) {
			ids = append(ids, id)
		}
	}
	return ids
}

const clusterList = `{"data":[{"id":"glm5.3-flash"},{"id":"deepseek-v4-flash"}]}`

// The platform's list is the member's: a model it does not list - premium,
// pack-only, canary - is not configured, whatever this CLI knows about it.
func TestSetupConfiguresOnlyWhatThePlatformLists(t *testing.T) {
	home := catalogueEnv(t, 200, `{"models":[{"name":"glm5.3-flash"}]}`, clusterList)
	withStdin(t, "sk-good\n")
	if code, msg := execute(t, "setup", "--key", "-", "--all"); code != 0 {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if got := openCodeModels(t, home); len(got) != 1 || got[0] != "glm5.3-flash" {
		t.Errorf("configured %v, want only glm5.3-flash", got)
	}
}

// The platform being down, or answering something that is not a model list,
// does not stop a key that works: Setup falls back to what the key itself can
// call.
func TestSetupFallsBackToTheKeysOwnModelsWhenThePlatformCannotAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"a 500":        {500, `boom`},
		"html":         {200, `<html>maintenance</html>`},
		"empty object": {200, `{}`},
		"null models":  {200, `{"models":null}`},
		"no usable id": {200, `{"models":[{"name":"../etc"},{"name":""}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			home := catalogueEnv(t, c.status, c.body, clusterList)
			withStdin(t, "sk-good\n")
			if code, msg := execute(t, "setup", "--key", "-", "--all"); code != 0 {
				t.Fatalf("exit %d: %s", code, msg)
			}
			if got := openCodeModels(t, home); len(got) != 2 {
				t.Errorf("configured %v, want the key's two models", got)
			}
		})
	}
}

// With no model list from anyone, nothing is written - and the member is told
// so, in words, with the next step. The key is kept for the next run.
func TestSetupWritesNothingWithoutAModelList(t *testing.T) {
	home := catalogueEnv(t, 500, `boom`, "")
	withStdin(t, "sk-good\n")
	code, msg := execute(t, "setup", "--key", "-", "--all")
	if code == 0 {
		t.Fatalf("exit 0: setup claimed success with no models: %s", msg)
	}
	for _, want := range []string{"nothing was written", "run nan setup again"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp") {
		t.Errorf("a raw network error reached the member: %q", msg)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if strings.TrimSpace(string(data)) != "{}" {
		t.Errorf("a config was written from no model list: %s", data)
	}
	if sess, _ := readSession(t, home); sess["apiKey"] != "sk-good" {
		t.Errorf("the key was not kept: %v", sess)
	}
}

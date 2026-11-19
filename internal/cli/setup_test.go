package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inference stands in for the cluster's /v1/models, answering with status
// (and a model list when it is 200).
func inference(t *testing.T, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-5"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("NAN_INFERENCE_URL", srv.URL+"/v1")
	t.Setenv("PATH", t.TempDir()) // no tool installed: only the key is saved
}

func TestSetupRefusesAKeyTheClusterRefused(t *testing.T) {
	home := fakeHome(t, "")
	inference(t, http.StatusUnauthorized)
	withStdin(t, "sk-wrong\n")
	code, msg := execute(t, "setup", "--key", "-", "--all")
	if code == 0 || !strings.Contains(msg, "refused this key") {
		t.Fatalf("exit %d, %q: a refused key was accepted", code, msg)
	}
	if sess, _ := readSession(t, home); sess["apiKey"] != nil {
		t.Errorf("a refused key was saved: %v", sess)
	}
}

// A cluster that cannot be reached says nothing about the key. The
// dashboard lets it through, and so does the command: refusing it was
// telling somebody offline that their key was wrong.
func TestSetupKeepsAKeyItCouldNotCheck(t *testing.T) {
	for name, set := range map[string]func(t *testing.T){
		"unreachable": func(t *testing.T) {
			t.Setenv("NAN_INFERENCE_URL", "http://127.0.0.1:1/v1")
			t.Setenv("PATH", t.TempDir())
		},
		"a 503": func(t *testing.T) { inference(t, http.StatusServiceUnavailable) },
	} {
		t.Run(name, func(t *testing.T) {
			home := fakeHome(t, "")
			set(t)
			withStdin(t, "sk-offline\n")
			if code, msg := execute(t, "setup", "--key", "-", "--all"); code != 0 {
				t.Fatalf("exit %d, %q: an unchecked key was refused", code, msg)
			}
			if sess, _ := readSession(t, home); sess["apiKey"] != "sk-offline" {
				t.Errorf("the key was not saved: %v", sess)
			}
		})
	}
}

func TestSetupRemoveForgetsTheKey(t *testing.T) {
	home := fakeHome(t, `{"token":"t","apiKey":"sk-gone","enabledTools":{"pi":true}}`)
	t.Setenv("PATH", t.TempDir())
	if code, msg := execute(t, "setup", "remove"); code != 0 {
		t.Fatalf("exit %d, %q", code, msg)
	}
	sess, _ := readSession(t, home)
	if sess["apiKey"] != nil || sess["enabledTools"] != nil {
		t.Errorf("remove left %v", sess)
	}
	if sess["token"] != "t" {
		t.Errorf("remove signed the machine out too: %v", sess)
	}
}

func TestSetupRemoveOnAFreshMachineWritesNothing(t *testing.T) {
	home := fakeHome(t, "")
	t.Setenv("PATH", t.TempDir())
	if code, msg := execute(t, "setup", "remove"); code != 0 {
		t.Fatalf("exit %d, %q", code, msg)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "nan", "session.json")); err == nil {
		t.Error("remove created a session file on a machine that had none")
	}
}

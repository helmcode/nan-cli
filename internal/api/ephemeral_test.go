package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden responses in testdata/ephemeral were rendered by cloud-api's
// own handlers (renderRun, renderAutomation, EphemeralHandler on
// helmcode/nan-cloud-api main), so this client decodes exactly what the
// platform serialises for ephemeral runs.
func ephemeralGolden(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ephemeral", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func TestGoldenEphemeralRun(t *testing.T) {
	var run Run
	ephemeralGolden(t, "run_get_ephemeral.json", &run)
	if !run.Ephemeral() || run.Size == nil || *run.Size != SizeM || run.Workspace.ID != "" || run.Workspace.Name != "" ||
		run.GitHubRepo == nil || *run.GitHubRepo != "acme/api" || run.Cost == nil ||
		run.Cost.EURMicros != 18000 || run.Cost.EUR != 0.018 || run.Cost.BilledSeconds != 180 {
		t.Fatalf("run = %+v (cost %+v)", run, run.Cost)
	}
}

func TestGoldenEphemeralRunList(t *testing.T) {
	var list RunList
	ephemeralGolden(t, "run_list_ephemeral.json", &list)
	if len(list.Data) != 3 {
		t.Fatalf("list = %+v", list)
	}
	metered, unmetered, ws := list.Data[0], list.Data[1], list.Data[2]
	if !metered.Ephemeral() || metered.Cost == nil || !unmetered.Ephemeral() || unmetered.Cost != nil {
		t.Errorf("ephemeral runs: %+v / %+v", metered, unmetered)
	}
	if ws.Ephemeral() || ws.Executor != ExecutorWorkspace || ws.Size != nil || ws.Cost != nil || ws.Workspace.Name != "develop" {
		t.Errorf("workspace run: %+v", ws)
	}
}

func TestGoldenEphemeralAutomation(t *testing.T) {
	var a Automation
	ephemeralGolden(t, "automation_get_ephemeral.json", &a)
	if a.Executor != ExecutorEphemeral || a.Size == nil || *a.Size != SizeL || a.Workspace != nil || a.Checkout != "fresh" {
		t.Fatalf("automation = %+v", a)
	}
}

func TestGoldenEphemeralUsageAndSettings(t *testing.T) {
	var u EphemeralUsage
	ephemeralGolden(t, "ephemeral_usage.json", &u)
	if u.Month != "2026-10" || u.Minutes != 49 || u.Seconds != 2950 || u.Plan != "inference" || u.BillingEnabled ||
		u.FreeEURMicrosTotal != 900000 || u.FreeEURMicrosUsed != 159000 || u.FreeEURMicrosLeft != 741000 || u.FreeMinutesLeft != 247 ||
		u.MonthlyCapEURCents != 10000 || u.PricesEURMicrosPerMinute[SizeS] != 3000 || u.PricesEURMicrosPerMinute[SizeM] != 6000 ||
		u.PricesEURMicrosPerMinute[SizeL] != 12000 {
		t.Fatalf("usage = %+v", u)
	}
	var s EphemeralSettings
	ephemeralGolden(t, "ephemeral_settings.json", &s)
	if s.MonthlyCapEURCents != 10000 || s.MonthlyCapEUR != 100 || s.MaxConcurrent != 50 || s.BillingEnabled ||
		s.Limits.MonthlyCapEURCentsMax != 100000 || s.Limits.MaxConcurrentMax != 50 {
		t.Fatalf("settings = %+v", s)
	}
}

// An ephemeral run names no workspace: the platform refuses one ("an
// ephemeral run has no workspace"), so the field must be left out.
func TestCreateEphemeralRunBody(t *testing.T) {
	repo := "acme/api"
	model := "glm5.3"
	b, err := json.Marshal(CreateRunRequest{Agent: "pi", Prompt: "x", Model: &model, Executor: ExecutorEphemeral, Size: SizeM, GitHubRepo: &repo})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if _, ok := got["workspace"]; ok {
		t.Errorf("an ephemeral run sends a workspace: %s", b)
	}
	if got["executor"] != "ephemeral" || got["size"] != "m" || got["github_repo"] != "acme/api" || got["model"] != "glm5.3" {
		t.Errorf("body = %s", b)
	}
	// A workspace run keeps its shape: no executor, size or repository.
	b, _ = json.Marshal(CreateRunRequest{Workspace: "develop", Agent: "pi", Prompt: "x"})
	if s := string(b); !strings.Contains(s, `"workspace":"develop"`) || strings.Contains(s, "executor") ||
		strings.Contains(s, "size") || strings.Contains(s, "github_repo") {
		t.Errorf("workspace body = %s", s)
	}
}

func TestEphemeralClientPathsAndSession(t *testing.T) {
	var paths, cookies []string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		cookies = append(cookies, r.Header.Get("Cookie"))
		name := "ephemeral_settings.json"
		if strings.HasSuffix(r.URL.Path, "/usage") {
			name = "ephemeral_usage.json"
		}
		b, _ := os.ReadFile(filepath.Join("testdata", "ephemeral", name))
		_, _ = w.Write(b)
	}), "sess", "")
	ctx := context.Background()
	if u, err := c.EphemeralUsage(ctx); err != nil || u.FreeMinutesLeft != 247 {
		t.Fatalf("usage: %+v %v", u, err)
	}
	if s, err := c.EphemeralSettings(ctx); err != nil || s.MaxConcurrent != 50 {
		t.Fatalf("settings: %+v %v", s, err)
	}
	if strings.Join(paths, ",") != "GET /api/ephemeral/usage,GET /api/ephemeral/settings" {
		t.Errorf("paths = %v", paths)
	}
	for _, ck := range cookies {
		if ck != "nan_session=sess" {
			t.Errorf("cookie = %q", ck)
		}
	}
}

// Off for the member: the platform answers 404 {"error":"not_found"}; a
// token gets 403 {"error":"session_required"}. Both reach the caller as an
// APIError with the status, which is what it branches on.
func TestEphemeralClientRefusals(t *testing.T) {
	for status, body := range map[int]string{404: `{"error":"not_found"}`, 403: `{"error":"session_required"}`} {
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}), "", "nan_pat_x")
		_, err := c.EphemeralUsage(context.Background())
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Errorf("%d: %v", status, err)
		}
	}
}

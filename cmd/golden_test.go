package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

// goldenEnv serves cloud-api's own golden responses (copied into
// internal/api/testdata/phase2) on the paths that return them.
func goldenEnv(t *testing.T) (*runsEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	const run = "6f1c2a9b-0000-4000-8000-000000000001"
	const auto = "a0000000-0000-4000-8000-000000000001"
	routes := map[string]string{
		"/v1/runs/" + run:                         "run_get_propose.json",
		"/v1/approvals":                           "approvals_list.json",
		"/v1/automations":                         "automations_list.json",
		"/v1/automations/" + auto:                 "automation_get.json",
		"/v1/automations/" + auto + "/triggers":   "automation_triggers.json",
		"/v1/automations/" + auto + "/deliveries": "automation_deliveries.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := routes[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "internal", "api", "testdata", "phase2", name))
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	env := &runsEnv{client: api.NewRunsClient("s", "").WithBaseURL(srv.URL), stdout: &out, stderr: &errOut}
	return env, &out, &errOut
}

func wantAll(t *testing.T, what, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, out)
		}
	}
}

func TestGoldenRender(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	const run = "6f1c2a9b-0000-4000-8000-000000000001"
	const ws = "11111111-2222-4333-8444-555555555555"
	ctx := context.Background()

	env, out, _ := goldenEnv(t)
	if err := doRunsShow(ctx, env, run); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "runs show", out.String(), "awaiting_approval", "propose", "a0000000-0000-4000-8000-000000000001",
		"acme/api", "GITHUB_TOKEN (the agent)", "pending", "3 actions", "d0000000-0000-4000-8000-000000000004",
		"github_pr_review ok", "nan runs reject "+run, "/workspaces/"+ws+"/runs/"+run)

	env, out, _ = goldenEnv(t)
	if err := doRunsProposal(ctx, env, run, false, false); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "runs proposal", out.String(), "waiting for your approval", "[automatic]", "[needs approval]",
		"2. github_pr_comment  acme/api#42  [automatic]", "<img onerror=x> looks fine",
		"3. github_pr_review  acme/api#42  COMMENT  [needs approval]",
		"+3 -1", "a.go", "/workspaces/"+ws+"/runs/"+run)
	// approval_from 2: only the review waits, the push is automatic, so no
	// diff is stored and none is offered.
	if strings.Contains(out.String(), "--diff") {
		t.Errorf("runs proposal offers a diff that is not stored:\n%s", out.String())
	}

	env, out, _ = goldenEnv(t)
	if err := doApprovalsLs(ctx, env, api.ApprovalPending); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "approvals ls", out.String(), run, "pending", "a0000000-0000-4000-8000-000000000001", "github_pr_review")
	if strings.Contains(out.String(), "github_pr_comment") {
		t.Errorf("approvals ls lists an action that applied automatically:\n%s", out.String())
	}

	env, out, _ = goldenEnv(t)
	if err := doAutomationsLs(ctx, env); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "automations ls", out.String(), "PR review api", ws, "enabled", "github_pr_review")

	env, out, _ = goldenEnv(t)
	if err := doAutomationsShow(ctx, env, "PR review api"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "automations show", out.String(), "glm5.3", "GITHUB_TOKEN ← GITHUB_WRITE_TOKEN (actions only, never the agent)",
		"webhook github · nan_whk_AbC1…", "Review for correctness.", "https://cloud.nan.builders/automations/a0000000-0000-4000-8000-000000000001")

	env, out, _ = goldenEnv(t)
	if err := doAutomationsDeliveries(ctx, env, "PR review api"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "deliveries", out.String(), "run_created", run, "72d3162e-cc78-11e3-81ab-4c9367dc0958")
}

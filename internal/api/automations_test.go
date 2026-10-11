package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const automationID = "a0000000-0000-4000-8000-000000000001"

// The Phase 2 fields of a run (AGENT-AUTOMATIONS-PHASE2 §4.4, §7.6, §10).
func TestRunDecodesPhase2Fields(t *testing.T) {
	body := `{"id":"r1","state":"awaiting_approval","phase":"propose","inference_source":"nan",
	"automation":{"id":"a1","name":"PR review"},"parent_run_id":null,"apply_run_ids":["x1","x2"],
	"approval":{"run_id":"r1","state":"pending","approval_from":1,"expires_at":"2026-10-14T12:00:00Z",
	"proposal":{"v":1,"actions":[{"kind":"git_push","base":"main","branch":"fix/x","commit":"abc","commit_message":"fix","force":true},
	{"kind":"github_add_labels","repo":"acme/api","number":7,"labels":["bug"]}],"base":"main","merge_base":"m",
	"bundle_sha256":"b","diff_sha256":"d","diffstat":{"files":[{"path":"a.go","added":3,"deleted":1,"binary":false}],"files_changed":1,"insertions":3,"deletions":1,"truncated":false}},
	"proposal_sha256":"ff","has_diff":true,"diff_masked":true,"decided_at":null,"prefix_apply_run_id":"x1","apply_run_id":null}}`
	var run Run
	if err := json.Unmarshal([]byte(body), &run); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "propose" || run.InferenceSource != "nan" || run.Automation.Label() != "PR review" || len(run.ApplyRunIDs) != 2 {
		t.Errorf("run = %+v", run)
	}
	a := run.Approval
	if a == nil || a.State != ApprovalPending || *a.ApprovalFrom != 1 || !a.HasDiff || !a.DiffMasked || *a.PrefixApplyRunID != "x1" {
		t.Fatalf("approval = %+v", a)
	}
	p := a.Proposal
	if len(p.Actions) != 2 || !p.Actions[0].Force || p.Actions[1].Labels[0] != "bug" || p.Diffstat.Files[0].Added != 3 {
		t.Errorf("proposal = %+v", p)
	}
}

func TestRefAcceptsAnIDOrAnObject(t *testing.T) {
	var v struct {
		A *Ref `json:"a"`
		B *Ref `json:"b"`
		C *Ref `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":"id-1","b":{"id":"id-2","name":"two"},"c":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A.ID != "id-1" || v.A.Label() != "id-1" || v.B.Label() != "two" || v.C != nil || v.C.Label() != "" {
		t.Errorf("%+v %+v %+v", v.A, v.B, v.C)
	}
}

func TestListApprovalsQuery(t *testing.T) {
	var path, query string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[{"run_id":"r1","state":"pending","run_state":"awaiting_approval","automation":"a1"}],"next_cursor":null}`)
	}), "", "nan_pat_x")
	list, err := c.ListApprovals(context.Background(), ListApprovalsParams{State: "pending", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/approvals" || query != "limit=5&state=pending" {
		t.Errorf("GET %s?%s", path, query)
	}
	if len(list.Data) != 1 || list.Data[0].RunState != "awaiting_approval" || list.Data[0].Automation.ID != "a1" {
		t.Errorf("list = %+v", list)
	}
}

func TestRejectRunSendsTheReason(t *testing.T) {
	var method, path string
	var body map[string]any
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, runJSON("cancelled"))
	}), "sess", "")
	run, err := c.RejectRun(context.Background(), runID, "not now")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/v1/runs/"+runID+"/reject" || body["reason"] != "not now" || run.State != "cancelled" {
		t.Errorf("%s %s %v -> %+v", method, path, body, run)
	}

	// No reason: an empty object, not "reason":"".
	body = nil
	_, _ = c.RejectRun(context.Background(), runID, "")
	if _, ok := body["reason"]; ok {
		t.Errorf("empty reason was sent: %v", body)
	}
}

func TestRejectRunNotAwaitingIsAnAPIError(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"message":"the run is not awaiting approval","type":"invalid_request_error","code":"not_awaiting_approval"}}`)
	}), "sess", "")
	_, err := c.RejectRun(context.Background(), runID, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "not_awaiting_approval" {
		t.Fatalf("err = %#v", err)
	}
}

func TestProposalDiff(t *testing.T) {
	ctype := "text/plain; charset=utf-8"
	body := "diff --git a/a b/a\n+x\n"
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/"+runID+"/proposal.diff" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		_, _ = io.WriteString(w, body)
	}), "sess", "")
	got, err := c.ProposalDiff(context.Background(), runID)
	if err != nil || got != body {
		t.Fatalf("%q %v", got, err)
	}

	ctype = "text/html"
	if _, err := c.ProposalDiff(context.Background(), runID); err == nil {
		t.Error("an HTML answer was taken for a diff")
	}

	ctype = "text/plain"
	body = strings.Repeat("x", maxDiff+1)
	if _, err := c.ProposalDiff(context.Background(), runID); err == nil {
		t.Error("an oversized diff was accepted")
	}
}

func TestAutomationReads(t *testing.T) {
	var paths []string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/v1/automations":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"`+automationID+`","name":"PR review","workspace":"develop","enabled":true,"approval_kinds":["git_push"]}]}`)
		case "/v1/automations/" + automationID:
			_, _ = io.WriteString(w, `{"id":"`+automationID+`","name":"PR review","workspace":{"id":"w1","name":"develop"},"secrets":[{"secret":"GH","env":"GITHUB_TOKEN","exposure":"actions"}]}`)
		case "/v1/automations/" + automationID + "/triggers":
			_, _ = io.WriteString(w, `{"data":[{"id":"t1","kind":"schedule","enabled":true,"cron":"0 9 * * 1-5","timezone":"Europe/Madrid"}]}`)
		case "/v1/automations/" + automationID + "/deliveries":
			_, _ = io.WriteString(w, `{"data":[{"delivery_id":"d1","received_at":"2026-10-10T12:00:00Z","outcome":"run_created","run_id":"r1"}]}`)
		default:
			http.NotFound(w, r)
		}
	}), "", "nan_pat_x")
	ctx := context.Background()
	list, err := c.ListAutomations(ctx)
	if err != nil || len(list.Data) != 1 || list.Data[0].Workspace.Label() != "develop" {
		t.Fatalf("%+v %v", list, err)
	}
	a, err := c.GetAutomation(ctx, automationID)
	if err != nil || a.Workspace.Label() != "develop" || a.Secrets[0].Exposure != "actions" {
		t.Fatalf("%+v %v", a, err)
	}
	tr, err := c.ListTriggers(ctx, automationID)
	if err != nil || *tr.Data[0].Cron != "0 9 * * 1-5" {
		t.Fatalf("%+v %v", tr, err)
	}
	d, err := c.ListDeliveries(ctx, automationID, 50)
	if err != nil || d.Data[0].Outcome != "run_created" {
		t.Fatalf("%+v %v", d, err)
	}
	if paths[3] != "/v1/automations/"+automationID+"/deliveries?limit=50" {
		t.Errorf("paths = %q", paths)
	}
}

func TestRunAutomation(t *testing.T) {
	var idem string
	var raw []byte
	status := http.StatusCreated
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/automations/"+automationID+"/runs" {
			http.NotFound(w, r)
			return
		}
		idem = r.Header.Get("Idempotency-Key")
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, runJSON("queued"))
	}), "", "nan_pat_x")
	run, replayed, err := c.RunAutomation(context.Background(), automationID, json.RawMessage(`{"pr":42}`), "k1")
	if err != nil || replayed || run.ID != runID {
		t.Fatalf("%+v %v %v", run, replayed, err)
	}
	if idem != "k1" || string(raw) != `{"input":{"pr":42}}` {
		t.Errorf("idem %q body %s", idem, raw)
	}

	status = http.StatusOK
	_, replayed, _ = c.RunAutomation(context.Background(), automationID, nil, "")
	if !replayed || string(raw) != `{}` {
		t.Errorf("replayed %v body %s", replayed, raw)
	}
}

func TestListSecretsUsesTheSession(t *testing.T) {
	var cookie, path string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, path = r.Header.Get("Cookie"), r.URL.Path
		_, _ = io.WriteString(w, `{"data":[{"id":"s1","object":"secret","name":"GITHUB_TOKEN","kind":"generic","provider":null,"hint":"…a1b2","value_bytes":93,"description":"d","used_by":[{"id":"a1","name":"PR review"}],"created_at":"2026-10-10T12:00:00Z","updated_at":"2026-10-10T12:00:00Z","last_used_at":null}]}`)
	}), "sess", "")
	list, err := c.ListSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "nan_session=sess" || path != "/api/account/secrets" {
		t.Errorf("%q %q", cookie, path)
	}
	if len(list.Data) != 1 || *list.Data[0].Hint != "…a1b2" || list.Data[0].UsedBy[0].Name != "PR review" {
		t.Errorf("%+v", list)
	}
}

// /api's newer envelope carries the code in "error" and the text in
// "message"; the code is what callers branch on.
func TestBareEnvelopeWithCodeAndMessage(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"browser_session_required","message":"sign in in the browser","param":"x"}`)
	}), "sess", "")
	_, err := c.ListSecrets(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "browser_session_required" || apiErr.Message != "sign in in the browser" || apiErr.Param != "x" {
		t.Fatalf("err = %#v", err)
	}
}

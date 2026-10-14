package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/spf13/cobra"
)

// The Phase 2 contract (AGENT-AUTOMATIONS-PHASE2 §3.4, §4.4, §7.6, §11) as a
// test server, in front of fakePlatform for everything /v1/runs.
const (
	testAutoID = "a0000000-0000-4000-8000-000000000001"
	testWSID   = "0e000000-0000-4000-8000-0000000000aa"
)

type fakePhase2 struct {
	runs *fakePlatform

	mu            sync.Mutex
	status        int    // when set, every Phase 2 route answers it with errBody
	errBody       string // the error envelope for status
	automations   string // GET /v1/automations body
	runBody       map[string]any
	runIdem       string
	approvalsQ    string
	rejectBody    map[string]any
	rejectStatus  int
	runGet        string // GET /v1/runs/{id} body, overriding fakePlatform
	diff          string
	diffRequests  int
	secretsCookie string
	secretsAuthz  string
	paths         []string
}

const automationJSON = `{"id":"` + testAutoID + `","object":"automation","name":"PR review","description":"Reviews PRs\u001b[2J",
"template":"pr_review","workspace":{"id":"` + testWSID + `","name":"develop"},"agent":"pi","model":null,
"instructions":"Review for correctness.\n\u001b]0;pwn\u0007Be brief.","cwd":"/home/nan/projects/api","git_isolation":true,
"timeout_seconds":1800,"approval_kinds":["git_push","github_create_pr"],"approval_ttl_seconds":259200,
"inference":{"source":"nan"},"github_repo":"acme/api",
"secrets":[{"secret":"GITHUB_READ_TOKEN","env":"GITHUB_TOKEN","exposure":"agent"},{"secret":"GITHUB_WRITE_TOKEN","env":"GITHUB_TOKEN","exposure":"actions"}],
"max_runs_per_hour":30,"concurrency_policy":"skip_if_active","max_trigger_depth":2,"enabled":true,"disabled_reason":null,
"created_at":"2026-10-10T12:00:00Z","updated_at":"2026-10-10T12:00:00Z"}`

func (f *fakePhase2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.RequestURI())
	p := r.URL.Path
	phase2 := strings.HasPrefix(p, "/v1/automations") || strings.HasPrefix(p, "/v1/approvals") ||
		strings.HasPrefix(p, "/api/account/secrets") || strings.HasSuffix(p, "/reject") || strings.HasSuffix(p, "/proposal.diff")
	if phase2 && f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.errBody)
		f.mu.Unlock()
		return
	}
	switch {
	case r.Method == http.MethodGet && p == "/v1/automations":
		body := f.automations
		if body == "" {
			body = `{"object":"list","data":[` + automationJSON + `]}`
		}
		_, _ = io.WriteString(w, body)
	case r.Method == http.MethodGet && p == "/v1/automations/"+testAutoID:
		_, _ = io.WriteString(w, automationJSON)
	case r.Method == http.MethodGet && p == "/v1/automations/"+testAutoID+"/triggers":
		_, _ = io.WriteString(w, `{"data":[{"id":"t1","kind":"webhook","enabled":true,"token_prefix":"nan_whk_AbC1","signature_scheme":"github"},
		{"id":"t2","kind":"schedule","enabled":true,"cron":"0 9 * * 1-5","timezone":"Europe/Madrid","next_fire_at":"2026-10-12T07:00:00Z"}]}`)
	case r.Method == http.MethodGet && p == "/v1/automations/"+testAutoID+"/deliveries":
		_, _ = io.WriteString(w, `{"data":[{"delivery_id":"gh-\u001b[31m1","received_at":"2026-10-10T12:00:00Z","outcome":"run_created","run_id":"`+testRunID+`"},
		{"delivery_id":"gh-2","received_at":"2026-10-10T11:00:00Z","outcome":"ignored","run_id":null}]}`)
	case r.Method == http.MethodPost && p == "/v1/automations/"+testAutoID+"/runs":
		f.runIdem = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&f.runBody)
		w.WriteHeader(http.StatusCreated)
		// The platform names the automation by id only.
		_, _ = io.WriteString(w, strings.Replace(f.runs.run(api.StateQueued), `"object":"run",`, `"object":"run","automation_id":"`+testAutoID+`",`, 1))
	case r.Method == http.MethodGet && p == "/v1/approvals":
		f.approvalsQ = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[{"run_id":"`+testRunID+`","state":"pending","approval_from":1,
		"expires_at":"2026-10-14T12:00:00Z","proposal":{"v":1,"actions":[{"kind":"github_pr_comment","repo":"acme/api","number":4,"body":"x"},
		{"kind":"git_push","base":"main","branch":"b","commit":"c","commit_message":"m"},{"kind":"github_create_pr","repo":"acme/api","base":"main","head":"b","title":"t","body":"b"},
		{"kind":"git_push","base":"main","branch":"b2","commit":"c","commit_message":"m"}]},"proposal_sha256":"ff","has_diff":true,
		"run_state":"awaiting_approval","automation":{"id":"`+testAutoID+`","name":"PR review\u001b[31m"}}],"next_cursor":null}`)
	case r.Method == http.MethodPost && p == "/v1/runs/"+testRunID+"/reject":
		f.rejectBody = nil
		_ = json.NewDecoder(r.Body).Decode(&f.rejectBody)
		if f.rejectStatus != 0 {
			w.WriteHeader(f.rejectStatus)
			_, _ = io.WriteString(w, `{"error":{"message":"the run is not awaiting approval","type":"invalid_request_error","code":"not_awaiting_approval"}}`)
			break
		}
		_, _ = io.WriteString(w, strings.Replace(f.runs.run(api.StateCancelled), `"error_code":null`, `"error_code":"approval_rejected"`, 1))
	case r.Method == http.MethodGet && p == "/v1/runs/"+testRunID && f.runGet != "":
		_, _ = io.WriteString(w, f.runGet)
	case r.Method == http.MethodGet && p == "/v1/runs/"+testRunID+"/proposal.diff":
		f.diffRequests++
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, f.diff)
	case r.Method == http.MethodGet && p == "/api/account/secrets":
		f.secretsCookie, f.secretsAuthz = r.Header.Get("Cookie"), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[{"id":"s1","object":"secret","name":"GITHUB_TOKEN","kind":"generic","provider":null,"hint":"…a1b2",
		"value_bytes":93,"description":"fine-grained","used_by":[{"id":"`+testAutoID+`","name":"PR review"}],
		"created_at":"2026-10-10T12:00:00Z","updated_at":"2026-10-10T12:00:00Z","last_used_at":null},
		{"id":"s2","object":"secret","name":"STRIPE\u001b[31m","kind":"generic","provider":null,"hint":null,"value_bytes":20,"description":"",
		"used_by":[],"created_at":"2026-10-10T12:00:00Z","updated_at":"2026-10-10T12:00:00Z","last_used_at":"2026-10-11T08:00:00Z"}]}`)
	default:
		f.mu.Unlock()
		f.runs.ServeHTTP(w, r)
		return
	}
	f.mu.Unlock()
}

func newPhase2Harness(t *testing.T, fake *fakePhase2) *harness {
	t.Helper()
	if fake.runs == nil {
		fake.runs = &fakePlatform{}
	}
	h := newHarness(t, fake.runs)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client := api.NewRunsClient("session-token", "").WithBaseURL(srv.URL)
	client.Backoff = func(int) time.Duration { return time.Millisecond }
	client.IdleTimeout = 2 * time.Second
	h.env.client = client
	h.url = srv.URL
	return h
}

func resetPhase2Flags() {
	resetRunsFlags()
	autoLsJSON, autoShowJSON = false, false
	autoRunInput, autoRunDetach, autoRunJSON, autoRunIdemKey = "", false, false, ""
	autoDeliveryLimit, autoDeliveryJSON = 50, false
	approvalsState, approvalsLimit, approvalsJSON = api.ApprovalPending, 20, false
	secretsLsJSON = false
}

func assertNoControls(t *testing.T, what, out string) {
	t.Helper()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("%s carries control characters: %q", what, out)
	}
}

func TestAutomationsLs(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{})
	if err := doAutomationsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"NAME", "PR review", "develop", "enabled", "git_push, github_create_pr", testAutoID} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}

	h = newPhase2Harness(t, &fakePhase2{automations: `{"object":"list","data":[]}`})
	if err := doAutomationsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "https://cloud.nan.builders/automations") {
		t.Errorf("empty list does not point at the portal: %q", h.stderr.String())
	}

	h = newPhase2Harness(t, &fakePhase2{})
	autoLsJSON = true
	if err := doAutomationsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	var list api.AutomationList
	if err := json.Unmarshal(h.stdout.Bytes(), &list); err != nil || len(list.Data) != 1 || list.Data[0].ID != testAutoID {
		t.Errorf("--json: %v %q", err, h.stdout.String())
	}
	assertNoControls(t, "--json", h.stdout.String())
}

func TestAutomationsShowByName(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{}
	h := newPhase2Harness(t, fake)
	if err := doAutomationsShow(context.Background(), h.env, "PR review"); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{
		testAutoID, "PR review", "develop", "acme/api", "git_push, github_create_pr", "72h0m0s", "skip if active",
		"GITHUB_TOKEN ← GITHUB_READ_TOKEN (the agent)", "GITHUB_TOKEN ← GITHUB_WRITE_TOKEN (actions only, never the agent)",
		"webhook github · nan_whk_AbC1…", "schedule 0 9 * * 1-5 (Europe/Madrid)",
		"Review for correctness.", "Be brief.",
		"nan automations run " + testAutoID, "https://cloud.nan.builders/automations/" + testAutoID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	assertNoControls(t, "show", out)
	if fake.paths[0] != "GET /v1/automations" || fake.paths[1] != "GET /v1/automations/"+testAutoID {
		t.Errorf("paths = %q", fake.paths)
	}
}

func TestAutomationsShowByIDSkipsTheList(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{}
	h := newPhase2Harness(t, fake)
	if err := doAutomationsShow(context.Background(), h.env, testAutoID); err != nil {
		t.Fatal(err)
	}
	if fake.paths[0] != "GET /v1/automations/"+testAutoID {
		t.Errorf("paths = %q", fake.paths)
	}
}

func TestAutomationsUnknownName(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{})
	err := doAutomationsShow(context.Background(), h.env, "pr REVIEW")
	if exitCode(err) != exitUsage || !strings.Contains(err.Error(), `did you mean "PR review"`) {
		t.Fatalf("exit %d: %v", exitCode(err), err)
	}
	err = doAutomationsShow(context.Background(), h.env, "nope\x1b[2J")
	if exitCode(err) != exitUsage || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("exit %d: %q", exitCode(err), err)
	}
}

// A run that stops for approval exits 10 and says where to go next
// (AGENT-AUTOMATIONS-PHASE2 §9.4, finding 12).
func TestAutomationsRunExitsTenOnAwaitingApproval(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{runs: &fakePlatform{finalState: api.StateAwaitingApproval, events: []string{
		sse(1, "started", `{"agent":"pi","agent_version":"1.0.2","cwd":"/home/nan"}`),
		sse(2, "approval_required", `{"proposal_sha256":"abcdef0123456789","kinds":["git_push"]}`),
	}}}
	h := newPhase2Harness(t, fake)
	autoRunIdemKey = "retry-1"
	err := doAutomationsRun(context.Background(), h.env, "PR review", json.RawMessage(`{"pr":42}`))
	if exitCode(err) != exitAwaitingApproval || exitAwaitingApproval != 10 {
		t.Fatalf("exit %d (%v), want 10", exitCode(err), err)
	}
	if fake.runIdem != "retry-1" || fmt.Sprint(fake.runBody["input"]) != "map[pr:42]" {
		t.Errorf("request: idem %q body %v", fake.runIdem, fake.runBody)
	}
	out := h.stdout.String() + h.stderr.String()
	for _, want := range []string{"of PR review queued on develop", "approval required for: git_push", "awaiting approval", "nan runs proposal " + testRunID, "nan runs reject " + testRunID, "browser sign-in"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nan runs approve") {
		t.Error("the output names an approve command the CLI does not have")
	}
}

func TestAutomationsRunDetach(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{}
	h := newPhase2Harness(t, fake)
	autoRunDetach = true
	err := doAutomationsRun(context.Background(), h.env, testAutoID, nil)
	if exitCode(err) != exitDetached {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if strings.TrimSpace(h.stdout.String()) != testRunID {
		t.Errorf("stdout = %q", h.stdout.String())
	}
	if _, ok := fake.runBody["input"]; ok || fake.runs.streams != 0 {
		t.Errorf("body %v, streams %d", fake.runBody, fake.runs.streams)
	}
}

func TestAutomationsRunWithoutTheScope(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{status: http.StatusForbidden,
		errBody: `{"error":{"message":"this token lacks the automations:run scope","type":"permission_error","code":"token_scope_denied"}}`})
	h.env.cred = credential{kind: credEnv, bearer: testPAT}
	err := doAutomationsRun(context.Background(), h.env, testAutoID, nil)
	if exitCode(err) != exitAuth {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	for _, want := range []string{"automations:run", "Settings > Tokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), testPAT) {
		t.Error("the message shows the token")
	}
}

func TestAutomationsRunErrors(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	for code, want := range map[string]int{
		`{"error":{"message":"disabled","code":"automation_disabled"}}`:      exitConfig,
		`{"error":{"message":"limit","code":"automation_rate_limited"}}`:     exitUnavailable,
		`{"error":{"message":"active","code":"skipped_active"}}`:             exitUnavailable,
		`{"error":{"message":"no","code":"browser_session_required"}}`:       exitAuth,
		`{"error":{"message":"gone","type":"invalid_request_error"}}`:        exitUsage,
		`{"error":{"message":"not running","code":"workspace_not_running"}}`: exitUnavailable,
	} {
		status := http.StatusConflict
		if strings.Contains(code, "rate_limited") {
			status = http.StatusTooManyRequests
		}
		if strings.Contains(code, "browser") {
			status = http.StatusForbidden
		}
		if strings.Contains(code, "gone") {
			status = http.StatusNotFound
		}
		h := newPhase2Harness(t, &fakePhase2{status: status, errBody: code})
		err := doAutomationsRun(context.Background(), h.env, testAutoID, nil)
		if exitCode(err) != want {
			t.Errorf("%s: exit %d (%v), want %d", code, exitCode(err), err, want)
		}
	}
}

func TestReadAutomationInput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "in.json")
	if err := os.WriteFile(file, []byte("{\n  \"pr\": 42\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flag, stdin, want string
		code              int
	}{
		{"", "", "", 0},
		{"@" + file, "", `{"pr":42}`, 0},
		{"-", `{"a": [1, 2]}`, `{"a":[1,2]}`, 0},
		{`{"x": "y"}`, "", `{"x":"y"}`, 0},
		{`[1,2]`, "", "", exitUsage},
		{`"text"`, "", "", exitUsage},
		{`{"x":`, "", "", exitUsage},
		{"@" + filepath.Join(dir, "missing.json"), "", "", exitUsage},
		{`{"x":"` + strings.Repeat("a", maxInputBytes) + `"}`, "", "", exitUsage},
		{"-", "{\"x\":\"\xff\"}", "", exitUsage},
	} {
		got, err := readAutomationInput(promptInput{r: strings.NewReader(tc.stdin)}, tc.flag)
		if exitCode(err) != tc.code || string(got) != tc.want {
			t.Errorf("%.40q: got %q, exit %d (%v)", tc.flag, got, exitCode(err), err)
		}
	}
}

func TestAutomationsDeliveries(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{}
	h := newPhase2Harness(t, fake)
	autoDeliveryLimit = 10
	if err := doAutomationsDeliveries(context.Background(), h.env, testAutoID); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"OUTCOME", "run_created", testRunID, "ignored", "gh-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	assertNoControls(t, "deliveries", out)
	if fake.paths[0] != "GET /v1/automations/"+testAutoID+"/deliveries?limit=10" {
		t.Errorf("paths = %q", fake.paths)
	}
}

func TestApprovalsLs(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{}
	h := newPhase2Harness(t, fake)
	if err := doApprovalsLs(context.Background(), h.env, api.ApprovalPending); err != nil {
		t.Fatal(err)
	}
	if fake.approvalsQ != "limit=20&state=pending" {
		t.Errorf("query = %q", fake.approvalsQ)
	}
	out := h.stdout.String()
	// From approval_from on, distinct: the comment before it applied on its own.
	for _, want := range []string{testRunID, "pending", "PR review", "git_push, github_create_pr"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "github_pr_comment") || strings.Count(out, "git_push") != 1 {
		t.Errorf("waiting kinds wrong:\n%s", out)
	}
	assertNoControls(t, "approvals ls", out)
	for _, want := range []string{"nan runs proposal <run-id>", "https://cloud.nan.builders/automations?tab=approvals", "nan runs reject <run-id>"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("hints lack %q:\n%s", want, h.stderr.String())
		}
	}
}

func TestApprovalsStateFlag(t *testing.T) {
	if s, err := approvalsStateParam("all"); err != nil || s != "" {
		t.Errorf("all -> %q %v", s, err)
	}
	if s, err := approvalsStateParam("expired"); err != nil || s != "expired" {
		t.Errorf("expired -> %q %v", s, err)
	}
	if _, err := approvalsStateParam("waiting"); exitCode(err) != exitUsage {
		t.Errorf("waiting -> %v", err)
	}
}

func awaitingRunJSON() string {
	return `{"id":"` + testRunID + `","object":"run","state":"awaiting_approval","phase":"propose","workspace":{"id":"` + testWSID + `","name":"develop"},
	"agent":"pi","trigger":"webhook","automation":{"id":"` + testAutoID + `","name":"PR review"},"created_at":"2026-10-10T12:00:00Z",
	"apply_run_ids":["` + testRunID[:35] + `2"],"github_repo":"acme/api","secrets":[{"env":"GITHUB_TOKEN","exposure":"agent"}],
	"result":{"status":"ok","summary":"","branch":"","commit":"","changed_files":0,"artifacts":[],"actions_applied":[{"kind":"github_pr_comment","ok":true,"ref":"https://github.com/acme/api/pull/4#c1"}]},
	"approval":{"run_id":"` + testRunID + `","state":"pending","approval_from":0,"expires_at":"2026-10-14T12:00:00Z",
	"proposal":{"v":1,"actions":[{"kind":"git_push","base":"main","branch":"fix/x","commit":"0123456789abcdef","commit_message":"fix\u001b[2J it"}],
	"base":"main","merge_base":"m","diffstat":{"files":[{"path":"a.go","added":1,"deleted":0,"binary":false}],"files_changed":1,"insertions":1,"deletions":0,"truncated":false}},
	"proposal_sha256":"ff00","has_diff":true,"diff_masked":false,"prefix_apply_run_id":null,"apply_run_id":null}}`
}

func TestRunsProposalWithDiff(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{runGet: awaitingRunJSON(), diff: "diff --git a/a.go b/a.go\n+\x1b]52;c;ZXZpbA==\x07evil\n"}
	h := newPhase2Harness(t, fake)
	if err := doRunsProposal(context.Background(), h.env, testRunID, true, false); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"waiting for your approval", "git_push  fix/x onto main", "fix it", "diff:", "+evil", "/workspaces/" + testWSID + "/runs/" + testRunID} {
		if !strings.Contains(out, want) {
			t.Errorf("proposal lacks %q:\n%s", want, out)
		}
	}
	assertNoControls(t, "proposal", out)
	if fake.diffRequests != 1 {
		t.Errorf("diff requests = %d", fake.diffRequests)
	}
}

func TestRunsProposalWithoutAStoredDiff(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{runGet: strings.Replace(awaitingRunJSON(), `"has_diff":true`, `"has_diff":false`, 1)}
	h := newPhase2Harness(t, fake)
	if err := doRunsProposal(context.Background(), h.env, testRunID, true, false); err != nil {
		t.Fatal(err)
	}
	if fake.diffRequests != 0 || !strings.Contains(h.stderr.String(), "no diff is stored") {
		t.Errorf("requests %d, stderr %q", fake.diffRequests, h.stderr.String())
	}
}

func TestRunsProposalJSONAndMissing(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{runGet: awaitingRunJSON()})
	if err := doRunsProposal(context.Background(), h.env, testRunID, false, true); err != nil {
		t.Fatal(err)
	}
	var a api.Approval
	if err := json.Unmarshal(h.stdout.Bytes(), &a); err != nil || a.ProposalSHA256 != "ff00" {
		t.Errorf("%v %q", err, h.stdout.String())
	}
	assertNoControls(t, "--json", h.stdout.String())

	h = newPhase2Harness(t, &fakePhase2{})
	err := doRunsProposal(context.Background(), h.env, testRunID, false, false)
	if exitCode(err) != exitUsage || !strings.Contains(err.Error(), "has no proposal") {
		t.Errorf("exit %d: %v", exitCode(err), err)
	}
}

func TestRunsShowPhase2(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{runGet: awaitingRunJSON()})
	if err := doRunsShow(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"phase", "propose", "automation", "PR review", "approval", "pending", "1 actions (nan runs proposal", "apply run", testRunID[:35] + "2", "awaiting your approval", "nan runs reject " + testRunID,
		"repository", "acme/api", "GITHUB_TOKEN (the agent)", "1. github_pr_comment ok · https://github.com/acme/api/pull/4#c1"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
}

func TestRunsReject(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	fake := &fakePhase2{}
	h := newPhase2Harness(t, fake)
	rejectReason = "wrong branch"
	if err := doRunsReject(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	if fake.rejectBody["reason"] != "wrong branch" || !strings.Contains(h.stdout.String(), "rejected") {
		t.Errorf("body %v out %q", fake.rejectBody, h.stdout.String())
	}

	fake = &fakePhase2{rejectStatus: http.StatusConflict}
	h = newPhase2Harness(t, fake)
	err := doRunsReject(context.Background(), h.env, testRunID)
	if exitCode(err) != exitUsage || !strings.Contains(err.Error(), "not awaiting approval") {
		t.Errorf("exit %d: %v", exitCode(err), err)
	}
}

func TestRejectReasonValidation(t *testing.T) {
	for reason, ok := range map[string]bool{
		"":                       true,
		"line one\nline two":     true,
		strings.Repeat("é", 500): true,
		strings.Repeat("a", 501): false,
		"bell\x07":               false,
		"esc\x1b[2J":             false,
		"c1\u009b":               false,
		"bad utf8 \xff":          false,
	} {
		err := checkRejectReason(reason)
		if (err == nil) != ok {
			t.Errorf("%.20q: %v", reason, err)
		}
	}
}

func TestRunsLsAcceptsAwaitingApproval(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	lsState = api.StateAwaitingApproval
	if err := checkLsFlags(); err != nil {
		t.Error(err)
	}
}

// nan secrets ls goes with the session even when a token is set, on the
// portal's route, which any session may read whatever the write API.
func TestSecretsLsPrefersTheSession(t *testing.T) {
	fakeHome(t, `{"token":"sess-1","platformToken":"`+testPAT+`"}`)
	t.Setenv(tokenEnvVar, testPAT)
	env, err := newWriteEnv()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakePhase2{runs: &fakePlatform{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	env.client.WithBaseURL(srv.URL)
	var out, errOut bytes.Buffer
	env.stdout, env.stderr = &out, &errOut
	resetPhase2Flags()
	if err := doSecretsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if fake.secretsCookie != "nan_session=sess-1" || fake.secretsAuthz != "" {
		t.Errorf("cookie %q authz %q", fake.secretsCookie, fake.secretsAuthz)
	}
	for _, want := range []string{"NAME", "GITHUB_TOKEN", "…a1b2", "PR review", "never", "STRIPE"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
	assertNoControls(t, "secrets ls", out.String())
}

func TestSecretsLsJSONHasNoValues(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{})
	secretsLsJSON = true
	if err := doSecretsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), `"value"`) || !strings.Contains(h.stdout.String(), `"value_bytes": 93`) {
		t.Errorf("%s", h.stdout.String())
	}
}

func TestBrowserSessionRequiredSaysWhy(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{status: http.StatusForbidden, errBody: `{"error":"browser_session_required","message":"sign in again in the browser"}`})
	err := doSecretsLs(context.Background(), h.env)
	if exitCode(err) != exitAuth {
		t.Fatalf("exit %d: %v", exitCode(err), err)
	}
	for _, want := range []string{"browser", "https://cloud.nan.builders", runsDocs} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
}

// execute runs the real command tree, as main does, and returns how it
// would exit.
func execute(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	cmd, err := rootCmd.ExecuteC()
	if err == nil {
		return 0, out.String()
	}
	code, msg := exitCodeFor(cmd, err)
	return code, msg
}

// Approving stays portal-only (write API decision 4): no command does it,
// and reaching for one says where to go and why.
func TestApprovingPointsToThePortal(t *testing.T) {
	fakeHome(t, "")
	for _, args := range [][]string{
		{"runs", "approve", testRunID},
		{"approvals", "approve", testRunID},
	} {
		code, msg := execute(t, args...)
		if code != exitUsage || !strings.Contains(msg, "https://cloud.nan.builders") || !strings.Contains(msg, "browser") {
			t.Errorf("nan %s: exit %d: %s", strings.Join(args, " "), code, msg)
		}
	}
}

func TestNoCommandApproves(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Name() == "approve" || sub.HasAlias("approve") {
				t.Errorf("%q exists", sub.CommandPath())
			}
			walk(sub)
		}
	}
	walk(rootCmd)
}

func TestPhase2Help(t *testing.T) {
	for name, text := range map[string]string{
		"automations":            automationsCmd.Long,
		"automations run":        automationsRunCmd.Long + automationsRunCmd.Example,
		"automations deliveries": automationsDeliveriesCmd.Long,
		"approvals":              approvalsCmd.Long,
		"secrets":                secretsCmd.Long,
		"runs proposal":          runsProposalCmd.Long,
		"runs reject":            runsRejectCmd.Long,
		"runs":                   runsCmd.Long,
	} {
		if !strings.Contains(text, runsDocs) {
			t.Errorf("nan %s --help does not link %s", name, runsDocs)
		}
		if strings.Contains(text, "—") {
			t.Errorf("nan %s --help has an em-dash", name)
		}
	}
	for name, text := range map[string]string{
		"automations": automationsCmd.Long,
		"approvals":   approvalsCmd.Long,
		"secrets":     secretsCmd.Long,
	} {
		if !strings.Contains(text, "portal") || !strings.Contains(text, "browser") {
			t.Errorf("nan %s --help does not say what is portal-only and why", name)
		}
	}
	for _, want := range []string{"automations:read", "automations:run"} {
		if !strings.Contains(automationsCmd.Long, want) {
			t.Errorf("nan automations --help does not name the %s scope", want)
		}
	}
	if !strings.Contains(runCmd.Long, "10  awaiting approval") || !strings.Contains(runsLogsCmd.Long, "10 awaiting approval") {
		t.Error("exit code 10 is not documented")
	}
	cmds := append(automationsCmd.Commands(), approvalsCmd.Commands()...)
	cmds = append(cmds, secretsCmd.Commands()...)
	cmds = append(cmds, automationsTriggersCmd.Commands()...)
	for _, c := range cmds {
		if c.InheritedFlags().Lookup("token-file") == nil {
			t.Errorf("%s has no --token-file", c.CommandPath())
		}
	}
}

// nan runs logs -f exits 10 on a run that stops for approval, like nan run.
func TestRunsLogsFollowExitsTenOnAwaitingApproval(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newHarness(t, &fakePlatform{finalState: api.StateAwaitingApproval})
	logsFollow = true
	err := doRunsLogs(context.Background(), h.env, testRunID)
	if exitCode(err) != exitAwaitingApproval {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if !strings.Contains(h.stderr.String(), "nan runs proposal "+testRunID) {
		t.Errorf("no hint: %s", h.stderr.String())
	}
}

func TestRunsApproveNamesTheRun(t *testing.T) {
	fakeHome(t, "")
	for _, verb := range []string{"approve", "accept"} {
		code, msg := execute(t, "runs", verb, testRunID)
		if code != exitUsage || !strings.Contains(msg, "nan runs proposal "+testRunID) || !strings.Contains(msg, "?tab=approvals") {
			t.Errorf("%s: exit %d: %s", verb, code, msg)
		}
	}
	code, msg := execute(t, "runs", "bogus\x1b[2J")
	if code != exitUsage || strings.Contains(msg, "\x1b") {
		t.Errorf("unknown verb: exit %d: %q", code, msg)
	}
}

func TestNotEnabledIsNotAUsageError(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newPhase2Harness(t, &fakePhase2{status: http.StatusNotFound, errBody: `{"error":{"message":"not found"}}`})
	for name, err := range map[string]error{
		"automations ls": doAutomationsLs(context.Background(), h.env),
		"approvals ls":   doApprovalsLs(context.Background(), h.env, ""),
		"secrets ls":     doSecretsLs(context.Background(), h.env),
	} {
		if exitCode(err) != exitAuth || !strings.Contains(err.Error(), "not enabled") {
			t.Errorf("%s: exit %d: %v", name, exitCode(err), err)
		}
	}
}

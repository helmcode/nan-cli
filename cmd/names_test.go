package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

// The platform's runs, approvals and automations name their workspace and
// automation by id. The text views look the names up (GET /api/workspaces,
// GET /v1/automations) at most once per command, show "name (id)" in detail
// views and the name in tables, fall back to the id when the lookup fails,
// and never look anything up for --json, whose output stays the API's shape.

const (
	namesRun  = "6f1c2a9b-0000-4000-8000-000000000001"
	namesAuto = "a0000000-0000-4000-8000-000000000001"
	namesWS   = "11111111-2222-4333-8444-555555555555"

	namesWorkspaces = `[{"workspace_uuid":"` + namesWS + `","name":"develop","status":"running"},
{"workspace_uuid":"22222222-2222-4333-8444-555555555555","name":"other","status":"stopped"}]`

	scopeDenied = `{"error":{"message":"this token lacks the workspaces:read scope","type":"permission_error","code":"token_scope_denied"}}`
)

// answer is a canned response; drop closes the connection without one.
type answer struct {
	status int
	body   string
	drop   bool
}

// namesServer serves the phase 2 golden responses, with overrides per path
// ("" body for /api/workspaces means a 404), and records every request.
type namesServer struct {
	t         *testing.T
	overrides map[string]answer

	mu   sync.Mutex
	seen []string
}

func goldenBody(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "api", "testdata", "phase2", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (s *namesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	if a, ok := s.overrides[r.URL.Path]; ok {
		if a.drop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if a.status != 0 {
			w.WriteHeader(a.status)
		}
		_, _ = w.Write([]byte(a.body))
		return
	}
	routes := map[string]string{
		"/v1/runs/" + namesRun:         "run_get_propose.json",
		"/v1/runs":                     "run_list_propose.json",
		"/v1/approvals":                "approvals_list.json",
		"/v1/automations":              "automations_list.json",
		"/v1/automations/" + namesAuto: "automation_get.json",
		"/v1/automations/" + namesAuto + "/triggers": "automation_triggers.json",
	}
	name, ok := routes[r.URL.Path]
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(goldenBody(s.t, name)))
}

func (s *namesServer) count(req string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.seen {
		if r == req {
			n++
		}
	}
	return n
}

func (s *namesServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func newNamesEnv(t *testing.T, overrides map[string]answer) (*runsEnv, *namesServer, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	resetPhase2Flags()
	t.Cleanup(resetPhase2Flags)
	s := &namesServer{t: t, overrides: overrides}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	env := &runsEnv{client: api.NewRunsClient("", "nan_pat_test").WithBaseURL(srv.URL), stdout: &out, stderr: &errOut}
	return env, s, &out, &errOut
}

func withWorkspaces() map[string]answer {
	return map[string]answer{"/api/workspaces": {body: namesWorkspaces}}
}

// lineOf is the line of out that starts with key, for the detail views.
func lineOf(t *testing.T, out, key string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, key+" ") {
			return strings.Join(strings.Fields(l), " ")
		}
	}
	t.Fatalf("no %q line in:\n%s", key, out)
	return ""
}

// tableRow is the first data row of a table, its columns split on runs of
// spaces (no golden value has two spaces inside it).
func tableRow(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("no table row in:\n%s", out)
	}
	return lines[1]
}

func TestNamesAutomationsLs(t *testing.T) {
	env, s, out, _ := newNamesEnv(t, withWorkspaces())
	if err := doAutomationsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	row := tableRow(t, out.String())
	if !strings.Contains(row, "develop") || strings.Contains(row, namesWS) {
		t.Errorf("automations ls row shows no workspace name:\n%s", out.String())
	}
	if n := s.count("GET /api/workspaces"); n != 1 {
		t.Errorf("GET /api/workspaces %d times, want 1: %v", n, s.requests())
	}
	if n := s.count("GET /v1/automations"); n != 1 {
		t.Errorf("GET /v1/automations %d times, want 1 (the list itself): %v", n, s.requests())
	}
}

func TestNamesAutomationsShow(t *testing.T) {
	env, s, out, _ := newNamesEnv(t, withWorkspaces())
	if err := doAutomationsShow(context.Background(), env, namesAuto); err != nil {
		t.Fatal(err)
	}
	if got, want := lineOf(t, out.String(), "workspace"), "workspace develop ("+namesWS+")"; got != want {
		t.Errorf("workspace line = %q, want %q", got, want)
	}
	if n := s.count("GET /api/workspaces"); n != 1 {
		t.Errorf("GET /api/workspaces %d times, want 1: %v", n, s.requests())
	}
	if n := s.count("GET /v1/automations"); n != 0 {
		t.Errorf("show by id lists the automations: %v", s.requests())
	}
}

func TestNamesApprovalsLs(t *testing.T) {
	env, s, out, _ := newNamesEnv(t, withWorkspaces())
	if err := doApprovalsLs(context.Background(), env, api.ApprovalPending); err != nil {
		t.Fatal(err)
	}
	row := tableRow(t, out.String())
	if !strings.Contains(row, "PR review api") || strings.Contains(row, namesAuto) {
		t.Errorf("approvals ls row shows no automation name:\n%s", out.String())
	}
	if n := s.count("GET /v1/automations"); n != 1 {
		t.Errorf("GET /v1/automations %d times, want 1: %v", n, s.requests())
	}
	if n := s.count("GET /api/workspaces"); n != 0 {
		t.Errorf("approvals ls looks up workspaces it does not show: %v", s.requests())
	}
}

func TestNamesRunsShowAndProposal(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(*runsEnv) error
	}{
		{"show", func(env *runsEnv) error { return doRunsShow(context.Background(), env, namesRun) }},
		{"proposal", func(env *runsEnv) error { return doRunsProposal(context.Background(), env, namesRun, false, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, s, out, _ := newNamesEnv(t, withWorkspaces())
			if err := tc.do(env); err != nil {
				t.Fatal(err)
			}
			if got, want := lineOf(t, out.String(), "automation"), "automation PR review api ("+namesAuto+")"; got != want {
				t.Errorf("automation line = %q, want %q", got, want)
			}
			if n := s.count("GET /v1/automations"); n != 1 {
				t.Errorf("GET /v1/automations %d times, want 1: %v", n, s.requests())
			}
			// The run names its workspace itself: nothing to look up.
			if n := s.count("GET /api/workspaces"); n != 0 {
				t.Errorf("looked up a workspace name the run carries: %v", s.requests())
			}
		})
	}
}

func TestNamesRunsShowWorkspace(t *testing.T) {
	env, _, out, _ := newNamesEnv(t, withWorkspaces())
	if err := doRunsShow(context.Background(), env, namesRun); err != nil {
		t.Fatal(err)
	}
	if got, want := lineOf(t, out.String(), "workspace"), "workspace develop ("+namesWS+")"; got != want {
		t.Errorf("workspace line = %q, want %q", got, want)
	}
}

// runListWithoutWorkspaceName is the golden run list with the workspace's name
// gone, as for a run whose workspace was deleted or renamed away.
func runListWithoutWorkspaceName(t *testing.T) string {
	t.Helper()
	var list map[string]any
	if err := json.Unmarshal([]byte(goldenBody(t, "run_list_propose.json")), &list); err != nil {
		t.Fatal(err)
	}
	run := list["data"].([]any)[0].(map[string]any)
	run["workspace"] = map[string]any{"id": namesWS}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNamesRunsLsLooksUpOnlyWhatIsMissing(t *testing.T) {
	env, s, out, _ := newNamesEnv(t, withWorkspaces())
	if err := doRunsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableRow(t, out.String()), "develop") || s.count("GET /api/workspaces") != 0 {
		t.Errorf("runs ls with named workspaces: %v\n%s", s.requests(), out.String())
	}

	overrides := withWorkspaces()
	overrides["/v1/runs"] = answer{body: runListWithoutWorkspaceName(t)}
	env, s, out, _ = newNamesEnv(t, overrides)
	if err := doRunsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableRow(t, out.String()), "develop") || s.count("GET /api/workspaces") != 1 {
		t.Errorf("runs ls did not name the workspace: %v\n%s", s.requests(), out.String())
	}
}

// Every way the lookups can fail leaves the command working, with the ids.
func TestNamesFallBackToTheID(t *testing.T) {
	failures := map[string]answer{
		"403 token_scope_denied": {status: http.StatusForbidden, body: scopeDenied},
		"500":                    {status: http.StatusInternalServerError, body: `{"error":"boom"}`},
		"401":                    {status: http.StatusUnauthorized, body: `{"error":"invalid session"}`},
		"not JSON":               {body: `<html>`},
		"connection dropped":     {drop: true},
	}
	for what, fail := range failures {
		t.Run(what, func(t *testing.T) {
			// automations ls: the list itself works, the workspaces do not.
			env, _, out, errOut := newNamesEnv(t, map[string]answer{"/api/workspaces": fail})
			if err := doAutomationsLs(context.Background(), env); err != nil {
				t.Fatalf("automations ls failed: %v", err)
			}
			if !strings.Contains(tableRow(t, out.String()), namesWS) || errOut.Len() != 0 {
				t.Errorf("automations ls fallback:\n%s\nstderr: %s", out.String(), errOut.String())
			}

			env, _, out, errOut = newNamesEnv(t, map[string]answer{"/api/workspaces": fail})
			if err := doAutomationsShow(context.Background(), env, namesAuto); err != nil {
				t.Fatalf("automations show failed: %v", err)
			}
			if got := lineOf(t, out.String(), "workspace"); got != "workspace "+namesWS || errOut.Len() != 0 {
				t.Errorf("automations show workspace line = %q, stderr %q", got, errOut.String())
			}

			// approvals ls and the run views: the automations list fails.
			env, _, out, _ = newNamesEnv(t, map[string]answer{"/v1/automations": fail})
			if err := doApprovalsLs(context.Background(), env, api.ApprovalPending); err != nil {
				t.Fatalf("approvals ls failed: %v", err)
			}
			if !strings.Contains(tableRow(t, out.String()), namesAuto) {
				t.Errorf("approvals ls fallback:\n%s", out.String())
			}

			env, _, out, errOut = newNamesEnv(t, map[string]answer{"/v1/automations": fail})
			if err := doRunsShow(context.Background(), env, namesRun); err != nil {
				t.Fatalf("runs show failed: %v", err)
			}
			if got := lineOf(t, out.String(), "automation"); got != "automation "+namesAuto || errOut.Len() != 0 {
				t.Errorf("runs show automation line = %q, stderr %q", got, errOut.String())
			}

			env, _, out, _ = newNamesEnv(t, map[string]answer{"/v1/automations": fail})
			if err := doRunsProposal(context.Background(), env, namesRun, false, false); err != nil {
				t.Fatalf("runs proposal failed: %v", err)
			}
			if got := lineOf(t, out.String(), "automation"); got != "automation "+namesAuto {
				t.Errorf("runs proposal automation line = %q", got)
			}
		})
	}
}

// An id the lookup does not know (an automation deleted since) is shown as
// the id, after the one lookup.
func TestNamesUnknownIDFallsBack(t *testing.T) {
	env, s, out, _ := newNamesEnv(t, map[string]answer{"/api/workspaces": {body: `[{"workspace_uuid":"99999999-2222-4333-8444-555555555555","name":"x"}]`}})
	if err := doAutomationsShow(context.Background(), env, namesAuto); err != nil {
		t.Fatal(err)
	}
	if got := lineOf(t, out.String(), "workspace"); got != "workspace "+namesWS {
		t.Errorf("workspace line = %q", got)
	}
	if n := s.count("GET /api/workspaces"); n != 1 {
		t.Errorf("GET /api/workspaces %d times: %v", n, s.requests())
	}
}

// A name is agent-adjacent text from the platform: it is sanitised before
// it reaches the terminal.
func TestNamesAreSanitised(t *testing.T) {
	env, _, out, _ := newNamesEnv(t, map[string]answer{"/api/workspaces": {body: `[{"workspace_uuid":"` + namesWS + `","name":"dev\u001b[2Jelop\u0007"}]`}})
	if err := doAutomationsShow(context.Background(), env, namesAuto); err != nil {
		t.Fatal(err)
	}
	assertNoControls(t, "automations show", out.String())
}

// --json prints the API's shape, with no lookup: the same bytes whether or
// not the names could be had, and only the command's own requests.
func TestNamesNeverWithJSON(t *testing.T) {
	cases := []struct {
		name string
		set  func()
		do   func(*runsEnv) error
		want []string
	}{
		{"automations ls", func() { autoLsJSON = true },
			func(env *runsEnv) error { return doAutomationsLs(context.Background(), env) },
			[]string{"GET /v1/automations"}},
		{"automations show", func() { autoShowJSON = true },
			func(env *runsEnv) error { return doAutomationsShow(context.Background(), env, namesAuto) },
			[]string{"GET /v1/automations/" + namesAuto}},
		{"approvals ls", func() { approvalsJSON = true },
			func(env *runsEnv) error { return doApprovalsLs(context.Background(), env, api.ApprovalPending) },
			[]string{"GET /v1/approvals"}},
		{"runs show", func() { showJSON = true },
			func(env *runsEnv) error { return doRunsShow(context.Background(), env, namesRun) },
			[]string{"GET /v1/runs/" + namesRun}},
		{"runs proposal", func() {},
			func(env *runsEnv) error { return doRunsProposal(context.Background(), env, namesRun, false, true) },
			[]string{"GET /v1/runs/" + namesRun}},
		{"runs ls", func() { lsJSON = true },
			func(env *runsEnv) error { return doRunsLs(context.Background(), env) },
			[]string{"GET /v1/runs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overrides := withWorkspaces()
			overrides["/v1/runs"] = answer{body: runListWithoutWorkspaceName(t)}
			env, s, out, _ := newNamesEnv(t, overrides)
			tc.set()
			if err := tc.do(env); err != nil {
				t.Fatal(err)
			}
			if got := s.requests(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("requests = %v, want %v", got, tc.want)
			}
			// The same command where no name can be had prints the same.
			noNames := map[string]answer{
				"/api/workspaces": {status: http.StatusForbidden, body: scopeDenied},
				"/v1/runs":        overrides["/v1/runs"],
			}
			env2, _, out2, _ := newNamesEnv(t, noNames)
			tc.set()
			if err := tc.do(env2); err != nil {
				t.Fatal(err)
			}
			if out.String() != out2.String() {
				t.Errorf("--json differs with names available:\n%s\nvs\n%s", out.String(), out2.String())
			}
		})
	}
}

// Many rows, several ids: still one lookup per list.
func TestNamesOneLookupPerCommand(t *testing.T) {
	const otherWS = "22222222-2222-4333-8444-555555555555"
	var autos map[string]any
	if err := json.Unmarshal([]byte(goldenBody(t, "automations_list.json")), &autos); err != nil {
		t.Fatal(err)
	}
	first := autos["data"].([]any)[0].(map[string]any)
	var data []any
	for i, ws := range []string{namesWS, otherWS, namesWS, "33333333-2222-4333-8444-555555555555"} {
		a := map[string]any{}
		for k, v := range first {
			a[k] = v
		}
		a["id"] = strings.Replace(namesAuto, "1", string(rune('1'+i)), 1)
		a["workspace"] = ws
		data = append(data, a)
	}
	autos["data"] = data
	autosBody, _ := json.Marshal(autos)

	overrides := withWorkspaces()
	overrides["/v1/automations"] = answer{body: string(autosBody)}
	env, s, out, _ := newNamesEnv(t, overrides)
	if err := doAutomationsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if n := s.count("GET /api/workspaces"); n != 1 {
		t.Errorf("GET /api/workspaces %d times for 4 rows, want 1: %v", n, s.requests())
	}
	wantAll(t, "automations ls", out.String(), "develop", "other", "33333333-2222-4333-8444-555555555555")

	var approvals map[string]any
	if err := json.Unmarshal([]byte(goldenBody(t, "approvals_list.json")), &approvals); err != nil {
		t.Fatal(err)
	}
	item := approvals["data"].([]any)[0]
	approvals["data"] = []any{item, item, item}
	approvalsBody, _ := json.Marshal(approvals)
	env, s, out, _ = newNamesEnv(t, map[string]answer{
		"/v1/approvals":   {body: string(approvalsBody)},
		"/v1/automations": {status: http.StatusForbidden, body: scopeDenied},
	})
	if err := doApprovalsLs(context.Background(), env, api.ApprovalPending); err != nil {
		t.Fatal(err)
	}
	if n := s.count("GET /v1/automations"); n != 1 {
		t.Errorf("GET /v1/automations %d times for 3 rows after a refusal, want 1: %v", n, s.requests())
	}
	if strings.Count(out.String(), namesAuto) != 3 {
		t.Errorf("approvals ls fallback:\n%s", out.String())
	}
}

// Ids compare without regard to case: the platform's and the list's may
// differ in it.
func TestNamesIDsIgnoreCase(t *testing.T) {
	env, _, out, _ := newNamesEnv(t, map[string]answer{"/api/workspaces": {body: `[{"workspace_uuid":"` + strings.ToUpper(namesWS) + `","name":"develop"}]`}})
	if err := doAutomationsShow(context.Background(), env, namesAuto); err != nil {
		t.Fatal(err)
	}
	if got, want := lineOf(t, out.String(), "workspace"), "workspace develop ("+namesWS+")"; got != want {
		t.Errorf("workspace line = %q, want %q", got, want)
	}
}

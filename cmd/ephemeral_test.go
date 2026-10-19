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

	"github.com/nxssie/nan-cli/internal/api"
)

// Ephemeral runs (AGENT-EPHEMERAL-EXECUTORS.md, E2): the CLI starts them
// with nan run --ephemeral, shows their executor, size and cost, accepts
// executor/size in automation files, and reads the member's minutes and
// limits with nan ephemeral usage/settings. Free minutes are S-minutes,
// and that is said wherever they are shown.

func ephemeralOpts() runOptions {
	o := defaultOpts()
	o.ephemeral = true
	o.repo = "acme/api"
	o.model = "glm5.3"
	return o
}

func TestEphemeralRunSendsTheRequest(t *testing.T) {
	// "not json": an ephemeral run never looks a workspace up.
	h := newHarness(t, &fakePlatform{workspaces: "not json", ephemeral: true})
	opts := ephemeralOpts()
	opts.size = "M"
	opts.timeout, opts.timeoutSet = 3600e9, true
	if err := doRun(context.Background(), h.env, opts, []string{"fix the flaky test"}); err != nil {
		t.Fatal(err)
	}
	c := h.fake.created
	if c["executor"] != "ephemeral" || c["size"] != "m" || c["github_repo"] != "acme/api" || c["model"] != "glm5.3" ||
		c["agent"] != "pi" || c["prompt"] != "fix the flaky test" || c["trigger"] != "cli" || c["timeout_seconds"] != float64(3600) {
		t.Errorf("created with %v", c)
	}
	for _, k := range []string{"workspace", "cwd", "git_isolation", "inference"} {
		if _, ok := c[k]; ok {
			t.Errorf("an ephemeral run sent %s: %v", k, c)
		}
	}
	wantAll(t, "stderr", h.stderr.String(), "in an ephemeral container (size M) for acme/api", "model glm5.3",
		"cost: €0.018 for 3 started minutes of size M", "nan ephemeral usage")
	if strings.Contains(h.stderr.String(), "—") {
		t.Errorf("new output has an em-dash: %q", h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "all good") {
		t.Errorf("did not stream: %q", h.stdout.String())
	}
}

// Without --size the platform picks s: the CLI does not send a size.
func TestEphemeralRunDefaultSize(t *testing.T) {
	h := newHarness(t, &fakePlatform{ephemeral: true})
	if err := doRun(context.Background(), h.env, ephemeralOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.fake.created["size"]; ok {
		t.Errorf("sent a size nobody asked for: %v", h.fake.created)
	}
}

func TestEphemeralRunDetach(t *testing.T) {
	h := newHarness(t, &fakePlatform{ephemeral: true})
	opts := ephemeralOpts()
	opts.detach = true
	err := doRun(context.Background(), h.env, opts, []string{"x"})
	if exitCode(err) != exitDetached {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if strings.TrimSpace(h.stdout.String()) != testRunID {
		t.Errorf("stdout = %q", h.stdout.String())
	}
	wantAll(t, "stderr", h.stderr.String(), "queued in an ephemeral container (size M) for acme/api", "nan runs logs "+testRunID)
}

// BYOK and ephemeral combine: the model is the BYOK model.
func TestEphemeralRunWithBYOK(t *testing.T) {
	h := newHarness(t, &fakePlatform{ephemeral: true})
	opts := ephemeralOpts()
	opts.byokSecret, opts.model = "ANTHROPIC_KEY", "claude-sonnet-4-5"
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	inf, _ := h.fake.created["inference"].(map[string]any)
	if h.fake.created["executor"] != "ephemeral" || inf["model"] != "claude-sonnet-4-5" || h.fake.created["model"] != nil {
		t.Errorf("created with %v", h.fake.created)
	}
}

func TestEphemeralFlagMistakesAreUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*runOptions)
		wants []string
	}{
		{"no repo", func(o *runOptions) { o.repo = "" }, []string{"--repo OWNER/NAME"}},
		{"no model", func(o *runOptions) { o.model = "" }, []string{"--model"}},
		{"bad repo", func(o *runOptions) { o.repo = "acme" }, []string{"OWNER/NAME", "acme/api"}},
		{"dot repo", func(o *runOptions) { o.repo = "acme/.." }, []string{"OWNER/NAME"}},
		{"repo URL", func(o *runOptions) { o.repo = "https://github.com/acme/api.git" }, []string{"did you mean --repo acme/api?"}},
		{"bad size", func(o *runOptions) { o.size = "xl" }, []string{"s, m or l", "4 vCPU", "S-minutes"}},
		{"with --ws", func(o *runOptions) { o.workspace = "develop" }, []string{"no workspace", "--ws"}},
		{"with --cwd", func(o *runOptions) { o.cwd = "/home/nan/x" }, []string{"--cwd"}},
		{"with --worktree", func(o *runOptions) { o.worktree = true }, []string{"--worktree"}},
		{"with --no-worktree", func(o *runOptions) { o.noWorktree = true }, []string{"--no-worktree"}},
		{"--repo without --ephemeral", func(o *runOptions) { o.ephemeral = false }, []string{"--repo is for ephemeral runs", "--ephemeral"}},
		{"--size without --ephemeral", func(o *runOptions) { o.ephemeral, o.repo, o.size = false, "", "m" }, []string{"--size is for ephemeral runs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &fakePlatform{ephemeral: true})
			opts := ephemeralOpts()
			tc.edit(&opts)
			err := doRun(context.Background(), h.env, opts, []string{"x"})
			if exitCode(err) != exitUsage {
				t.Fatalf("exit %d (%v)", exitCode(err), err)
			}
			wantAll(t, tc.name, err.Error(), tc.wants...)
			if h.fake.created != nil {
				t.Error("sent the run anyway")
			}
		})
	}
}

// The sizes are case-insensitive on the command line and sent lowercase.
func TestEphemeralSizeIsNormalised(t *testing.T) {
	for in, want := range map[string]string{"S": "s", "m": "m", "L": "l"} {
		opts := ephemeralOpts()
		opts.size = in
		req, err := buildRunRequest(promptInput{r: strings.NewReader("")}, opts, []string{"x"})
		if err != nil || req.Size != want || req.Executor != api.ExecutorEphemeral || req.Workspace != "" {
			t.Errorf("%s: %+v %v", in, req, err)
		}
	}
}

func TestEphemeralRefusalsSayWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cred          credential
		status        int
		code, param   string
		want          int
		wants, avoids []string
	}{
		{"cap reached", credential{kind: credSession}, 402, "ephemeral_cap_reached", "", exitUnavailable,
			[]string{"monthly cap", "Settings at https://cloud.nan.builders/settings", "nan ephemeral usage"}, nil},
		{"token without scope", credential{kind: credEnv, bearer: testPAT}, 403, "token_scope_denied", "executor", exitAuth,
			[]string{"runs:ephemeral", "NAN_TOKEN", "Settings > Tokens", "nan auth login"}, []string{testPAT}},
		{"saved API key", credential{kind: credStoredKey, bearer: testKey}, 403, "token_scope_denied", "executor", exitAuth,
			[]string{"API keys (sk-) cannot", "nan auth login"}, []string{testKey}},
		{"not available", credential{kind: credSession}, 400, "invalid_request", "executor", exitAuth,
			[]string{"ephemeral runs are not available for your account"}, []string{"workspace"}},
		{"runs off", credential{kind: credSession}, 404, "not_found", "", exitAuth,
			[]string{"runs are not enabled"}, []string{"workspace"}},
		{"bad repo on the server", credential{kind: credSession}, 400, "invalid_request", "github_repo", exitUsage,
			[]string{"github_repo"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			param := "null"
			if tc.param != "" {
				param = fmt.Sprintf("%q", tc.param)
			}
			h := newHarness(t, &fakePlatform{ephemeral: true, createStatus: tc.status,
				createBody: fmt.Sprintf(`{"error":{"message":"refused \u001b[2J","type":"invalid_request_error","param":%s,"code":%q}}`, param, tc.code)})
			h.env.cred = tc.cred
			err := doRun(context.Background(), h.env, ephemeralOpts(), []string{"x"})
			if exitCode(err) != tc.want {
				t.Fatalf("exit %d (%v), want %d", exitCode(err), err, tc.want)
			}
			wantAll(t, tc.name, err.Error(), tc.wants...)
			for _, a := range tc.avoids {
				if strings.Contains(err.Error(), a) {
					t.Errorf("%q mentions %q", err.Error(), a)
				}
			}
			assertNoControls(t, tc.name, err.Error())
		})
	}
}

// An ephemeral automation run with a token missing runs:ephemeral reads
// the same, and so does the write API's refusal of an ephemeral automation.
func TestEphemeralRefusalsOnEveryPath(t *testing.T) {
	cred := credential{kind: credStoredToken, bearer: testPAT}
	scope := &api.APIError{Status: 403, Code: "token_scope_denied", Param: "executor", Message: "x"}
	for name, exit := range map[string]*ExitError{
		"apiExit":   apiExit(scope, cred),
		"writeExit": writeExit(scope, cred, "automations", scopeAutomationsWrite),
	} {
		if exit.Code != exitAuth || !strings.Contains(exit.Error(), "runs:ephemeral") || strings.Contains(exit.Error(), "automations:write scope") {
			t.Errorf("%s: %d %v", name, exit.Code, exit)
		}
	}
	off := &api.APIError{Status: 400, Code: "invalid_request", Param: "executor", Message: `executor must be "workspace"`}
	if exit := writeExit(off, cred, "automations", scopeAutomationsWrite); exit.Code != exitAuth || exit.Error() != "ephemeral runs are not available for your account" {
		t.Errorf("writeExit off: %d %v", exit.Code, exit)
	}
	// A refusal that is not about ephemeral runs is left to the usual mapping.
	other := &api.APIError{Status: 403, Code: "token_scope_denied", Param: "", Message: "needs runs"}
	if exit := apiExit(other, cred); strings.Contains(exit.Error(), "runs:ephemeral") {
		t.Errorf("a plain scope refusal reads as ephemeral: %v", exit)
	}
}

// ─── views ─────────────────────────────────────────────────────────────────

// ephemeralGoldenEnv serves the ephemeral golden responses (rendered by
// cloud-api's own handlers, internal/api/testdata/ephemeral) on the paths
// that return them. usage answers /api/ephemeral/usage (nil: 404).
func ephemeralGoldenEnv(t *testing.T, cred credential, usage []byte) (*runsEnv, *bytes.Buffer, *bytes.Buffer, func() []string) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "internal", "api", "testdata", "ephemeral", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	auto := read("automation_get_ephemeral.json")
	routes := map[string][]byte{
		"/v1/runs/" + testRunID:                       read("run_get_ephemeral.json"),
		"/v1/runs":                                    read("run_list_ephemeral.json"),
		"/v1/automations/" + testAutoID:               auto,
		"/v1/automations":                             []byte(`{"object":"list","data":[` + string(auto) + `]}`),
		"/v1/automations/" + testAutoID + "/triggers": []byte(`{"data":[]}`),
		"/api/ephemeral/settings":                     read("ephemeral_settings.json"),
	}
	if usage != nil {
		routes["/api/ephemeral/usage"] = usage
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		b, ok := routes[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	env := &runsEnv{client: api.NewRunsClient(cred.session, cred.bearer).WithBaseURL(srv.URL), cred: cred, stdout: &out, stderr: &errOut}
	return env, &out, &errOut, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func goldenUsage(t *testing.T, billing bool) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "api", "testdata", "ephemeral", "ephemeral_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if billing {
		b = bytes.Replace(b, []byte(`"billing_enabled": false`), []byte(`"billing_enabled": true`), 1)
	}
	return b
}

var sessionCred = credential{kind: credSession, session: "s"}

// The views of an ephemeral run and an ephemeral automation, rendered from
// what cloud-api serialises.
func TestEphemeralGoldenViews(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		do   func(env *runsEnv) error
	}{
		{"runs_show", func(env *runsEnv) error { return doRunsShow(ctx, env, testRunID) }},
		{"runs_ls", func(env *runsEnv) error { return doRunsLs(ctx, env) }},
		{"automations_show", func(env *runsEnv) error { return doAutomationsShow(ctx, env, testAutoID) }},
		{"automations_ls", func(env *runsEnv) error { return doAutomationsLs(ctx, env) }},
		{"usage", func(env *runsEnv) error { return doEphemeralUsage(ctx, env) }},
		{"settings", func(env *runsEnv) error { return doEphemeralSettings(ctx, env) }},
	} {
		env, out, _, _ := ephemeralGoldenEnv(t, sessionCred, goldenUsage(t, false))
		if err := tc.do(env); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		checkGolden(t, "ephemeral/"+tc.name+".golden", out.String())
		assertNoControls(t, tc.name, out.String())
		if strings.Contains(out.String(), "—") {
			t.Errorf("%s has an em-dash", tc.name)
		}
	}
}

func TestEphemeralRunsShowFacts(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	ctx := context.Background()
	env, out, _, _ := ephemeralGoldenEnv(t, sessionCred, goldenUsage(t, false))
	if err := doRunsShow(ctx, env, testRunID); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "runs show", out.String(), "executor", "ephemeral", "M (2 vCPU, 8 GiB RAM, 16 GB disk)", "acme/api",
		"€0.018 for 3 started minutes of size M", "before your free minutes", "not charged yet")
	for _, gone := range []string{"workspace", "worktree", "cwd"} {
		if strings.Contains(out.String(), gone+" ") {
			t.Errorf("runs show of an ephemeral run shows %s:\n%s", gone, out.String())
		}
	}

	// Billing on: no note.
	env, out, _, _ = ephemeralGoldenEnv(t, sessionCred, goldenUsage(t, true))
	if err := doRunsShow(ctx, env, testRunID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not charged") {
		t.Errorf("billing on, still says not charged:\n%s", out.String())
	}

	// A token cannot ask: the note says it conditionally, and nothing is
	// asked of /api/ephemeral.
	env, out, _, seen := ephemeralGoldenEnv(t, credential{kind: credEnv, bearer: testPAT}, goldenUsage(t, false))
	if err := doRunsShow(ctx, env, testRunID); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "runs show (token)", out.String(), "while billing is not active", "nan ephemeral usage")
	for _, r := range seen() {
		if strings.HasPrefix(r, "GET /api/ephemeral") {
			t.Errorf("a token asked %s", r)
		}
	}

	// The usage lookup failing never fails the command.
	env, out, _, _ = ephemeralGoldenEnv(t, sessionCred, nil)
	if err := doRunsShow(ctx, env, testRunID); err != nil || !strings.Contains(out.String(), "while billing is not active") {
		t.Errorf("usage 404: %v\n%s", err, out.String())
	}
}

// A list of workspace runs only keeps its columns.
func TestRunsLsCostColumnOnlyWithEphemeralRuns(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	h := newHarness(t, &fakePlatform{})
	if err := doRunsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), "COST") {
		t.Errorf("workspace runs only, with a cost column:\n%s", h.stdout.String())
	}
	env, out, _, _ := ephemeralGoldenEnv(t, sessionCred, nil)
	if err := doRunsLs(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if !strings.Contains(lines[0], "COST") || !strings.Contains(lines[1], "ephemeral M") || !strings.Contains(lines[1], "€0.018") ||
		!strings.Contains(lines[2], "pending") || !strings.Contains(lines[3], "develop") || !strings.Contains(lines[3], " - ") {
		t.Errorf("runs ls:\n%s", out.String())
	}
}

func TestEphemeralUsageFacts(t *testing.T) {
	ctx := context.Background()
	env, out, _, _ := ephemeralGoldenEnv(t, sessionCred, goldenUsage(t, false))
	if err := doEphemeralUsage(ctx, env); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "usage", out.String(), "2026-10 (inference plan)", "49 minutes",
		"247 S-minutes of 300 (= 123 M or 61 L minutes); 53 used", "€0.00 after your free minutes", notChargedNote,
		"€100.00", "€0.003", "€0.006", "€0.012", "a minute uses 2 free S-minutes", "a minute uses 4 free S-minutes", freeMinutesHelp)

	env, out, _, _ = ephemeralGoldenEnv(t, sessionCred, goldenUsage(t, true))
	if err := doEphemeralUsage(ctx, env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not charged") {
		t.Errorf("billing on, still not charged:\n%s", out.String())
	}
}

func TestEphemeralSettingsIsReadOnly(t *testing.T) {
	env, out, errOut, seen := ephemeralGoldenEnv(t, sessionCred, nil)
	if err := doEphemeralSettings(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "settings", out.String(), "€100.00 (€0 to €1000.00)", "up to 50 at once (1 to 50)", "not active yet")
	wantAll(t, "settings stderr", errOut.String(), "change these in the browser", "https://cloud.nan.builders/settings")
	for _, r := range seen() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("settings wrote: %s", r)
		}
	}
	for _, c := range ephemeralSettingsCmd.Commands() {
		t.Errorf("nan ephemeral settings has a subcommand %s: writes are browser-only", c.Name())
	}
}

func TestEphemeralReadsWhenUnavailable(t *testing.T) {
	ctx := context.Background()
	for name, status := range map[string]int{"off": 404, "not a session": 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"x"}`)
		}))
		env := &runsEnv{client: api.NewRunsClient("s", "").WithBaseURL(srv.URL), cred: sessionCred, stdout: io.Discard, stderr: io.Discard}
		for cmd, do := range map[string]func() error{
			"usage":    func() error { return doEphemeralUsage(ctx, env) },
			"settings": func() error { return doEphemeralSettings(ctx, env) },
		} {
			err := do()
			if exitCode(err) != exitAuth {
				t.Errorf("%s %s: exit %d (%v)", name, cmd, exitCode(err), err)
			}
			if status == 404 && err.Error() != "ephemeral runs are not available for your account" {
				t.Errorf("%s %s: %v", name, cmd, err)
			}
		}
		srv.Close()
	}
}

// nan ephemeral takes the session only: the platform takes no token there.
func TestEphemeralNeedsTheSession(t *testing.T) {
	fakeHome(t, cliSession)
	cred, err := ephemeralSession(func(string) string { return testPAT }, "")
	if err != nil || cred.kind != credSession || cred.session != "sess-cli" {
		t.Fatalf("with a session: %+v %v", cred, err)
	}
	for name, tc := range map[string]struct {
		session, env, file string
		wants              []string
	}{
		"nobody":       {"", "", "", []string{"nan auth login"}},
		"NAN_TOKEN":    {"", testPAT, "", []string{"NAN_TOKEN is set", "nan auth login"}},
		"token-file":   {cliSession, "", "/tmp/tok", []string{"--token-file", "nan auth login"}},
		"saved token":  {`{"platform_token":"` + testPAT + `"}`, "", "", []string{"nan auth login"}},
		"corrupt file": {`{not json`, "", "", []string{"nan auth login"}},
	} {
		fakeHome(t, tc.session)
		_, err := ephemeralSession(func(k string) string {
			if k == tokenEnvVar {
				return tc.env
			}
			return ""
		}, tc.file)
		if exitCode(err) != exitAuth {
			t.Errorf("%s: exit %d (%v)", name, exitCode(err), err)
			continue
		}
		wantAll(t, name, err.Error(), tc.wants...)
		if strings.Contains(err.Error(), testPAT) {
			t.Errorf("%s: shows the token", name)
		}
	}
}

// ─── automation files ──────────────────────────────────────────────────────

func TestCheckAutomationExecutor(t *testing.T) {
	const ok = `{"name":"n","executor":"ephemeral","size":"m","checkout":"fresh","github_repo":"acme/api","agent":"pi","model":"glm5.3"}`
	for _, tc := range []struct {
		name   string
		body   string
		create bool
		want   string // "" = accepted
	}{
		{"ephemeral", ok, true, ""},
		{"ephemeral, size left out", `{"executor":"ephemeral","checkout":"fresh","github_repo":"acme/api","model":"m"}`, true, ""},
		{"ephemeral with BYOK model", `{"executor":"ephemeral","checkout":"fresh","github_repo":"acme/api","inference":{"source":"byok","secret":"K","model":"x"}}`, true, ""},
		{"workspace automation", `{"name":"n","workspace":"develop","agent":"pi"}`, true, ""},
		{"explicit workspace executor", `{"executor":"workspace","workspace":"develop"}`, true, ""},
		{"edit: size only", `{"size":"l"}`, false, ""},
		{"edit: switch to ephemeral", `{"executor":"ephemeral"}`, false, ""},
		{"not an object", `[1]`, true, ""},
		{"unknown executor", `{"executor":"container"}`, true, `executor must be "workspace"`},
		{"executor not a string", `{"executor":1}`, false, `executor must be`},
		{"unknown size", `{"executor":"ephemeral","size":"xl"}`, false, `size must be "s", "m" or "l"`},
		{"size on a workspace executor", `{"executor":"workspace","size":"m"}`, false, "ephemeral automations only"},
		{"size without executor on create", `{"workspace":"develop","size":"m"}`, true, ""},
		{"workspace on ephemeral", `{"executor":"ephemeral","workspace":"develop","checkout":"fresh","github_repo":"a/b","model":"m"}`, true, "no workspace"},
		{"no fresh checkout", `{"executor":"ephemeral","github_repo":"a/b","model":"m"}`, true, "checkout: fresh"},
		{"no repo", `{"executor":"ephemeral","checkout":"fresh","model":"m"}`, true, "github_repo: OWNER/NAME"},
		{"cwd", `{"executor":"ephemeral","checkout":"fresh","github_repo":"a/b","model":"m","cwd":"/home/nan/x"}`, true, "remove cwd"},
		{"no model", `{"executor":"ephemeral","checkout":"fresh","github_repo":"a/b"}`, true, "must name its model"},
	} {
		err := checkAutomationExecutor(json.RawMessage(tc.body), tc.create)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (exitCode(err) != exitUsage || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// A bad ephemeral automation file is refused before anything is sent, and a
// good one (YAML) goes out as written.
func TestAutomationsCreateEphemeralFile(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	good := filepath.Join(dir, "good.yaml")
	for p, body := range map[string]string{
		bad:  "name: nightly\nexecutor: ephemeral\nsize: xl\n",
		good: "name: nightly\nexecutor: ephemeral\nsize: m\ncheckout: fresh\ngithub_repo: acme/api\nagent: pi\nmodel: glm5.3\ninstructions: Review.\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", bad)
	if r.code != exitUsage || !strings.Contains(r.msg, `size must be "s", "m" or "l"`) || !strings.Contains(r.msg, "S-minutes") {
		t.Fatalf("bad file: %d %s", r.code, r.msg)
	}
	if len(fake.reqs) != 0 {
		t.Errorf("sent %d requests for a bad file", len(fake.reqs))
	}

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", good)
	if r.code != 0 {
		t.Fatalf("good file: %d %s", r.code, r.msg)
	}
	var sent map[string]any
	for _, q := range fake.reqs {
		if q.Method == http.MethodPost && q.Path == "/v1/automations" {
			_ = json.Unmarshal([]byte(q.Body), &sent)
		}
	}
	if sent["executor"] != "ephemeral" || sent["size"] != "m" || sent["checkout"] != "fresh" {
		t.Errorf("sent %v", sent)
	}

	// edit: an unknown size never leaves the machine either.
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "edit", testAutoID, "-f", bad)
	if r.code != exitUsage || len(fake.reqs) != 0 {
		t.Errorf("edit with a bad file: %d %s, %d requests", r.code, r.msg, len(fake.reqs))
	}
}

// ─── formatting and help ───────────────────────────────────────────────────

func TestFormatEUR(t *testing.T) {
	for micros, want := range map[int64]string{
		0: "€0.00", 3000: "€0.003", 18000: "€0.018", 1_500_000: "€1.50", 100_000_000: "€100.00",
		123_456: "€0.123456", 10_000: "€0.01", -6000: "-€0.006",
	} {
		if got := formatEUR(micros); got != want {
			t.Errorf("formatEUR(%d) = %q, want %q", micros, got, want)
		}
	}
	if got := formatCents(10000); got != "€100.00" {
		t.Errorf("formatCents = %q", got)
	}
	for secs, want := range map[int64]int64{0: 0, 1: 1, 14: 1, 60: 1, 61: 2, 180: 3} {
		if got := startedMinutes(secs); got != want {
			t.Errorf("startedMinutes(%d) = %d, want %d", secs, got, want)
		}
	}
}

func TestRunCostLabels(t *testing.T) {
	m := "m"
	for _, tc := range []struct {
		run         api.Run
		long, short string
	}{
		{api.Run{Executor: "ephemeral", Size: &m, State: "running"}, "metered when the run ends", "pending"},
		{api.Run{Executor: "ephemeral", Size: &m, State: "succeeded"}, "not metered yet", "pending"},
		{api.Run{Executor: "ephemeral", Size: &m, State: "succeeded", Cost: &api.RunCost{EURMicros: 6000, BilledSeconds: 14}},
			"€0.006 for 1 started minute of size M", "€0.006"},
		{api.Run{Executor: "workspace", State: "succeeded"}, "", "-"},
	} {
		if tc.long != "" && !strings.Contains(runCostLabel(&tc.run), tc.long) {
			t.Errorf("%+v: %q", tc.run, runCostLabel(&tc.run))
		}
		if got := runCostShort(&tc.run); got != tc.short {
			t.Errorf("%+v short: %q", tc.run, got)
		}
	}
	bad := "x\x1b[2J"
	if got := sizeLabel(&bad); strings.ContainsAny(got, "\x1b") {
		t.Errorf("sizeLabel = %q", got)
	}
}

// The owner asked for this sentence wherever free minutes are shown.
func TestEphemeralHelpExplainsSMinutes(t *testing.T) {
	const want = "Free minutes are S-minutes: one M minute uses 2, one L minute 4 (100 free = 100 S, 50 M or 25 L)"
	if freeMinutesHelp != want {
		t.Fatalf("freeMinutesHelp = %q", freeMinutesHelp)
	}
	for name, text := range map[string]string{
		"nan ephemeral":          ephemeralCmd.Long,
		"nan ephemeral usage":    ephemeralUsageCmd.Long,
		"nan run":                runCmd.Long,
		"nan automations create": automationsCreateCmd.Long,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s --help does not explain S-minutes", name)
		}
		if strings.Contains(text, "—") {
			t.Errorf("%s --help has an em-dash", name)
		}
		if strings.Contains(strings.ToLower(text), "eu only") {
			t.Errorf("%s --help claims EU only", name)
		}
	}
	wantAll(t, "nan run --help", runCmd.Long, "--ephemeral --repo OWNER/NAME --model ID", "HTTP and HTTPS", "public",
		"GITHUB_TOKEN", "1 vCPU, 4 GiB RAM, 10 GB disk", "€0.012 per minute", "14 s run is 1 minute", "runs:ephemeral",
		"metered and shown, not charged", "nan ephemeral usage")
	wantAll(t, "nan run examples", runCmd.Example, "nan run --ephemeral --repo acme/api --model glm5.3 --size m")
	for name, c := range map[string]string{"nan ephemeral": ephemeralCmd.Long, "nan ephemeral usage": ephemeralUsageCmd.Long,
		"nan ephemeral settings": ephemeralSettingsCmd.Long} {
		wantAll(t, name+" --help", c, "Docs: https://nan.builders/docs/ephemeral-runs")
	}
	wantAll(t, "nan ephemeral settings --help", ephemeralSettingsCmd.Long, "read only", "browser", ephemeralSettingsURL)
	wantAll(t, "nan ephemeral --help", ephemeralCmd.Long, "Community 100, Inference 300, Premium", "€100", "50", "nan auth login")
	wantAll(t, "nan automations create --help", automationsCreateCmd.Long, "executor: ephemeral", "size: m", "checkout: fresh", "runs:ephemeral")
	for _, f := range []string{"ephemeral", "repo", "size"} {
		if runCmd.Flags().Lookup(f) == nil {
			t.Errorf("nan run has no --%s", f)
		}
	}
	if ephemeralUsageCmd.Annotations[sysexitsAnnotation] == "" || ephemeralSettingsCmd.Annotations[sysexitsAnnotation] == "" {
		t.Error("nan ephemeral commands do not use the documented exit codes")
	}
}

// End to end through the command tree: an unknown subcommand and the
// usage command with the session.
func TestEphemeralCommandTree(t *testing.T) {
	code, msg := execute(t, "ephemeral", "set-cap")
	if code != exitUsage || !strings.Contains(msg, "nan ephemeral --help") {
		t.Errorf("unknown subcommand: %d %s", code, msg)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "nan_session=sess-cli" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write(goldenUsage(t, false))
	}))
	t.Cleanup(srv.Close)
	fakeHome(t, cliSession)
	swap(t, &runsBaseURL, srv.URL)
	var out, errOut bytes.Buffer
	swap(t, &writeStdout, io.Writer(&out))
	swap(t, &writeStderr, io.Writer(&errOut))
	code, msg = execute(t, "ephemeral", "usage")
	if code != 0 || !strings.Contains(out.String(), freeMinutesHelp) {
		t.Errorf("usage: %d %s\n%s", code, msg, out.String())
	}
}

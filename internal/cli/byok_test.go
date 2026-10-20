package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

// BYOK (bring your own key), AGENT-AUTOMATIONS-PHASE3-BYOK.md §2: a run or
// an automation may use the member's provider key, named by its secret, with
// a model id. Ad-hoc BYOK runs need a session (§4.3).

var updateGolden = flag.Bool("update", false, "rewrite the cmd/testdata golden files")

func byokOpts() runOptions {
	o := defaultOpts()
	o.byokSecret = "ANTHROPIC_KEY"
	o.model = "claude-sonnet-4-5"
	return o
}

func TestBYOKRunSendsTheInferenceObject(t *testing.T) {
	h := newHarness(t, &fakePlatform{})
	if err := doRun(context.Background(), h.env, byokOpts(), []string{"review PR 42"}); err != nil {
		t.Fatal(err)
	}
	inf, ok := h.fake.created["inference"].(map[string]any)
	if !ok || len(inf) != 3 || inf["source"] != "byok" || inf["secret"] != "ANTHROPIC_KEY" || inf["model"] != "claude-sonnet-4-5" {
		t.Fatalf("inference = %v", h.fake.created["inference"])
	}
	// With BYOK the model is inference.model: a top-level model is a 400.
	if _, ok := h.fake.created["model"]; ok {
		t.Errorf("sent a top-level model with byok: %v", h.fake.created)
	}
	if !strings.Contains(h.stderr.String(), "your key ANTHROPIC_KEY") || !strings.Contains(h.stderr.String(), "claude-sonnet-4-5") {
		t.Errorf("the start line does not say which key runs it: %q", h.stderr.String())
	}

	// A NaN run sends no inference at all: every existing caller unchanged.
	h = newHarness(t, &fakePlatform{})
	opts := defaultOpts()
	opts.model = "glm5.3"
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.fake.created["inference"]; ok || h.fake.created["model"] != "glm5.3" {
		t.Errorf("a NaN run sent %v", h.fake.created)
	}
}

func TestBYOKFlagMistakesAreUsageErrors(t *testing.T) {
	const pasted = "sk-ant-api03-PastedTheKeyItself0123456789"
	for name, tc := range map[string]struct {
		mutate func(*runOptions)
		wants  []string
	}{
		"no model":               {func(o *runOptions) { o.model = "" }, []string{"--model", "your provider"}},
		"model starting with -":  {func(o *runOptions) { o.model = "-x" }, []string{"--model"}},
		"lower case name":        {func(o *runOptions) { o.byokSecret = "anthropic_key" }, []string{"NAME", "ANTHROPIC_KEY", "nan secrets ls"}},
		"the key itself":         {func(o *runOptions) { o.byokSecret = pasted }, []string{"NAME", "shell history", "revoke"}},
		"the key as the model":   {func(o *runOptions) { o.model = pasted }, []string{"--model", "shell history", "revoke"}},
		"a long lower case name": {func(o *runOptions) { o.byokSecret = "my_very_long_lower_case_name_x" }, []string{"NAME"}},
	} {
		h := newHarness(t, &fakePlatform{})
		opts := byokOpts()
		tc.mutate(&opts)
		err := doRun(context.Background(), h.env, opts, []string{"x"})
		if exitCode(err) != exitUsage || h.fake.created != nil {
			t.Errorf("%s: exit %d, sent %v (%v)", name, exitCode(err), h.fake.created, err)
			continue
		}
		wantAll(t, name, err.Error(), tc.wants...)
		if strings.Contains(err.Error(), "MY_VERY_LONG") {
			t.Errorf("%s: suggested a name for a long value: %v", name, err)
		}
		if strings.Contains(err.Error(), "PastedTheKey") {
			t.Errorf("%s: the error repeats what may be a key: %v", name, err)
		}
	}
}

// §4.3: an ad-hoc BYOK run needs a session. A token or an API key is refused
// here, before anything is sent, with what to do instead; a saved platform
// token is skipped for the session it would otherwise outrank.
func TestBYOKRunNeedsTheSession(t *testing.T) {
	fakeHome(t, `{"token":"sess-1","platformToken":"`+testPAT+`","apiKey":"`+testKey+`"}`)
	cred, err := byokCredential(os.Getenv, "")
	if err != nil || cred.kind != credSession || cred.session != "sess-1" {
		t.Fatalf("saved token + session: %+v %v", cred, err)
	}

	for name, setup := range map[string]func(t *testing.T) string{
		"NAN_TOKEN": func(t *testing.T) string {
			fakeHome(t, `{"token":"sess-1"}`)
			t.Setenv(tokenEnvVar, testPAT)
			return ""
		},
		"--token-file": func(t *testing.T) string {
			fakeHome(t, `{"token":"sess-1"}`)
			p := filepath.Join(t.TempDir(), "tok")
			_ = os.WriteFile(p, []byte(testKey+"\n"), 0o600)
			return p
		},
		"saved token only": func(t *testing.T) string { fakeHome(t, `{"platformToken":"`+testPAT+`"}`); return "" },
		"saved key only":   func(t *testing.T) string { fakeHome(t, `{"apiKey":"`+testKey+`"}`); return "" },
		"nothing":          func(t *testing.T) string { fakeHome(t, ""); return "" },
	} {
		file := setup(t)
		_, err := byokCredential(os.Getenv, file)
		if exitCode(err) != exitAuth {
			t.Errorf("%s: exit %d (%v)", name, exitCode(err), err)
			continue
		}
		wantAll(t, name, err.Error(), "nan auth login", "nan automations run")
		if strings.Contains(err.Error(), testPAT) || strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: the error shows the credential: %v", name, err)
		}
	}
}

func TestBYOKRefusalsSayWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		name          string
		opts          runOptions
		status        int
		code, param   string
		want          int
		wants, avoids []string
	}{
		{
			"session required", byokOpts(), 403, "session_required", "inference", exitAuth,
			[]string{"nan auth login", "nan automations run"},
			nil,
		},
		{
			"no such secret", byokOpts(), 404, "not_found", "inference.secret", exitUsage,
			[]string{"ANTHROPIC_KEY", "nan secrets ls", "Provider key"},
			[]string{"workspace"},
		},
		{
			"not a provider key", byokOpts(), 400, "invalid_request", "inference.secret", exitUsage,
			[]string{"ANTHROPIC_KEY", "provider_key", "nan secrets ls"},
			nil,
		},
		{"bad model", byokOpts(), 400, "invalid_request", "inference.model", exitUsage, []string{"--model"}, nil},
		{"not available", byokOpts(), 400, "invalid_request", "inference", exitAuth, []string{"not available"}, nil},
		{
			"no workspace, no plan", byokOpts(), 403, "tier_restricted", "", exitAuth,
			[]string{"workspace"},
			[]string{"--byok-secret"},
		},
		{
			"no plan, nan run", defaultOpts(), 403, "tier_restricted", "", exitAuth,
			[]string{"membership", "--byok-secret", "--model"},
			nil,
		},
	} {
		param := "null"
		if tc.param != "" {
			param = fmt.Sprintf("%q", tc.param)
		}
		h := newHarness(t, &fakePlatform{
			createStatus: tc.status,
			createBody:   fmt.Sprintf(`{"error":{"message":"refused \u001b[2J","type":"invalid_request_error","param":%s,"code":%q}}`, param, tc.code),
		})
		err := doRun(context.Background(), h.env, tc.opts, []string{"x"})
		if exitCode(err) != tc.want {
			t.Errorf("%s: exit %d (%v), want %d", tc.name, exitCode(err), err, tc.want)
			continue
		}
		wantAll(t, tc.name, err.Error(), tc.wants...)
		for _, a := range tc.avoids {
			if strings.Contains(err.Error(), a) {
				t.Errorf("%s: %q mentions %q", tc.name, err.Error(), a)
			}
		}
		assertNoControls(t, tc.name, err.Error())
	}
}

// byokGoldenEnv serves the BYOK golden responses (internal/api/testdata/phase3,
// rendered by cloud-api's own handlers) on the paths that return them.
func byokGoldenEnv(t *testing.T) (*runsEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	const run = "6f1c2a9b-0000-4000-8000-000000000001"
	const auto = "a0000000-0000-4000-8000-000000000001"
	routes := map[string]string{
		"/v1/runs/" + run:                       "phase3/run_get_byok.json",
		"/v1/runs":                              "phase3/run_list_byok.json",
		"/v1/automations/" + auto:               "phase3/automation_get_byok.json",
		"/v1/automations/" + auto + "/triggers": "phase2/automation_triggers.json",
		"/api/account/secrets":                  "phase3/secrets_list_byok.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := routes[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "api", "testdata", name))
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	return &runsEnv{client: api.NewRunsClient("s", "").WithBaseURL(srv.URL), stdout: &out, stderr: &errOut}, &out, &errOut
}

func checkTextGolden(t *testing.T, name, got string) {
	t.Helper()
	checkGolden(t, "byok/"+name+".golden", got)
}

// checkGolden compares got with cmd/testdata/<rel>; -update rewrites it.
func checkGolden(t *testing.T, rel, got string) {
	t.Helper()
	name := rel
	path := filepath.Join("testdata", filepath.FromSlash(rel))
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run go test ./cmd -run %s -update)", name, err, t.Name())
	}
	if got != string(want) {
		t.Errorf("%s drifted from %s:\n--- got\n%s\n--- want\n%s", name, path, got, want)
	}
}

// The views of a BYOK run, a BYOK automation and provider keys, rendered from
// what cloud-api serialises.
func TestBYOKGoldenViews(t *testing.T) {
	resetPhase2Flags()
	defer resetPhase2Flags()
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		do   func(env *runsEnv) error
	}{
		{"runs_show", func(env *runsEnv) error { return doRunsShow(ctx, env, "6f1c2a9b-0000-4000-8000-000000000001") }},
		{"runs_ls", func(env *runsEnv) error { return doRunsLs(ctx, env) }},
		{"automations_show", func(env *runsEnv) error { return doAutomationsShow(ctx, env, "a0000000-0000-4000-8000-000000000001") }},
		{"secrets_ls", func(env *runsEnv) error { return doSecretsLs(ctx, env) }},
	} {
		env, out, _ := byokGoldenEnv(t)
		if err := tc.do(env); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		checkTextGolden(t, tc.name, out.String())
		if strings.Contains(out.String(), "GoldenValue") {
			t.Errorf("%s shows a secret value", tc.name)
		}
	}
}

// A long model id is a model id, and the BASE URL column and the webhook
// note only show when they apply.
func TestBYOKOnlyWhenItApplies(t *testing.T) {
	h := newHarness(t, &fakePlatform{})
	opts := byokOpts()
	opts.model = "meta-llama/Llama-3.3-70B-Instruct-Turbo-with-a-long-name"
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}

	resetPhase2Flags()
	defer resetPhase2Flags()
	ph := newPhase2Harness(t, &fakePhase2{})
	if err := doSecretsLs(context.Background(), ph.env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ph.stdout.String(), "BASE URL") || !strings.Contains(ph.stdout.String(), "PROVIDER") {
		t.Errorf("secrets ls without base URLs:\n%s", ph.stdout.String())
	}

	for name, trig := range map[string][]api.Trigger{
		"no trigger":       nil,
		"schedule only":    {{Kind: "schedule", Enabled: true}},
		"disabled webhook": {{Kind: "webhook", Enabled: false}},
	} {
		if hasWebhook(trig) {
			t.Errorf("%s: counted as a webhook", name)
		}
	}
	if !hasWebhook([]api.Trigger{{Kind: "schedule", Enabled: true}, {Kind: "webhook", Enabled: true}}) {
		t.Error("an enabled webhook is not seen")
	}
}

func TestBYOKCorruptSession(t *testing.T) {
	fakeHome(t, `{not json`)
	_, err := byokCredential(os.Getenv, "")
	if exitCode(err) != exitAuth || !strings.Contains(err.Error(), "nan auth login") {
		t.Fatalf("exit %d: %v", exitCode(err), err)
	}
}

func TestBYOKInferenceLabels(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, tc := range []struct {
		in          *api.Inference
		long, short string
	}{
		{nil, "NaN", "nan"},
		{&api.Inference{Source: "nan"}, "NaN", "nan"},
		{
			&api.Inference{Source: "byok", Provider: s("openai_compatible"), Secret: s("TOGETHER_KEY"), Model: s("m")},
			"your key TOGETHER_KEY (OpenAI-compatible): billed by your provider, not metered by NaN", "key TOGETHER_KEY",
		},
		{&api.Inference{Source: "byok", Provider: s("gemini"), Secret: s("G")}, "your key G (Google Gemini): billed by your provider, not metered by NaN", "key G"},
		{&api.Inference{Source: "byok"}, "your key: billed by your provider, not metered by NaN", "key"},
		{&api.Inference{Source: "byok", Provider: s("x\x1b[2J"), Secret: s("K\x07")}, "your key K (x): billed by your provider, not metered by NaN", "key K"},
		{&api.Inference{Source: "future"}, "future", "future"},
	} {
		if got := inferenceLabel(tc.in); got != tc.long {
			t.Errorf("long %+v = %q, want %q", tc.in, got, tc.long)
		}
		if got := inferenceShort(tc.in); got != tc.short {
			t.Errorf("short %+v = %q, want %q", tc.in, got, tc.short)
		}
	}
}

func TestBYOKHelp(t *testing.T) {
	f := runCmd.Flags().Lookup("byok-secret")
	if f == nil || !strings.Contains(f.Usage, "NAME") {
		t.Fatalf("--byok-secret: %+v", f)
	}
	wantAll(t, "nan run --help", runCmd.Long, "--byok-secret", "nan auth login", "nan secrets ls", "https://nan.builders/docs/runs")
	wantAll(t, "nan run examples", runCmd.Example, "--byok-secret ANTHROPIC_KEY --model")
	wantAll(t, "nan secrets --help", secretsCmd.Long, "provider key")
}

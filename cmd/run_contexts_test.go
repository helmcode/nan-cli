package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

// Agent contexts on runs (devops AGENT-CONTEXT.md §2.5, §6, C2): nan run
// --context NAME (repeatable, at most 5, order kept) sends "contexts" on
// POST /v1/runs; an ephemeral run may leave out --repo only when it loads
// at least one context. Every mistake is a usage error (64) before any
// request; the platform's context refusals read as what to do.

// requestGolden is the request body as the CLI marshals it.
func requestGolden(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "contexts/"+name, string(b)+"\n")
}

func TestRunContextsAreSentInOrder(t *testing.T) {
	h := newHarness(t, &fakePlatform{workspaces: `[{"workspace_uuid":"w1","name":"develop","status":"running"}]`})
	opts := defaultOpts()
	opts.contexts = []string{"acme-api", "style-guide"}
	if err := doRun(context.Background(), h.env, opts, []string{"review it"}); err != nil {
		t.Fatal(err)
	}
	got, _ := h.fake.created["contexts"].([]any)
	if fmt.Sprint(got) != "[acme-api style-guide]" {
		t.Errorf("contexts sent: %v (%v)", h.fake.created["contexts"], h.fake.created)
	}
	wantAll(t, "stderr", h.stderr.String(), "contexts acme-api, style-guide")
}

func TestRunWithoutContextsSendsNone(t *testing.T) {
	req, err := buildRunRequest(promptInput{}, defaultOpts(), []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	if strings.Contains(string(b), "contexts") {
		t.Errorf("a run without --context sent contexts: %s", b)
	}
}

func TestRunContextRequestGoldens(t *testing.T) {
	ws := defaultOpts()
	ws.workspace, ws.contexts = "develop", []string{"acme-api", "style-guide"}
	req, err := buildRunRequest(promptInput{}, ws, []string{"review the open pull requests"})
	if err != nil {
		t.Fatal(err)
	}
	requestGolden(t, "run_request_workspace.json", req)

	eph := ephemeralOpts()
	eph.repo, eph.size, eph.contexts = "", "m", []string{"acme-api"}
	req, err = buildRunRequest(promptInput{}, eph, []string{"write the release notes"})
	if err != nil {
		t.Fatal(err)
	}
	requestGolden(t, "run_request_ephemeral_no_repo.json", req)

	both := ephemeralOpts()
	both.contexts = []string{"style-guide", "acme-api"}
	req, err = buildRunRequest(promptInput{}, both, []string{"fix the flaky test"})
	if err != nil {
		t.Fatal(err)
	}
	requestGolden(t, "run_request_ephemeral_repo.json", req)
}

// An ephemeral run with a context and no repository: no github_repo is
// sent, and the announcement names no repository.
func TestEphemeralRunWithOnlyAContext(t *testing.T) {
	h := newHarness(t, &fakePlatform{workspaces: "not json", ephemeral: true})
	opts := ephemeralOpts()
	opts.repo, opts.contexts = "", []string{"acme-api"}
	if err := doRun(context.Background(), h.env, opts, []string{"write the notes"}); err != nil {
		t.Fatal(err)
	}
	c := h.fake.created
	if _, ok := c["github_repo"]; ok || c["executor"] != "ephemeral" || fmt.Sprint(c["contexts"]) != "[acme-api]" {
		t.Errorf("created with %v", c)
	}
}

func TestRunContextFlagMistakesAreUsageErrors(t *testing.T) {
	six := []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	for _, tc := range []struct {
		name  string
		edit  func(*runOptions)
		wants []string
	}{
		{"six contexts", func(o *runOptions) { o.contexts = six }, []string{"at most 5", "--context"}},
		{"repeated", func(o *runOptions) { o.contexts = []string{"acme-api", "acme-api"} }, []string{"acme-api", "twice"}},
		{"repeated id, other case", func(o *runOptions) {
			o.contexts = []string{"c0000000-0000-4000-8000-0000000000ab", "C0000000-0000-4000-8000-0000000000AB"}
		}, []string{"twice"}},
		{"not a name", func(o *runOptions) { o.contexts = []string{"Acme API"} }, []string{"Acme API", "context name", "nan context ls"}},
		{"empty", func(o *runOptions) { o.contexts = []string{""} }, []string{"empty", "--context"}},
		{"control bytes", func(o *runOptions) { o.contexts = []string{"a\x1b[2Jb"} }, []string{"context name"}},
		{"ephemeral with neither repo nor context", func(o *runOptions) { o.ephemeral, o.model = true, "glm5.3" },
			[]string{"--repo OWNER/NAME", "--context NAME"}},
		{"token secret without repo", func(o *runOptions) {
			o.ephemeral, o.model, o.contexts = true, "glm5.3", []string{"acme-api"}
			o.githubTokenSecret, o.githubTokenSecretSet = "GITHUB_TOKEN", true
		}, []string{"--github-token-secret", "--repo"}},
		{"ephemeral context without model", func(o *runOptions) { o.ephemeral, o.contexts = true, []string{"acme-api"} },
			[]string{"--model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &fakePlatform{})
			opts := defaultOpts()
			tc.edit(&opts)
			err := doRun(context.Background(), h.env, opts, []string{"x"})
			if exitCode(err) != exitUsage {
				t.Fatalf("exit %d (%v)", exitCode(err), err)
			}
			wantAll(t, tc.name, err.Error(), tc.wants...)
			assertNoControls(t, tc.name, err.Error())
			if h.fake.created != nil {
				t.Error("sent the run anyway")
			}
		})
	}
}

// A context id is accepted in any case, as the platform does.
func TestRunContextAcceptsIDs(t *testing.T) {
	opts := defaultOpts()
	opts.contexts = []string{"C0000000-0000-4000-8000-0000000000AB", "acme-api"}
	req, err := buildRunRequest(promptInput{}, opts, []string{"x"})
	if err != nil || len(req.Contexts) != 2 || req.Contexts[0] != "C0000000-0000-4000-8000-0000000000AB" {
		t.Fatalf("%+v %v", req, err)
	}
}

// Through cobra: the flag repeats, and a sixth one stops the command
// before it even looks for a credential.
func TestRunContextFlagThroughTheCommand(t *testing.T) {
	fakeHome(t, "")
	t.Setenv(tokenEnvVar, "")
	args := []string{"run"}
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
		args = append(args, "--context", n)
	}
	code, msg := execute(t, append(args, "x")...)
	if code != exitUsage || !strings.Contains(msg, "at most 5") {
		t.Fatalf("exit %d: %s", code, msg)
	}
	code, msg = execute(t, "run", "--ephemeral", "--model", "glm5.3", "x")
	if code != exitUsage || !strings.Contains(msg, "--context NAME") {
		t.Fatalf("ephemeral without repo: exit %d: %s", code, msg)
	}
	// A comma is not a separator: one flag is one context.
	code, msg = execute(t, "run", "--context", "a,b", "x")
	if code != exitUsage || !strings.Contains(msg, `"a,b"`) {
		t.Fatalf("comma: exit %d: %s", code, msg)
	}
}

func TestRunContextRefusalsSayWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ephemeral     bool
		status        int
		code, param   string
		wants, avoids []string
	}{
		{"unknown context", false, 404, "context_not_found", "contexts[1]",
			[]string{`context "style-guide" not found`, "nan context ls"}, []string{"workspace", "not enabled"}},
		{"unknown context, ephemeral", true, 404, "context_not_found", "contexts[0]",
			[]string{`context "acme-api" not found`, "nan context ls"}, []string{"not enabled"}},
		{"unknown context, no index", false, 404, "context_not_found", "contexts",
			[]string{"not found", "nan context ls"}, []string{"workspace"}},
		{"skill conflict", false, 422, "context_skill_conflict", "contexts",
			[]string{"same skill", "--context"}, nil},
		{"too large", true, 413, "context_too_large", "contexts",
			[]string{"too large for one run", "250 MiB", "fewer contexts"}, nil},
		{"bad reference", false, 400, "invalid_request", "contexts[0]",
			[]string{"--context", "acme-api"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			param := "null"
			if tc.param != "" {
				param = fmt.Sprintf("%q", tc.param)
			}
			h := newHarness(t, &fakePlatform{ephemeral: tc.ephemeral, createStatus: tc.status,
				createBody: fmt.Sprintf(`{"error":{"message":"refused \u001b[2J","type":"invalid_request_error","param":%s,"code":%q}}`, param, tc.code)})
			opts := defaultOpts()
			opts.workspace = "develop"
			if tc.ephemeral {
				opts = ephemeralOpts()
			}
			opts.contexts = []string{"acme-api", "style-guide"}
			err := doRun(context.Background(), h.env, opts, []string{"x"})
			if exitCode(err) != exitUsage {
				t.Fatalf("exit %d (%v)", exitCode(err), err)
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

// The run object's pinned contexts are part of the client model, so
// nan run --json shows what the run loaded.
func TestRunJSONCarriesPinnedContexts(t *testing.T) {
	var r api.Run
	if err := json.Unmarshal([]byte(`{"id":"x","contexts":[{"id":"c1","name":"acme-api","version":3,"tree_sha256":"ab"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Contexts) != 1 || r.Contexts[0].Name != "acme-api" || r.Contexts[0].Version != 3 || r.Contexts[0].TreeSHA256 != "ab" {
		t.Errorf("%+v", r.Contexts)
	}
	b, _ := json.Marshal(api.Run{})
	if strings.Contains(string(b), "contexts") {
		t.Errorf("a run without contexts serialises them: %s", b)
	}
}

func TestRunHelpExplainsContexts(t *testing.T) {
	wantAll(t, "nan run --help", runCmd.Long, "--context NAME", "at most 5", "nan context ls", "without --repo")
	wantAll(t, "nan run examples", runCmd.Example, "--context")
	if f := runCmd.Flags().Lookup("context"); f == nil || f.Value.Type() != "stringArray" {
		t.Fatalf("--context flag: %+v", f)
	}
}

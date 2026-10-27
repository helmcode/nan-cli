package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Agent contexts on automations (devops AGENT-CONTEXT.md §2.5, §6, C2):
// nan automations create|edit --context NAME (repeatable, at most 5) puts
// "contexts" in the body, and --checkout none makes an ephemeral
// automation without a repository, which needs at least one context.

func automationFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// bodyGolden compares a sent JSON body, indented, with a golden file.
func bodyGolden(t *testing.T, name, body string) {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(body), "", "  "); err != nil {
		t.Fatalf("%s: %v (%q)", name, err, body)
	}
	checkGolden(t, "contexts/"+name, out.String()+"\n")
}

const wsAutomationYAML = "name: PR review api\nworkspace: develop\nagent: pi\ninstructions: Review it.\n"

const noRepoAutomationYAML = "name: weekly notes\nexecutor: ephemeral\nsize: s\nagent: pi\nmodel: glm5.3\ninstructions: Write the notes.\n"

func TestAutomationsCreateWithContexts(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", automationFile(t, wsAutomationYAML),
		"--context", "acme-api", "--context", "style-guide")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	posts := fake.sent("POST", "/v1/automations")
	if len(posts) != 1 {
		t.Fatalf("%v", fake.requests())
	}
	bodyGolden(t, "automation_create_workspace.json", posts[0].Body)
}

func TestAutomationsCreateWithoutRepository(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", automationFile(t, noRepoAutomationYAML),
		"--checkout", "none", "--context", "acme-api")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	posts := fake.sent("POST", "/v1/automations")
	if len(posts) != 1 {
		t.Fatalf("%v", fake.requests())
	}
	bodyGolden(t, "automation_create_ephemeral_none.json", posts[0].Body)

	// The same, written in the file.
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f",
		automationFile(t, noRepoAutomationYAML+"checkout: none\ncontexts: [acme-api]\n"))
	if r.code != 0 || len(fake.sent("POST", "/v1/automations")) != 1 {
		t.Fatalf("from the file: exit %d: %s", r.code, r.msg)
	}
}

// Without flags, the file still goes out exactly as written.
func TestAutomationsCreateWithoutContextFlagsIsUntouched(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession, stdin: `{"name":"x","approval_kinds":["github_push"]}`}, "automations", "create", "-f", "-")
	if r.code != 0 || fake.sent("POST", "/v1/automations")[0].Body != `{"name":"x","approval_kinds":["github_push"]}` {
		t.Fatalf("exit %d %s %v", r.code, r.msg, fake.requests())
	}
}

func TestAutomationsEditContextsWithoutAFile(t *testing.T) {
	path := "/v1/automations/" + testAutoID
	for name, tc := range map[string]struct {
		args   []string
		body   string
		golden string
	}{
		"contexts only": {[]string{"--context", "acme-api", "--context", "style-guide"}, `{"contexts":["acme-api","style-guide"]}`, ""},
		"checkout none": {[]string{"--checkout", "none", "--context", "acme-api"}, "", "automation_edit_checkout_none.json"},
		"with a file":   {[]string{"-f", automationFile(t, "size: l\n"), "--context", "acme-api"}, `{"contexts":["acme-api"],"size":"l"}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeWrite()
			r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"automations", "edit", testAutoID}, tc.args...)...)
			if r.code != 0 {
				t.Fatalf("exit %d: %s", r.code, r.msg)
			}
			got := fake.sent("PATCH", path)
			if len(got) != 1 {
				t.Fatalf("%v", fake.requests())
			}
			if tc.golden != "" {
				bodyGolden(t, tc.golden, got[0].Body)
			} else if got[0].Body != tc.body {
				t.Errorf("body %s, want %s", got[0].Body, tc.body)
			}
		})
	}
}

func TestAutomationsContextMistakesAreUsageErrors(t *testing.T) {
	six := []string{}
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
		six = append(six, "--context", n)
	}
	ws := automationFile(t, wsAutomationYAML)
	noRepo := automationFile(t, noRepoAutomationYAML)
	withRepo := automationFile(t, noRepoAutomationYAML+"github_repo: acme/api\n")
	both := automationFile(t, wsAutomationYAML+"contexts: [acme-api]\n")
	bothCheckout := automationFile(t, noRepoAutomationYAML+"checkout: fresh\n")
	fileSix := automationFile(t, wsAutomationYAML+"contexts: [a1, a2, a3, a4, a5, a6]\n")
	fileBad := automationFile(t, wsAutomationYAML+"contexts: [Acme API]\n")
	fileNotList := automationFile(t, wsAutomationYAML+"contexts: acme-api\n")
	for _, tc := range []struct {
		name  string
		args  []string
		wants []string
	}{
		{"six on create", append([]string{"automations", "create", "-f", ws}, six...), []string{"at most 5"}},
		{"six on edit", append([]string{"automations", "edit", testAutoID}, six...), []string{"at most 5"}},
		{"repeated", []string{"automations", "create", "-f", ws, "--context", "acme-api", "--context", "acme-api"}, []string{"twice"}},
		{"not a name", []string{"automations", "create", "-f", ws, "--context", "Acme API"}, []string{"context name"}},
		{"in the file and as a flag", []string{"automations", "create", "-f", both, "--context", "b"}, []string{"contexts", "--context", "once"}},
		{"checkout in the file and as a flag", []string{"automations", "create", "-f", bothCheckout, "--checkout", "none", "--context", "a"},
			[]string{"checkout", "--checkout", "once"}},
		{"none without a context", []string{"automations", "create", "-f", noRepo, "--checkout", "none"}, []string{`checkout "none"`, "--context"}},
		{"none with a repository", []string{"automations", "create", "-f", withRepo, "--checkout", "none", "--context", "a"},
			[]string{`checkout "none"`, "github_repo"}},
		{"none on a workspace automation", []string{"automations", "create", "-f", ws, "--checkout", "none", "--context", "a"},
			[]string{"ephemeral"}},
		{"unknown checkout", []string{"automations", "create", "-f", ws, "--checkout", "clone"}, []string{"--checkout", "none"}},
		{"ephemeral with neither", []string{"automations", "create", "-f", noRepo}, []string{"github_repo", "checkout: none"}},
		{"edit with nothing", []string{"automations", "edit", testAutoID}, []string{"-f FILE"}},
		{"six in the file", []string{"automations", "create", "-f", fileSix}, []string{"at most 5"}},
		{"bad name in the file", []string{"automations", "create", "-f", fileBad}, []string{"context name"}},
		{"contexts not a list", []string{"automations", "create", "-f", fileNotList}, []string{"contexts", "list"}},
		{"none edit with a repository", []string{"automations", "edit", testAutoID, "-f", automationFile(t, "github_repo: acme/api\n"), "--checkout", "none"},
			[]string{`checkout "none"`, "github_repo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeWrite()
			r := runWrite(t, fake, writeSetup{session: cliSession}, tc.args...)
			if r.code != exitUsage {
				t.Fatalf("exit %d: %s", r.code, r.msg)
			}
			wantAll(t, tc.name, r.msg, tc.wants...)
			if len(fake.reqs) != 0 {
				t.Errorf("sent %d requests", len(fake.reqs))
			}
		})
	}
}

func TestAutomationsContextRefusals(t *testing.T) {
	notFound := `{"error":{"message":"context not found \u001b[2J","type":"invalid_request_error","param":"contexts[1]","code":"context_not_found"}}`
	ws := automationFile(t, wsAutomationYAML)

	fake := newFakeWrite()
	fake.failOn("POST /v1/automations", 404, notFound)
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", ws, "--context", "acme-api", "--context", "style-guide")
	if r.code != exitUsage || !strings.Contains(r.msg, `context "style-guide" not found`) || !strings.Contains(r.msg, "nan context ls") {
		t.Errorf("create: exit %d: %s", r.code, r.msg)
	}
	assertNoControls(t, "create", r.msg)

	// On edit a 404 about a context is not "automation not found".
	fake = newFakeWrite()
	fake.failOn("PATCH /v1/automations/"+testAutoID, 404, notFound)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "edit", testAutoID, "--context", "acme-api", "--context", "style-guide")
	if r.code != exitUsage || !strings.Contains(r.msg, `context "style-guide" not found`) || strings.Contains(r.msg, "automation") {
		t.Errorf("edit: exit %d: %s", r.code, r.msg)
	}

	// A context listed in the file is named too.
	fake = newFakeWrite()
	fake.failOn("POST /v1/automations", 404, strings.Replace(notFound, "contexts[1]", "contexts[0]", 1))
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", automationFile(t, wsAutomationYAML+"contexts: [from-file]\n"))
	if r.code != exitUsage || !strings.Contains(r.msg, `context "from-file" not found`) {
		t.Errorf("file: exit %d: %s", r.code, r.msg)
	}
}

func TestAutomationsHelpExplainsContexts(t *testing.T) {
	for _, c := range []struct {
		name string
		long string
	}{{"create", automationsCreateCmd.Long}, {"edit", automationsEditCmd.Long}} {
		wantAll(t, c.name, c.long, "--context NAME", "at most 5", "--checkout none")
		for _, f := range []string{"context", "checkout"} {
			if (map[string]bool{"create": automationsCreateCmd.Flags().Lookup(f) != nil, "edit": automationsEditCmd.Flags().Lookup(f) != nil})[c.name] == false {
				t.Errorf("%s has no --%s", c.name, f)
			}
		}
	}
}

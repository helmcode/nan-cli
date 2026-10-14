package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The --json contract: a script pipes stdout into jq, so with --json stdout
// carries exactly one JSON document (or, for a command that follows a run,
// one JSON value per line) and nothing else. Every hint, note and warning
// goes to stderr.

// jsonCase is one --json invocation. lines marks the commands that print
// JSON Lines (events, then the run) instead of a single document; code is
// the documented exit code (a detached run still going exits 75).
type jsonCase struct {
	args  []string
	lines bool
	stdin string
	code  int
}

// hintFragments are pieces of the human text the commands print around
// their output; none of them belongs on stdout with --json.
var hintFragments = []string{
	oneTimeNote,
	"Store these now",
	"note:",
	"add a trigger",
	"run it",
	"change it:",
	"follow:",
	"cancel:",
	"nan automations",
	"nan runs",
	"nan secrets",
	"Ctrl-C",
	"already exists",
	"showing the",
	"the next runs",
	notifSecretNote,
	"Store it now",
	"previous secret keeps working",
	"send a test",
	"nan notifications",
	"turned off",
}

// jsonFixtures writes the files the write commands read.
func jsonFixtures(t *testing.T) (automation, trigger, value string) {
	t.Helper()
	dir := t.TempDir()
	automation = filepath.Join(dir, "automation.yaml")
	trigger = filepath.Join(dir, "trigger.yaml")
	value = filepath.Join(dir, "value")
	for path, body := range map[string]string{
		automation: "name: PR review api\nworkspace: develop\nagent: pi\ninstructions: Review it.\napproval_kinds: []\n",
		trigger:    "kind: webhook\nsignature_scheme: github\n",
		value:      testValue + "\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return automation, trigger, value
}

func jsonCases(t *testing.T) map[string][]jsonCase {
	automation, trigger, value := jsonFixtures(t)
	hook := filepath.Join(t.TempDir(), "hook-url")
	if err := os.WriteFile(hook, []byte(testHookURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string][]jsonCase{
		"nan approvals ls":   {{args: []string{"approvals", "ls", "--json"}}},
		"nan approvals show": {{args: []string{"approvals", "show", testRunID, "--json"}}},
		"nan automations create": {
			// approval_kinds is empty: the human output warns about it.
			{args: []string{"automations", "create", "-f", automation, "--json"}},
		},
		"nan automations deliveries": {{args: []string{"automations", "deliveries", testAutoID, "--json"}}},
		"nan automations disable":    {{args: []string{"automations", "disable", testAutoID, "--json"}}},
		"nan automations edit":       {{args: []string{"automations", "edit", testAutoID, "-f", automation, "--json"}}},
		"nan automations enable":     {{args: []string{"automations", "enable", testAutoID, "--json"}}},
		"nan automations ls":         {{args: []string{"automations", "ls", "--json"}}},
		"nan automations run": {
			{args: []string{"automations", "run", testAutoID, "--json"}, lines: true},
			{args: []string{"automations", "run", testAutoID, "--detach", "--json"}, code: exitDetached},
		},
		"nan automations show": {{args: []string{"automations", "show", testAutoID, "--json"}}},
		"nan automations triggers add": {
			// The E2E shape: the one-time URL and secret come back, and the
			// note about them goes to stderr only.
			{args: []string{"automations", "triggers", "add", testAutoID, "-f", trigger, "--json"}},
		},
		"nan automations triggers edit": {{args: []string{"automations", "triggers", "edit", testAutoID, triggerUUID, "-f", trigger, "--json"}}},
		"nan automations triggers ls":   {{args: []string{"automations", "triggers", "ls", testAutoID, "--json"}}},
		"nan automations triggers rotate": {
			{args: []string{"automations", "triggers", "rotate", testAutoID, triggerUUID, "--json"}},
			{args: []string{"automations", "triggers", "rotate", testAutoID, triggerUUID, "--url", "--secret", "--no-grace", "--json"}},
		},
		"nan notifications add": {
			{args: []string{"notifications", "add", "deploys", "--kind", "slack", "--url-file", hook, "--json"}},
			// The one-time signing secret is in the JSON; the note about it
			// goes to stderr only.
			{args: []string{"notifications", "add", "ci", "--kind", "https", "--url-stdin", "--event", "run.failed", "--json"}, stdin: testHookHTTPS},
		},
		"nan notifications deliveries": {{args: []string{"notifications", "deliveries", "ci receiver", "--limit", "3", "--json"}}},
		"nan notifications edit": {
			{args: []string{"notifications", "edit", "team alerts", "--disable", "--json"}},
			{args: []string{"notifications", "edit", "ci receiver", "--url-file", hook, "--json"}},
		},
		"nan notifications ls":     {{args: []string{"notifications", "ls", "--json"}}},
		"nan notifications rotate": {{args: []string{"notifications", "rotate", "ci receiver", "--json"}}},
		"nan notifications show":   {{args: []string{"notifications", "show", "ci receiver", "--json"}}},
		"nan notifications subscriptions": {
			{args: []string{"notifications", "subscriptions", "ci receiver", "--json"}},
			{args: []string{"notifications", "subscriptions", "team alerts", "--add", "run.finished", "--json"}},
		},
		"nan notifications test": {{args: []string{"notifications", "test", "team alerts", "--json"}}},
		"nan run": {
			{args: []string{"run", "review", "--json"}, lines: true},
			{args: []string{"run", "review", "--detach", "--json"}, code: exitDetached},
		},
		"nan runs cancel": {{args: []string{"runs", "cancel", testRunID, "--json"}}},
		"nan runs logs": {
			{args: []string{"runs", "logs", testRunID, "--json"}, lines: true},
			{args: []string{"runs", "logs", testRunID, "-f", "--json"}, lines: true},
		},
		"nan runs ls":       {{args: []string{"runs", "ls", "--json"}}},
		"nan runs proposal": {{args: []string{"runs", "proposal", testRunID, "--json"}}},
		"nan runs reject":   {{args: []string{"runs", "reject", testRunID, "--json"}}},
		"nan runs show":     {{args: []string{"runs", "show", testRunID, "--json"}}},
		"nan secrets edit":  {{args: []string{"secrets", "edit", "GITHUB_TOKEN", "--description", "new", "--json"}}},
		"nan secrets ls":    {{args: []string{"secrets", "ls", "--json"}}},
		"nan secrets set": {
			{args: []string{"secrets", "set", "NEW_TOKEN", "--value-file", value, "--json"}},
			// Replacing the value of a secret automations use: the human
			// output names them on stderr.
			{args: []string{"secrets", "set", "GITHUB_TOKEN", "--value-stdin", "--description", "rotated", "--json"}, stdin: testValue + "\n"},
		},
		"nan secrets show": {{args: []string{"secrets", "show", "GITHUB_TOKEN", "--json"}}},
	}
}

// commandsWithJSON lists every command of the tree that has a --json flag.
func commandsWithJSON(c *cobra.Command) []string {
	var out []string
	if c.Flags().Lookup("json") != nil {
		out = append(out, c.CommandPath())
	}
	for _, sub := range c.Commands() {
		out = append(out, commandsWithJSON(sub)...)
	}
	return out
}

// Every --json command has a case below: a new one cannot ship without
// being held to the contract.
func TestEveryJSONCommandIsCovered(t *testing.T) {
	cases := jsonCases(t)
	all := commandsWithJSON(rootCmd)
	sort.Strings(all)
	if len(all) == 0 {
		t.Fatal("found no command with --json: the walk is broken")
	}
	for _, path := range all {
		if len(cases[path]) == 0 {
			t.Errorf("%s has --json but no case in jsonCases", path)
		}
	}
	for path := range cases {
		found := false
		for _, p := range all {
			found = found || p == path
		}
		if !found {
			t.Errorf("jsonCases has %s, which has no --json flag", path)
		}
	}
}

func TestJSONStdoutIsOnlyJSON(t *testing.T) {
	for path, cs := range jsonCases(t) {
		for i, c := range cs {
			t.Run(fmt.Sprintf("%s/%d", strings.TrimPrefix(path, "nan "), i), func(t *testing.T) {
				code, msg, stdout, stderr := runJSONCase(t, c)
				if code != c.code {
					t.Fatalf("%s: exit %d (want %d) %s\nstderr: %s", path, code, c.code, msg, stderr)
				}
				if c.lines {
					assertJSONLines(t, stdout)
				} else {
					assertOneJSONDocument(t, stdout)
				}
				for _, h := range hintFragments {
					if strings.Contains(stdout, h) {
						t.Errorf("stdout carries the hint %q:\n%s", h, stdout)
					}
				}
				if strings.Contains(stdout+stderr+msg, "HOOKSECRET") {
					t.Error("the webhook URL was printed")
				}
				if strings.Contains(stdout+stderr+msg, testValue) {
					t.Error("the secret value was printed")
				}
			})
		}
	}
}

// The note that started this test is on stderr, where a human still sees it.
func TestTriggersAddJSONKeepsTheNoteOnStderr(t *testing.T) {
	_, trigger, _ := jsonFixtures(t)
	code, msg, stdout, stderr := runJSONCase(t, jsonCase{args: []string{"automations", "triggers", "add", testAutoID, "-f", trigger, "--json"}})
	if code != 0 {
		t.Fatalf("exit %d %s", code, msg)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out["webhook_url"] == nil || out["signing_secret"] == nil {
		t.Errorf("stdout: %v %s", err, stdout)
	}
	if !strings.Contains(stderr, oneTimeNote) {
		t.Errorf("stderr lacks the one-time note: %q", stderr)
	}
}

func assertOneJSONDocument(t *testing.T, stdout string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Errorf("stdout carries more than one JSON document (%v):\n%s", err, stdout)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("stdout does not end with a newline: %q", stdout)
	}
}

func assertJSONLines(t *testing.T, stdout string) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		if !json.Valid(sc.Bytes()) {
			t.Errorf("line %d is not JSON: %q", n, sc.Text())
		}
	}
	if n == 0 {
		t.Errorf("no JSON lines on stdout")
	}
}

// runJSONCase runs the real command tree against one fake platform, with
// os.Stdout and os.Stderr (and the write commands' copies of them) pointed
// at files, the way a shell redirect sees them.
func runJSONCase(t *testing.T, c jsonCase) (code int, msg, stdout, stderr string) {
	t.Helper()
	write := newFakeWrite()
	phase2 := &fakePhase2{runs: &fakePlatform{
		workspaces: `[{"workspace_uuid":"w1","name":"develop","status":"running"}]`,
		finalState: "succeeded",
		events: []string{
			sse(1, "started", `{"agent":"pi","agent_version":"1.0.2","cwd":"/home/nan"}`),
			sse(2, "message", `{"role":"assistant","text":"all good","final":true}`),
		},
	}}
	if c.args[0] == "approvals" || (len(c.args) > 1 && c.args[1] == "proposal") {
		phase2.runGet = awaitingRunJSON()
	}
	writeHandler := write.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		toWrite := p == "/api/auth/me" || p == "/api/account/secrets" || strings.HasPrefix(p, "/v1/secrets") ||
			strings.HasPrefix(p, "/v1/notification-channels") ||
			strings.HasSuffix(p, "/triggers") || strings.Contains(p, "/triggers/") ||
			(strings.HasPrefix(p, "/v1/automations") && r.Method != http.MethodGet && !strings.HasSuffix(p, "/runs"))
		if toWrite {
			writeHandler.ServeHTTP(w, r)
			return
		}
		phase2.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	fakeHome(t, cliSession)
	dir := t.TempDir()
	outF, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer outF.Close()
	defer errF.Close()

	oldBase, oldIn, oldTTY, oldOut, oldErr := runsBaseURL, writeStdin, writeStdinTTY, writeStdout, writeStderr
	oldOSOut, oldOSErr := os.Stdout, os.Stderr
	restore := func() {
		os.Stdout, os.Stderr = oldOSOut, oldOSErr
		runsBaseURL, writeStdin, writeStdinTTY, writeStdout, writeStderr = oldBase, oldIn, oldTTY, oldOut, oldErr
		resetCommandFlags(rootCmd)
	}
	runsBaseURL = srv.URL
	writeStdin = strings.NewReader(c.stdin)
	writeStdinTTY = func() bool { return false }
	resetCommandFlags(rootCmd)
	os.Stdout, os.Stderr = outF, errF
	writeStdout, writeStderr = outF, errF
	func() {
		// A panic inside the command must not leave os.Stdout on the file.
		defer restore()
		code, msg = execute(t, c.args...)
	}()

	o, _ := os.ReadFile(outF.Name())
	e, _ := os.ReadFile(errF.Name())
	if bytes.Contains(o, []byte("\x1b")) {
		t.Errorf("stdout carries control characters: %q", o)
	}
	return code, msg, string(o), string(e)
}

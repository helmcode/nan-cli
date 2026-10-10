package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
)

// `nan runs logs -f` meets the same 401 and 404 as the rest, only on the
// stream: it has to end the same way.
func TestRunsLogsFollowStreamRefusals(t *testing.T) {
	defer resetRunsFlags()
	for _, tc := range []struct {
		status int
		want   int
		says   string
	}{
		{http.StatusUnauthorized, exitAuth, "nan auth login"},
		{http.StatusNotFound, exitUsage, "not found"},
	} {
		resetRunsFlags()
		logsFollow = true
		h := newHarness(t, &fakePlatform{streamStatus: tc.status})
		err := doRunsLogs(context.Background(), h.env, testRunID)
		if exitCode(err) != tc.want || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%d: exit %d (%v)", tc.status, exitCode(err), err)
		}
		if h.fake.streams != 1 {
			t.Errorf("%d: retried %d times", tc.status, h.fake.streams-1)
		}
	}
}

func TestRuntimeErrorsExit1NotUsage(t *testing.T) {
	err := runtimeError(errors.New("write /dev/stdout: broken pipe"))
	if code, _ := exitCodeFor(runCmd, err); code != 1 {
		t.Errorf("a runtime failure exits %d", code)
	}
	if runtimeError(nil) != nil {
		t.Error("nil became an error")
	}
	if exitCode(runtimeError(&ExitError{Code: exitAuth})) != exitAuth {
		t.Error("an ExitError lost its code")
	}
}

// Neither credential may reach the terminal on any path: success, refusals,
// a lost stream, detach.
func TestCredentialsNeverReachTheOutput(t *testing.T) {
	const token, key = "sess-SECRET-token", "sk-SECRET-key"
	for _, fake := range []*fakePlatform{
		{},
		{createStatus: 401, createBody: `{"error":{"message":"invalid","code":"invalid_session"}}`},
		{createStatus: 403, createBody: `{"error":{"message":"no","code":"tier_restricted"}}`},
		{createStatus: 500, createBody: `{"error":{"message":"boom"}}`},
		{streamStatus: 502},
		{streamStatus: 401},
	} {
		for _, creds := range [][2]string{{token, ""}, {"", key}} {
			h := newHarness(t, fake)
			c := api.NewRunsClient(creds[0], creds[1]).WithBaseURL(h.url)
			c.Backoff = func(int) time.Duration { return time.Millisecond }
			h.env.client, h.env.usingKey = c, creds[0] == ""
			for _, detach := range []bool{false, true} {
				opts := defaultOpts()
				opts.detach = detach
				err := doRun(context.Background(), h.env, opts, []string{"x"})
				var all bytes.Buffer
				all.Write(h.stdout.Bytes())
				all.Write(h.stderr.Bytes())
				if err != nil {
					all.WriteString(err.Error())
				}
				if strings.Contains(all.String(), "SECRET") {
					t.Errorf("a credential reached the output: %q", all.String())
				}
			}
			fake.created = nil
		}
	}
}

func TestStdinPromptOnATerminalSaysItIsWaiting(t *testing.T) {
	var notice bytes.Buffer
	_, err := buildRunRequest(promptInput{r: strings.NewReader("p"), tty: true, notice: &notice}, defaultOpts(), []string{"-"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice.String(), "Ctrl-D") {
		t.Errorf("notice = %q", notice.String())
	}
	notice.Reset()
	_, _ = buildRunRequest(promptInput{r: strings.NewReader("p"), notice: &notice}, defaultOpts(), []string{"-"})
	if notice.Len() != 0 {
		t.Error("a piped prompt printed the terminal notice")
	}
}

func TestAServerIDThatIsNotARunIDIsRefused(t *testing.T) {
	h := newHarness(t, &fakePlatform{createBody: `{"id":"../../api/keys","object":"run","state":"queued"}`})
	err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"})
	if exitCode(err) != exitUnavailable || h.fake.streams != 0 {
		t.Errorf("exit %d (%v), streams %d", exitCode(err), err, h.fake.streams)
	}
}

func TestARepeatedStateIsPrintedOnce(t *testing.T) {
	h := newHarness(t, &fakePlatform{finalState: "succeeded", events: []string{
		"event: state\ndata: {\"state\":\"running\"}\n\n",
		"event: state\ndata: {\"state\":\"running\"}\n\n",
	}})
	if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(h.stderr.String(), "── running"); n != 1 {
		t.Errorf("printed running %d times: %q", n, h.stderr.String())
	}
}

// A usage mistake is reported as one even where nobody is signed in.
func TestUsageIsCheckedBeforeTheSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	for _, args := range [][]string{
		{"run", "a", "b"},
		{"runs", "show", "not-an-id"},
		{"runs", "show"},
		{"runs", "ls", "--limit", "0"},
	} {
		rootCmd.SetArgs(args)
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		cmd, err := rootCmd.ExecuteC()
		if code, msg := exitCodeFor(cmd, err); code != exitUsage {
			t.Errorf("%v: exit %d (%s)", args, code, msg)
		}
		resetRunsFlags()
	}
}

func TestRunsLogsSaysWhenTheRunIsStillGoing(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"next_after":0,"done":false}`)
	}))
	defer srv.Close()
	var stderr bytes.Buffer
	env := &runsEnv{client: api.NewRunsClient("t", "").WithBaseURL(srv.URL), stdout: io.Discard, stderr: &stderr}
	if err := doRunsLogs(context.Background(), env, testRunID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "nan runs logs "+testRunID+" -f") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// Without -f the log comes a page at a time: each page asks after the last,
// every event prints once, and a page that does not advance ends the loop.
func TestRunsLogsPaginates(t *testing.T) {
	defer resetRunsFlags()
	for name, pages := range map[string][]string{
		"until done": {
			`{"data":[{"v":1,"seq":1,"type":"log","data":{"stream":"nan-run","text":"one"}},{"v":1,"seq":2,"type":"log","data":{"stream":"nan-run","text":"two"}}],"next_after":2,"done":false}`,
			`{"data":[{"v":1,"seq":3,"type":"log","data":{"stream":"nan-run","text":"three"}}],"next_after":3,"done":true}`,
		},
		"stuck cursor": {
			`{"data":[{"v":1,"seq":1,"type":"log","data":{"stream":"nan-run","text":"one"}}],"next_after":1,"done":false}`,
			`{"data":[{"v":1,"seq":2,"type":"log","data":{"stream":"nan-run","text":"two"}}],"next_after":1,"done":false}`,
			`{"data":[{"v":1,"seq":3,"type":"log","data":{"stream":"nan-run","text":"never"}}],"next_after":5,"done":true}`,
		},
	} {
		resetRunsFlags()
		var afters []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			afters = append(afters, r.URL.Query().Get("after"))
			_, _ = io.WriteString(w, pages[len(afters)-1])
		}))
		var stdout bytes.Buffer
		env := &runsEnv{client: api.NewRunsClient("t", "").WithBaseURL(srv.URL), stdout: &stdout, stderr: io.Discard}
		if err := doRunsLogs(context.Background(), env, testRunID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		srv.Close()
		switch name {
		case "until done":
			if strings.Join(afters, ",") != "0,2" || stdout.String() != "nan-run │ one\nnan-run │ two\nnan-run │ three\n" {
				t.Errorf("%s: afters=%v stdout=%q", name, afters, stdout.String())
			}
		case "stuck cursor":
			if strings.Join(afters, ",") != "0,1" || strings.Contains(stdout.String(), "never") {
				t.Errorf("%s: afters=%v stdout=%q", name, afters, stdout.String())
			}
		}
	}
}

func TestJSONOutputNeutralisesALoneC1Byte(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	logsJSON = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{\"data\":[{\"v\":1,\"seq\":1,\"type\":\"log\",\"data\":{\"text\":\"a\x9b31m\"}}],\"next_after\":1,\"done\":true}")
	}))
	defer srv.Close()
	var stdout bytes.Buffer
	env := &runsEnv{client: api.NewRunsClient("t", "").WithBaseURL(srv.URL), stdout: &stdout, stderr: io.Discard}
	if err := doRunsLogs(context.Background(), env, testRunID); err != nil {
		t.Fatal(err)
	}
	if bytes.IndexByte(stdout.Bytes(), 0x9b) >= 0 {
		t.Errorf("raw 0x9b in --json output: %q", stdout.String())
	}

	h := newHarness(t, &fakePlatform{events: []string{"id: 1\nevent: run_event\ndata: {\"v\":1,\"seq\":1,\"type\":\"log\",\"data\":{\"text\":\"a\x9b31m\"}}\n\n"}})
	opts := defaultOpts()
	opts.json = true
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if bytes.IndexByte(h.stdout.Bytes(), 0x9b) >= 0 {
		t.Errorf("raw 0x9b in nan run --json: %q", h.stdout.String())
	}
}

func TestContradictoryWorktreeFlagsSayWhatIsWrong(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootCmd.SetArgs([]string{"run", "--worktree", "--no-worktree", "x"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	cmd, err := rootCmd.ExecuteC()
	code, msg := exitCodeFor(cmd, err)
	runOpts = runOptions{agent: "pi", timeout: 30 * time.Minute}
	_ = runCmd.Flags().Set("worktree", "false")
	_ = runCmd.Flags().Set("no-worktree", "false")
	if code != exitUsage || !strings.Contains(msg, "pick one") {
		t.Errorf("exit %d: %s", code, msg)
	}
}

func TestReconnectsAreAnnounced(t *testing.T) {
	h := newHarness(t, &fakePlatform{dropFirst: true})
	if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "reconnecting (1/5)") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

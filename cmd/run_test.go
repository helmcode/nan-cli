package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

const testRunID = "6f1c2a9b-0000-4000-8000-000000000001"

// fakePlatform is the /v1/runs contract (AGENT-AUTOMATIONS-PHASE1 §2.1.3) as
// a test server. Each test sets the parts it cares about.
type fakePlatform struct {
	mu sync.Mutex

	workspaces   string // body of GET /api/workspaces
	createStatus int
	createBody   string
	finalState   string
	finalError   string
	events       []string // SSE frames before `end`
	streamHang   bool     // never send `end`: the run keeps going
	dropFirst    bool     // close the first stream after its events, without `end`
	streamStatus int
	cancelStatus int

	created      map[string]any
	idemKey      string
	lastEventIDs []string
	streams      int
	cancels      int
	listQuery    string
	eventsQuery  []string
}

func (f *fakePlatform) run(state string) string {
	ec := "null"
	if f.finalError != "" && api.Terminal(state) {
		ec = fmt.Sprintf("%q", f.finalError)
	}
	return fmt.Sprintf(`{"id":%q,"object":"run","workspace":{"id":"w1","name":"develop"},"agent":"pi","model":null,"trigger":"cli","state":%q,"queue_position":null,"prompt_preview":"review","cwd":"/home/nan","git_isolation":null,"timeout_seconds":1800,"cancel_requested":false,"exit_code":null,"error_code":%s,"result":null,"usage":{"tokens_in":null,"tokens_out":null,"source":null},"last_seq":2,"created_at":"2026-10-10T12:00:00Z","started_at":null,"finished_at":null}`, testRunID, state, ec)
}

func (f *fakePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces":
		_, _ = io.WriteString(w, f.workspaces)

	case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
		f.idemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		status := f.createStatus
		if status == 0 {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
		body := f.createBody
		if body == "" {
			body = f.run(api.StateQueued)
		}
		_, _ = io.WriteString(w, body)

	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs":
		f.listQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[`+f.run("running")+`],"next_cursor":null}`)

	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/"+testRunID:
		_, _ = io.WriteString(w, f.run(f.finalState))

	case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/"+testRunID+"/cancel":
		f.cancels++
		status := f.cancelStatus
		if status == 0 {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, f.run("running"))

	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/"+testRunID+"/events":
		if r.Header.Get("Accept") != "text/event-stream" {
			f.eventsQuery = append(f.eventsQuery, r.URL.RawQuery)
			_, _ = io.WriteString(w, `{"data":[{"v":1,"seq":1,"ts":"t","type":"message","data":{"role":"assistant","text":"hello\u001b[2J","final":true}}],"next_after":1,"done":true}`)
			return
		}
		f.streams++
		f.lastEventIDs = append(f.lastEventIDs, r.Header.Get("Last-Event-ID"))
		if f.streamStatus != 0 {
			w.WriteHeader(f.streamStatus)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if f.dropFirst && f.streams == 1 {
			_, _ = io.WriteString(w, f.events[0])
			return
		}
		start := 0
		if f.dropFirst {
			start = 1
		}
		for _, e := range f.events[start:] {
			_, _ = io.WriteString(w, e)
		}
		if f.streamHang {
			w.(http.Flusher).Flush()
			f.mu.Unlock()
			<-r.Context().Done()
			f.mu.Lock()
			return
		}
		_, _ = io.WriteString(w, "event: state\ndata: {\"state\":\""+f.finalState+"\",\"error_code\":null}\n\n")
		_, _ = io.WriteString(w, "event: end\ndata: "+f.run(f.finalState)+"\n\n")

	default:
		http.NotFound(w, r)
	}
}

func sse(seq int, typ, data string) string {
	return fmt.Sprintf("id: %d\nevent: run_event\ndata: {\"v\":1,\"seq\":%d,\"ts\":\"2026-10-10T12:00:00Z\",\"type\":%q,\"data\":%s}\n\n", seq, seq, typ, data)
}

type harness struct {
	fake       *fakePlatform
	url        string
	env        *runsEnv
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	interrupts chan os.Signal
}

func newHarness(t *testing.T, fake *fakePlatform) *harness {
	t.Helper()
	if fake.workspaces == "" {
		fake.workspaces = `[{"workspace_uuid":"w1","name":"develop","status":"running"}]`
	}
	if fake.finalState == "" {
		fake.finalState = api.StateSucceeded
	}
	if fake.events == nil {
		fake.events = []string{
			sse(1, "started", `{"agent":"pi","agent_version":"1.0.2","cwd":"/home/nan"}`),
			sse(2, "message", `{"role":"assistant","text":"all good","final":true}`),
		}
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client := api.NewRunsClient("session-token", "").WithBaseURL(srv.URL)
	client.Backoff = func(int) time.Duration { return time.Millisecond }
	client.IdleTimeout = 2 * time.Second
	h := &harness{
		fake:       fake,
		url:        srv.URL,
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		interrupts: make(chan os.Signal, 2),
	}
	h.env = &runsEnv{client: client, stdout: h.stdout, stderr: h.stderr, interrupts: h.interrupts}
	return h
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return -1
}

func defaultOpts() runOptions { return runOptions{agent: "pi"} }

// doRun is what `nan run`'s RunE does once it has a session: build the
// request from the arguments, then start and follow the run.
func doRun(ctx context.Context, env *runsEnv, opts runOptions, args []string) error {
	return doRunWithStdin(ctx, env, opts, args, strings.NewReader(""))
}

func doRunWithStdin(ctx context.Context, env *runsEnv, opts runOptions, args []string, stdin io.Reader) error {
	req, err := buildRunRequest(promptInput{r: stdin}, opts, args)
	if err != nil {
		return err
	}
	return startRun(ctx, env, opts, req)
}

func TestRunStreamsAndExitsWithTheRunsCode(t *testing.T) {
	for _, tc := range []struct {
		state, errorCode string
		want             int
	}{
		{api.StateSucceeded, "", 0},
		{api.StateFailed, "agent_failed", 1},
		{api.StateTimedOut, "timed_out", 2},
		{api.StateCancelled, "cancelled", 3},
		{api.StateFailed, "config_error", 4},
		{api.StateFailed, "agent_not_installed", 4},
		{api.StateFailed, "no_inference_key", 4},
	} {
		t.Run(tc.state+"/"+tc.errorCode, func(t *testing.T) {
			h := newHarness(t, &fakePlatform{finalState: tc.state, finalError: tc.errorCode})
			err := doRun(context.Background(), h.env, defaultOpts(), []string{"review PR 42"})
			if got := exitCode(err); got != tc.want {
				t.Fatalf("exit %d (%v), want %d", got, err, tc.want)
			}
			if !strings.Contains(h.stdout.String(), "all good") {
				t.Errorf("stdout = %q", h.stdout.String())
			}
			if !strings.Contains(h.stderr.String(), "run "+testRunID) {
				t.Errorf("stderr does not name the run: %q", h.stderr.String())
			}
			if h.fake.created["trigger"] != "cli" || h.fake.created["prompt"] != "review PR 42" || h.fake.created["agent"] != "pi" {
				t.Errorf("created with %v", h.fake.created)
			}
		})
	}
}

func TestRunReconnectsWithLastEventID(t *testing.T) {
	h := newHarness(t, &fakePlatform{dropFirst: true})
	if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(h.fake.lastEventIDs) != "[ 1]" {
		t.Errorf("Last-Event-ID per connection = %q", h.fake.lastEventIDs)
	}
	if strings.Count(h.stdout.String(), "started pi") != 1 || !strings.Contains(h.stdout.String(), "all good") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

func TestRunStreamLostExits69AndNamesTheRun(t *testing.T) {
	h := newHarness(t, &fakePlatform{streamStatus: http.StatusBadGateway})
	err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"})
	if exitCode(err) != exitUnavailable {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if !strings.Contains(err.Error(), "nan runs logs "+testRunID+" -f") {
		t.Errorf("message does not say how to pick it up again: %v", err)
	}
	if h.fake.streams != 6 {
		t.Errorf("connected %d times", h.fake.streams)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCtrlCDetaches(t *testing.T) {
	old := cancelWindow
	cancelWindow = 50 * time.Millisecond
	defer func() { cancelWindow = old }()

	h := newHarness(t, &fakePlatform{streamHang: true})
	done := make(chan error, 1)
	go func() { done <- doRun(context.Background(), h.env, defaultOpts(), []string{"x"}) }()
	waitFor(t, func() bool { h.fake.mu.Lock(); defer h.fake.mu.Unlock(); return h.fake.streams == 1 })
	h.interrupts <- os.Interrupt

	err := <-done
	if exitCode(err) != exitDetached {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	for _, want := range []string{"nan runs logs " + testRunID + " -f", "nan runs cancel " + testRunID} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("stderr lacks %q: %q", want, h.stderr.String())
		}
	}
	if h.fake.cancels != 0 {
		t.Error("a single Ctrl-C cancelled the run")
	}
}

func TestDoubleCtrlCCancels(t *testing.T) {
	old := cancelWindow
	cancelWindow = 5 * time.Second
	defer func() { cancelWindow = old }()

	h := newHarness(t, &fakePlatform{streamHang: true})
	done := make(chan error, 1)
	go func() { done <- doRun(context.Background(), h.env, defaultOpts(), []string{"x"}) }()
	waitFor(t, func() bool { h.fake.mu.Lock(); defer h.fake.mu.Unlock(); return h.fake.streams == 1 })
	h.interrupts <- os.Interrupt
	h.interrupts <- os.Interrupt

	err := <-done
	if exitCode(err) != exitCancelled {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if h.fake.cancels != 1 {
		t.Errorf("cancel sent %d times", h.fake.cancels)
	}
	if !strings.Contains(h.stderr.String(), "cancel requested") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestDefaultWorkspace(t *testing.T) {
	t.Run("exactly one is used", func(t *testing.T) {
		h := newHarness(t, &fakePlatform{workspaces: `[{"workspace_uuid":"w9","name":"solo","status":"running"}]`})
		if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); err != nil {
			t.Fatal(err)
		}
		if h.fake.created["workspace"] != "solo" {
			t.Errorf("created in %v", h.fake.created["workspace"])
		}
	})
	t.Run("more than one is ambiguous", func(t *testing.T) {
		h := newHarness(t, &fakePlatform{workspaces: `[{"workspace_uuid":"a","name":"develop"},{"workspace_uuid":"b","name":"prod\u001b[31m"}]`})
		err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"})
		if exitCode(err) != exitUsage {
			t.Fatalf("exit %d (%v)", exitCode(err), err)
		}
		if !strings.Contains(err.Error(), "develop") || !strings.Contains(err.Error(), "prod") || strings.Contains(err.Error(), "\x1b") {
			t.Errorf("message = %q", err.Error())
		}
		if h.fake.created != nil {
			t.Error("created a run anyway")
		}
	})
	t.Run("none", func(t *testing.T) {
		h := newHarness(t, &fakePlatform{workspaces: `[]`})
		if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); exitCode(err) != exitUsage {
			t.Fatalf("exit %d (%v)", exitCode(err), err)
		}
	})
	t.Run("--ws skips the lookup", func(t *testing.T) {
		h := newHarness(t, &fakePlatform{workspaces: `not json`})
		opts := defaultOpts()
		opts.workspace = "named"
		if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
			t.Fatal(err)
		}
		if h.fake.created["workspace"] != "named" {
			t.Errorf("created in %v", h.fake.created["workspace"])
		}
	})
}

func TestExpiredSessionExits65(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid session","type":"authentication_error","code":"invalid_session"}}`)
	}))
	defer srv.Close()
	env := &runsEnv{client: api.NewRunsClient("stale", "").WithBaseURL(srv.URL), stdout: io.Discard, stderr: io.Discard}

	for name, call := range map[string]func() error{
		"run":    func() error { return doRun(context.Background(), env, defaultOpts(), []string{"x"}) },
		"ls":     func() error { return doRunsLs(context.Background(), env) },
		"show":   func() error { return doRunsShow(context.Background(), env, testRunID) },
		"cancel": func() error { return doRunsCancel(context.Background(), env, testRunID) },
	} {
		err := call()
		if exitCode(err) != exitAuth || !errors.Is(err, api.ErrSessionExpired) && !strings.Contains(err.Error(), "session has expired") {
			t.Errorf("%s: exit %d (%v)", name, exitCode(err), err)
		}
		if !strings.Contains(err.Error(), "nan auth login") {
			t.Errorf("%s: does not say how to fix it: %v", name, err)
		}
	}
}

func TestNoSessionExits65(t *testing.T) {
	t.Setenv(tokenEnvVar, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	_, err := newRunsEnv()
	if exitCode(err) != exitAuth {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
}

func TestAPIKeyIsUsedWithoutASession(t *testing.T) {
	t.Setenv(tokenEnvVar, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "nan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "nan", "session.json"), []byte(`{"token":"","apiKey":"sk-member"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := newRunsEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !env.cred.usingKey() {
		t.Error("a session with only an API key does not use it")
	}
}

func TestControlCharsAreStrippedFromTheStream(t *testing.T) {
	h := newHarness(t, &fakePlatform{events: []string{
		sse(1, "message", `{"role":"assistant","text":"hi\u001b]0;pwned\u0007\u001b[2J\r","final":true}`),
		sse(2, "tool_call", `{"id":"1","name":"bash\u001b[31m","args_preview":"rm\u009b"}`),
	}})
	if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	all := h.stdout.String() + h.stderr.String()
	if strings.ContainsAny(all, "\x1b\x07\r\u009b") {
		t.Errorf("control bytes reached the terminal: %q", all)
	}
	if !strings.Contains(h.stdout.String(), "hi") || !strings.Contains(h.stdout.String(), "→ bash rm") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

func TestRunJSONStreamsEventLinesThenTheRun(t *testing.T) {
	h := newHarness(t, &fakePlatform{})
	opts := defaultOpts()
	opts.json = true
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout lines = %q", lines)
	}
	var ev api.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev.Seq != 1 || ev.Type != "started" {
		t.Errorf("first line %q: %v", lines[0], err)
	}
	var run api.Run
	if err := json.Unmarshal([]byte(lines[2]), &run); err != nil || run.Object != "run" || run.State != api.StateSucceeded {
		t.Errorf("last line %q: %v", lines[2], err)
	}
}

func TestDetach(t *testing.T) {
	h := newHarness(t, &fakePlatform{})
	opts := defaultOpts()
	opts.detach = true
	err := doRun(context.Background(), h.env, opts, []string{"x"})
	if exitCode(err) != exitDetached {
		t.Fatalf("exit %d (%v)", exitCode(err), err)
	}
	if h.stdout.String() != testRunID+"\n" {
		t.Errorf("stdout = %q, want only the id", h.stdout.String())
	}
	if h.fake.streams != 0 {
		t.Error("--detach streamed")
	}

	h = newHarness(t, &fakePlatform{})
	opts.json = true
	_ = doRun(context.Background(), h.env, opts, []string{"x"})
	var run api.Run
	if err := json.Unmarshal(h.stdout.Bytes(), &run); err != nil || run.ID != testRunID {
		t.Errorf("--detach --json printed %q", h.stdout.String())
	}
}

func TestIdempotencyKeyAndReplay(t *testing.T) {
	h := newHarness(t, &fakePlatform{createStatus: http.StatusOK})
	opts := defaultOpts()
	opts.idempotencyKey = "deploy-42"
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if h.fake.idemKey != "deploy-42" {
		t.Errorf("Idempotency-Key = %q", h.fake.idemKey)
	}
	if !strings.Contains(h.stderr.String(), "already exists") {
		t.Errorf("a replay was not called one: %q", h.stderr.String())
	}

	opts.idempotencyKey = "has space"
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); exitCode(err) != exitUsage {
		t.Errorf("a bad key: exit %d", exitCode(err))
	}
}

func TestRunFlagsShapeTheRequest(t *testing.T) {
	h := newHarness(t, &fakePlatform{})
	opts := defaultOpts()
	opts.agent = "hermes"
	opts.model = "glm5.3"
	opts.cwd = "/home/nan/projects/api"
	opts.noWorktree = true
	opts.timeout, opts.timeoutSet = 10*time.Minute, true
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	c := h.fake.created
	if c["agent"] != "hermes" || c["model"] != "glm5.3" || c["cwd"] != "/home/nan/projects/api" ||
		c["git_isolation"] != false || c["timeout_seconds"] != float64(600) {
		t.Errorf("created with %v", c)
	}

	h = newHarness(t, &fakePlatform{})
	if err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"timeout_seconds", "git_isolation", "model", "cwd"} {
		if _, ok := h.fake.created[k]; ok {
			t.Errorf("sent %s without the flag, overriding the platform default", k)
		}
	}
}

func TestRunUsageErrorsExit64BeforeSendingAnything(t *testing.T) {
	long := strings.Repeat("a", maxPromptBytes+1)
	file := filepath.Join(t.TempDir(), "prompt.md")
	_ = os.WriteFile(file, []byte("from a file"), 0o600)
	for name, tc := range map[string]struct {
		mutate func(*runOptions)
		args   []string
	}{
		"no prompt":           {func(*runOptions) {}, nil},
		"two arguments":       {func(*runOptions) {}, []string{"a", "b"}},
		"argument and -f":     {func(o *runOptions) { o.file = file }, []string{"a"}},
		"empty prompt":        {func(*runOptions) {}, []string{"   "}},
		"too long":            {func(*runOptions) {}, []string{long}},
		"NUL":                 {func(*runOptions) {}, []string{"a\x00b"}},
		"bad agent":           {func(o *runOptions) { o.agent = "codex" }, []string{"x"}},
		"bad model":           {func(o *runOptions) { o.model = "a b" }, []string{"x"}},
		"timeout too short":   {func(o *runOptions) { o.timeout, o.timeoutSet = 30*time.Second, true }, []string{"x"}},
		"timeout too long":    {func(o *runOptions) { o.timeout, o.timeoutSet = 3*time.Hour, true }, []string{"x"}},
		"both worktree flags": {func(o *runOptions) { o.worktree, o.noWorktree = true, true }, []string{"x"}},
		"missing prompt file": {func(o *runOptions) { o.file = file + ".nope" }, nil},
	} {
		h := newHarness(t, &fakePlatform{})
		opts := defaultOpts()
		tc.mutate(&opts)
		err := doRun(context.Background(), h.env, opts, tc.args)
		if exitCode(err) != exitUsage {
			t.Errorf("%s: exit %d (%v)", name, exitCode(err), err)
		}
		if h.fake.created != nil {
			t.Errorf("%s: sent a run anyway", name)
		}
	}
}

func TestPromptFromFileAndStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prompt.md")
	_ = os.WriteFile(file, []byte("# Task\nfrom a file\n"), 0o600)

	h := newHarness(t, &fakePlatform{})
	opts := defaultOpts()
	opts.file = file
	if err := doRun(context.Background(), h.env, opts, nil); err != nil {
		t.Fatal(err)
	}
	if h.fake.created["prompt"] != "# Task\nfrom a file\n" {
		t.Errorf("prompt = %q", h.fake.created["prompt"])
	}

	h = newHarness(t, &fakePlatform{})
	if err := doRunWithStdin(context.Background(), h.env, defaultOpts(), []string{"-"}, strings.NewReader("from stdin")); err != nil {
		t.Fatal(err)
	}
	if h.fake.created["prompt"] != "from stdin" {
		t.Errorf("prompt = %q", h.fake.created["prompt"])
	}
}

func TestAdmissionRefusalsMapToExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code      string
		param     string
		want      int
		mentioned string
	}{
		{http.StatusConflict, "agent_not_installed", "", exitConfig, "not installed"},
		{http.StatusConflict, "no_inference_key", "", exitConfig, ""},
		{http.StatusConflict, "workspace_not_running", "", exitUnavailable, "not running"},
		{http.StatusConflict, "idempotency_conflict", "", exitUsage, "idempotency key"},
		{http.StatusForbidden, "tier_restricted", "", exitAuth, "membership"},
		{http.StatusForbidden, "workspace_key_not_allowed", "", exitAuth, "workspace key"},
		{http.StatusBadRequest, "invalid_request", "cwd", exitUsage, "cwd"},
		{http.StatusNotFound, "not_found", "", exitUsage, "not found"},
		{http.StatusTooManyRequests, "run_queue_full", "", exitUnavailable, "queued"},
		{http.StatusTooManyRequests, "rate_limited", "", exitUnavailable, "last hour"},
		{http.StatusServiceUnavailable, "", "", exitUnavailable, ""},
	} {
		param := "null"
		if tc.param != "" {
			param = fmt.Sprintf("%q", tc.param)
		}
		h := newHarness(t, &fakePlatform{
			createStatus: tc.status,
			createBody:   fmt.Sprintf(`{"error":{"message":"the agent is not installed","type":"invalid_request_error","param":%s,"code":%q}}`, param, tc.code),
		})
		err := doRun(context.Background(), h.env, defaultOpts(), []string{"x"})
		if exitCode(err) != tc.want {
			t.Errorf("%d %s: exit %d (%v), want %d", tc.status, tc.code, exitCode(err), err, tc.want)
			continue
		}
		if tc.mentioned != "" && !strings.Contains(err.Error(), tc.mentioned) {
			t.Errorf("%d %s: message %q lacks %q", tc.status, tc.code, err.Error(), tc.mentioned)
		}
		if h.fake.streams != 0 {
			t.Errorf("%d %s: streamed a run that was refused", tc.status, tc.code)
		}
	}
}

// What cobra refuses before RunE - an unknown flag, a missing id, flags that
// exclude each other - is a usage error on these commands, exit 64. Elsewhere
// it stays 1, which is what every other command has always exited with.
func TestCobraRefusalsExit64OnRunCommands(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--worktree", "--no-worktree", "x"},
		{"run", "--no-such-flag", "x"},
		{"runs", "show"},
		{"runs", "logs", "a", "b"},
	} {
		rootCmd.SetArgs(args)
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		cmd, err := rootCmd.ExecuteC()
		code, _ := exitCodeFor(cmd, err)
		if err == nil || code != exitUsage {
			t.Errorf("%v: exit %d (%v)", args, code, err)
		}
		runOpts = runOptions{agent: "pi", timeout: 30 * time.Minute}
		_ = runCmd.Flags().Set("worktree", "false")
		_ = runCmd.Flags().Set("no-worktree", "false")
	}
	if code, _ := exitCodeFor(meCmd, errors.New("boom")); code != 1 {
		t.Errorf("an ordinary command's failure exits %d", code)
	}
	if code, msg := exitCodeFor(runCmd, &ExitError{Code: exitDetached}); code != exitDetached || msg != "" {
		t.Errorf("a silent ExitError: %d %q", code, msg)
	}
}

func TestRunsShowRejectsWhatIsNotAnID(t *testing.T) {
	h := newHarness(t, &fakePlatform{})
	for _, id := range []string{"../../api/keys", "6f1c", "6f1c2a9b-0000-4000-8000-00000000000g"} {
		if err := doRunsShow(context.Background(), h.env, id); exitCode(err) != exitUsage {
			t.Errorf("%q: exit %d (%v)", id, exitCode(err), err)
		}
	}
}

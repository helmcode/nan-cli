package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const runID = "6f1c2a9b-0000-4000-8000-000000000001"

func testRunsClient(t *testing.T, h http.Handler, token, key string) *RunsClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewRunsClient(token, key).WithBaseURL(srv.URL)
	c.Backoff = func(int) time.Duration { return time.Millisecond }
	c.IdleTimeout = 200 * time.Millisecond
	return c
}

func runJSON(state string) string {
	return fmt.Sprintf(`{"id":%q,"object":"run","workspace":{"id":"w1","name":"develop"},"agent":"pi","state":%q,"last_seq":2,"created_at":"2026-10-10T12:00:00Z"}`, runID, state)
}

func TestRunsClientSendsTheSessionAsACookie(t *testing.T) {
	var cookie, authz string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, authz = r.Header.Get("Cookie"), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, runJSON("running"))
	}), "sess", "sk-key")
	if _, err := c.GetRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if cookie != "nan_session=sess" {
		t.Errorf("cookie = %q", cookie)
	}
	if authz != "" {
		t.Errorf("sent the API key alongside a session: %q", authz)
	}
}

func TestRunsClientFallsBackToTheAPIKey(t *testing.T) {
	var cookie, authz string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, authz = r.Header.Get("Cookie"), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, runJSON("running"))
	}), "", "sk-key")
	if _, err := c.GetRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if authz != "Bearer sk-key" || cookie != "" {
		t.Errorf("authz=%q cookie=%q", authz, cookie)
	}
}

func TestRunsClient401IsAnExpiredSession(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid session","type":"auth_error","code":"invalid_api_key"}}`)
	}), "stale", "")
	if _, err := c.GetRun(context.Background(), runID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
}

func TestRunsClientParsesBothErrorEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		body, code, msg, param string
	}{
		{`{"error":{"message":"workspace is not running","type":"invalid_request_error","param":null,"code":"workspace_not_running"}}`, "workspace_not_running", "workspace is not running", ""},
		{`{"error":{"message":"bad timeout","type":"invalid_request_error","param":"timeout_seconds","code":"invalid_request"}}`, "invalid_request", "bad timeout", "timeout_seconds"},
		{`{"error":"workspaces are not enabled"}`, "", "workspaces are not enabled", ""},
	} {
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, tc.body)
		}), "t", "")
		_, err := c.GetRun(context.Background(), runID)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("%s: got %v", tc.body, err)
		}
		if apiErr.Status != 409 || apiErr.Code != tc.code || apiErr.Message != tc.msg || apiErr.Param != tc.param {
			t.Errorf("%s: parsed %+v", tc.body, apiErr)
		}
	}
}

func TestCreateRunSendsTheBodyAndTheIdempotencyKey(t *testing.T) {
	var got map[string]any
	var idem, method, path string
	status := http.StatusCreated
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, idem = r.Method, r.URL.Path, r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, runJSON("queued"))
	}), "t", "")

	timeout := 600
	iso := false
	run, replayed, err := c.CreateRun(context.Background(), CreateRunRequest{
		Workspace: "develop", Agent: "pi", Prompt: "review PR 42",
		TimeoutSeconds: &timeout, GitIsolation: &iso, Trigger: "cli",
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/v1/runs" || idem != "key-1" {
		t.Errorf("%s %s idem=%q", method, path, idem)
	}
	if replayed {
		t.Error("a 201 reads as a replay")
	}
	if run.ID != runID || run.State != StateQueued {
		t.Errorf("run = %+v", run)
	}
	if got["workspace"] != "develop" || got["agent"] != "pi" || got["prompt"] != "review PR 42" ||
		got["timeout_seconds"] != float64(600) || got["git_isolation"] != false || got["trigger"] != "cli" {
		t.Errorf("body = %v", got)
	}
	for _, absent := range []string{"cwd", "model"} {
		if _, ok := got[absent]; ok {
			t.Errorf("sent %q although it was not set, which overrides the platform default", absent)
		}
	}

	status = http.StatusOK
	if _, replayed, _ = c.CreateRun(context.Background(), CreateRunRequest{Workspace: "develop", Agent: "pi", Prompt: "x"}, "key-1"); !replayed {
		t.Error("a 200 on create is an idempotent replay")
	}
}

func TestListRunsSendsTheFilters(t *testing.T) {
	var query string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[`+runJSON("running")+`],"next_cursor":"abc"}`)
	}), "t", "")
	list, err := c.ListRuns(context.Background(), ListRunsParams{Workspace: "develop", State: "running", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if query != "limit=5&state=running&workspace=develop" {
		t.Errorf("query = %q", query)
	}
	if len(list.Data) != 1 || list.NextCursor == nil || *list.NextCursor != "abc" {
		t.Errorf("list = %+v", list)
	}
}

func TestCancelRunTellsAcceptedFromAlreadyFinished(t *testing.T) {
	status := http.StatusAccepted
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/"+runID+"/cancel" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, runJSON("running"))
	}), "t", "")
	if _, changed, err := c.CancelRun(context.Background(), runID); err != nil || !changed {
		t.Errorf("202: changed=%v err=%v", changed, err)
	}
	status = http.StatusOK
	if _, changed, err := c.CancelRun(context.Background(), runID); err != nil || changed {
		t.Errorf("200: changed=%v err=%v", changed, err)
	}
}

func TestRunIDsAreEscapedIntoThePath(t *testing.T) {
	var raw string
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.EscapedPath()
		_, _ = io.WriteString(w, runJSON("running"))
	}), "t", "")
	_, _ = c.GetRun(context.Background(), "../../api/keys")
	if strings.Contains(raw, "/api/keys") {
		t.Errorf("an id walked out of /v1/runs: %q", raw)
	}
}

func TestListWorkspacesReadsTheArray(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces" {
			t.Errorf("path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"workspace_uuid":"u1","name":"develop","status":"running","ssh_command":"ssh x"}]`)
	}), "t", "")
	list, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "develop" || list[0].ID != "u1" {
		t.Errorf("list = %+v", list)
	}
}

// --- the event stream ---

func sseEvent(seq int64, typ, data string) string {
	return fmt.Sprintf("id: %d\nevent: run_event\ndata: {\"v\":1,\"seq\":%d,\"ts\":\"t\",\"type\":%q,\"data\":%s}\n\n", seq, seq, typ, data)
}

func sseEnd(state string) string { return "event: end\ndata: " + runJSON(state) + "\n\n" }

func collect(frames *[]Frame, mu *sync.Mutex) func(Frame) {
	return func(f Frame) {
		mu.Lock()
		defer mu.Unlock()
		*frames = append(*frames, f)
	}
}

func TestFollowReadsEventsStatesAndEnd(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if r.URL.Query().Get("after") != "0" {
			t.Errorf("after = %q", r.URL.Query().Get("after"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": hb\n\n")
		_, _ = io.WriteString(w, "event: state\ndata: {\"state\":\"running\",\"error_code\":null}\n\n")
		_, _ = io.WriteString(w, sseEvent(1, "message", `{"role":"assistant","text":"hi","final":true}`))
		// CRLF endings and a multi-line data field are still one frame.
		_, _ = io.WriteString(w, "id: 2\r\nevent: run_event\r\ndata: {\"v\":1,\"seq\":2,\r\ndata: \"type\":\"log\",\"data\":{}}\r\n\r\n")
		_, _ = io.WriteString(w, sseEnd("succeeded"))
	}), "t", "")

	var frames []Frame
	var mu sync.Mutex
	run, err := c.Follow(context.Background(), runID, 0, collect(&frames, &mu))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateSucceeded {
		t.Errorf("end run = %+v", run)
	}
	if len(frames) != 3 || frames[0].State == nil || frames[0].State.State != "running" ||
		frames[1].Event == nil || frames[1].Event.Seq != 1 || frames[2].Event == nil || frames[2].Event.Type != "log" {
		t.Fatalf("frames = %+v", frames)
	}
}

// The platform closes a stream after 30 minutes and anything in between can
// drop it sooner. The reconnect has to resume after the last event seen, and
// an event the server replays anyway must not print twice.
func TestFollowReconnectsWithLastEventID(t *testing.T) {
	var mu sync.Mutex
	var lastEventIDs []string
	calls := 0
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		lastEventIDs = append(lastEventIDs, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			_, _ = io.WriteString(w, sseEvent(1, "log", `{}`))
			_, _ = io.WriteString(w, sseEvent(2, "log", `{}`))
			// and the connection closes without `end`
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = io.WriteString(w, sseEvent(2, "log", `{}`)) // a replayed duplicate
			_, _ = io.WriteString(w, sseEvent(3, "log", `{}`))
			_, _ = io.WriteString(w, sseEnd("failed"))
		}
	}), "t", "")

	var frames []Frame
	var fmu sync.Mutex
	run, err := c.Follow(context.Background(), runID, 0, collect(&frames, &fmu))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateFailed {
		t.Errorf("state = %s", run.State)
	}
	var seqs []int64
	for _, f := range frames {
		seqs = append(seqs, f.Event.Seq)
	}
	if fmt.Sprint(seqs) != "[1 2 3]" {
		t.Errorf("seqs = %v", seqs)
	}
	if fmt.Sprint(lastEventIDs) != "[ 2 2]" {
		t.Errorf("Last-Event-ID per connection = %q", lastEventIDs)
	}
}

func TestFollowGivesUpAfterFiveFailuresInARow(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}), "t", "")
	_, err := c.Follow(context.Background(), runID, 0, func(Frame) {})
	if !errors.Is(err, ErrStreamLost) {
		t.Fatalf("got %v", err)
	}
	if calls != 6 {
		t.Errorf("connected %d times, want the first try plus 5 retries", calls)
	}
}

// A stream that is accepted and then says nothing - not even the 15 s
// heartbeat - is dead in a way the TCP connection does not notice.
func TestFollowReconnectsAfterIdleTimeout(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		_, _ = io.WriteString(w, sseEnd("succeeded"))
	}), "t", "")
	defer close(release)
	c.IdleTimeout = 50 * time.Millisecond
	run, err := c.Follow(context.Background(), runID, 0, func(Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateSucceeded || calls != 2 {
		t.Errorf("state=%s calls=%d", run.State, calls)
	}
}

// The 30 s timeout every other request has would cut a healthy stream off
// mid-run, so the stream client must not have one.
func TestTheStreamHasNoClientTimeout(t *testing.T) {
	c := NewRunsClient("t", "")
	if c.stream.Timeout != 0 {
		t.Errorf("stream timeout = %s", c.stream.Timeout)
	}
	if c.http.Timeout == 0 {
		t.Error("the ordinary requests lost their timeout")
	}
	if c.StreamRetries != 5 || c.IdleTimeout != 45*time.Second {
		t.Errorf("retries=%d idle=%s", c.StreamRetries, c.IdleTimeout)
	}
}

func TestFollowDoesNotRetryAuthOrNotFound(t *testing.T) {
	for _, tc := range []struct {
		status int
		check  func(error) bool
	}{
		{http.StatusUnauthorized, func(err error) bool { return errors.Is(err, ErrSessionExpired) }},
		{http.StatusNotFound, func(err error) bool { var e *APIError; return errors.As(err, &e) && e.Status == 404 }},
	} {
		calls := 0
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(tc.status)
		}), "t", "")
		_, err := c.Follow(context.Background(), runID, 0, func(Frame) {})
		if !tc.check(err) || calls != 1 {
			t.Errorf("%d: err=%v calls=%d", tc.status, err, calls)
		}
	}
}

func TestFollowStopsWhenTheContextIsCancelled(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}), "t", "")
	c.IdleTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := c.Follow(ctx, runID, 0, func(Frame) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

// A connection that is accepted and never answered must not hang the CLI:
// the idle limit covers the wait for the headers as well.
func TestFollowTimesOutWaitingForHeaders(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}), "t", "")
	c.IdleTimeout = 30 * time.Millisecond
	c.StreamRetries = 1
	done := make(chan error, 1)
	go func() { _, err := c.Follow(context.Background(), runID, 0, func(Frame) {}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStreamLost) {
			t.Errorf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Follow hung on a server that never sends headers")
	}
}

// Something in between answering 200 with JSON - or with a heartbeat and a
// hang-up - must count as a failure, or the client reconnects forever.
func TestFollowGivesUpOnStreamsThatNeverWork(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"not an event stream": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
		},
		"heartbeat and hang up": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": hb\n\n")
		},
	} {
		var mu sync.Mutex
		calls := 0
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls++
			mu.Unlock()
			h(w, r)
		}), "t", "")
		done := make(chan error, 1)
		go func() { _, err := c.Follow(context.Background(), runID, 0, func(Frame) {}); done <- err }()
		select {
		case err := <-done:
			mu.Lock()
			n := calls
			mu.Unlock()
			if !errors.Is(err, ErrStreamLost) || n != 6 {
				t.Errorf("%s: err=%v calls=%d", name, err, n)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: reconnected forever", name)
		}
	}
}

// The budget is for failures in a row: a connection that delivered frames,
// or that stayed up heartbeating, starts the count again.
func TestFollowRetryBudgetResetsOnWorkingConnections(t *testing.T) {
	t.Run("frames", func(t *testing.T) {
		var mu sync.Mutex
		calls := 0
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			switch {
			case n == 4:
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, sseEvent(1, "log", `{}`))
			case n == 8:
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, sseEnd("succeeded"))
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}), "t", "")
		if _, err := c.Follow(context.Background(), runID, 0, func(Frame) {}); err != nil {
			t.Fatalf("7 failures split by a working connection gave up: %v", err)
		}
	})
	t.Run("long-lived heartbeats", func(t *testing.T) {
		var mu sync.Mutex
		calls := 0
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if n < 9 {
				_, _ = io.WriteString(w, ": hb\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
				return
			}
			_, _ = io.WriteString(w, sseEnd("succeeded"))
		}), "t", "")
		c.HealthyAfter = 10 * time.Millisecond
		if _, err := c.Follow(context.Background(), runID, 0, func(Frame) {}); err != nil {
			t.Fatalf("a quiet run whose stream is recycled gave up: %v", err)
		}
	})
}

func TestFollowRetriesTooManyRequests(t *testing.T) {
	var calls atomic.Int32
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseEnd("succeeded"))
	}), "t", "")
	if _, err := c.Follow(context.Background(), runID, 0, func(Frame) {}); err != nil || calls.Load() != 2 {
		t.Errorf("err=%v calls=%d", err, calls.Load())
	}
}

func TestFollowSkipsAMalformedEnd(t *testing.T) {
	var calls atomic.Int32
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = io.WriteString(w, "event: end\ndata: {not json\n\n")
			return
		}
		_, _ = io.WriteString(w, sseEnd("failed"))
	}), "t", "")
	run, err := c.Follow(context.Background(), runID, 0, func(Frame) {})
	if err != nil || run.State != StateFailed {
		t.Errorf("run=%v err=%v", run, err)
	}
}

func TestFollowRefusesOversizedLinesAndFrames(t *testing.T) {
	for name, body := range map[string]string{
		"line":  "data: " + strings.Repeat("a", maxSSELine+1) + "\n\n",
		"frame": strings.Repeat("data: "+strings.Repeat("a", maxSSELine/2)+"\n", 10) + "\n",
	} {
		var calls atomic.Int32
		c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, body)
		}), "t", "")
		_, err := c.Follow(context.Background(), runID, 0, func(Frame) {})
		if err == nil || errors.Is(err, ErrStreamLost) || calls.Load() != 1 {
			t.Errorf("%s: err=%v calls=%d (want a fatal error, no retry)", name, err, calls.Load())
		}
	}
}

// The idle limit is about a connection that has gone quiet, not about one
// slow to finish a line: an oversized line that trickles in for longer than
// IdleTimeout must still end as the fatal oversize error on the first
// connection, never as an idle reconnect. (On a slow runner the 1 MiB line of
// the test above took longer than the idle limit to arrive, and was retried.)
func TestFollowOversizedLineArrivingSlowlyIsNotIdle(t *testing.T) {
	var calls atomic.Int32
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ")
		chunk := strings.Repeat("a", 128<<10)
		for sent := 0; sent <= maxSSELine; sent += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}), "t", "")
	c.IdleTimeout = 60 * time.Millisecond
	_, err := c.Follow(context.Background(), runID, 0, func(Frame) {})
	if err == nil || errors.Is(err, ErrStreamLost) || !strings.Contains(err.Error(), "stream line over") || calls.Load() != 1 {
		t.Errorf("err=%v calls=%d (want the fatal oversize error, no retry)", err, calls.Load())
	}
}

// /v1/runs never redirects, and following one would carry the credential to
// wherever it points.
func TestRunsClientDoesNotFollowRedirects(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}), "secret-session", "")
	_, _ = c.GetRun(context.Background(), runID)
	_, _ = c.Follow(context.Background(), runID, 0, func(Frame) {})
	if hit {
		t.Error("followed a redirect with the session attached")
	}
}

func TestTerminal(t *testing.T) {
	for state, want := range map[string]bool{"queued": false, "starting": false, "running": false, "succeeded": true, "failed": true, "cancelled": true, "timed_out": true} {
		if Terminal(state) != want {
			t.Errorf("Terminal(%s) != %v", state, want)
		}
	}
}

// A server that ignores Last-Event-ID and replays old events before hanging
// up has shown nothing new: that is not a working connection.
func TestFollowReplaysAloneDoNotRefillTheBudget(t *testing.T) {
	var calls atomic.Int32
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseEvent(1, "log", `{}`))
	}), "t", "")
	done := make(chan error, 1)
	go func() { _, err := c.Follow(context.Background(), runID, 0, func(Frame) {}); done <- err }()
	select {
	case err := <-done:
		// the first connection delivers seq 1 and closes (one failure, budget
		// fresh), then five that only replay it use the rest
		if !errors.Is(err, ErrStreamLost) || calls.Load() != 6 {
			t.Errorf("err=%v calls=%d", err, calls.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reconnected forever on replays")
	}
}

func TestFollowRefusesAnEndForAnotherRun(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseEnd("succeeded"))
	}), "t", "")
	if _, err := c.Follow(context.Background(), "00000000-0000-4000-8000-000000000000", 0, func(Frame) {}); err == nil {
		t.Error("took the end of a different run as this one's")
	}
}

func TestOnRetryHearsEachReconnect(t *testing.T) {
	c := testRunsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), "t", "")
	var attempts []int
	c.OnRetry = func(attempt, max int, err error) { attempts = append(attempts, attempt) }
	_, _ = c.Follow(context.Background(), runID, 0, func(Frame) {})
	if fmt.Sprint(attempts) != "[1 2 3 4 5]" {
		t.Errorf("attempts = %v", attempts)
	}
}

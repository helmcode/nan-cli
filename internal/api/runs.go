package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// RunsBaseURL is where /v1/runs lives. Not api.nan.builders: that host sits
// behind an edge Worker that rewrites response bodies and may buffer a stream,
// and agent output is exactly the kind of text it rewrites.
const RunsBaseURL = "https://cloud-api.nan.builders"

// Run states, as the platform names them.
const (
	StateQueued    = "queued"
	StateStarting  = "starting"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateTimedOut  = "timed_out"
)

// Terminal reports whether a run in this state will never change again.
func Terminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled, StateTimedOut:
		return true
	}
	return false
}

type RunWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RunArtifact struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type RunResult struct {
	Status       string        `json:"status"`
	Summary      string        `json:"summary"`
	Branch       string        `json:"branch"`
	Commit       string        `json:"commit"`
	ChangedFiles int           `json:"changed_files"`
	Artifacts    []RunArtifact `json:"artifacts"`
}

type RunUsage struct {
	TokensIn  *int64  `json:"tokens_in"`
	TokensOut *int64  `json:"tokens_out"`
	Source    *string `json:"source"`
}

// Run mirrors the run object of the platform's /v1/runs API. Every string in
// here except the ids is text a member's agent may have produced or shaped,
// so nothing prints it without sanitising it first.
type Run struct {
	ID              string       `json:"id"`
	Object          string       `json:"object"`
	Workspace       RunWorkspace `json:"workspace"`
	Agent           string       `json:"agent"`
	Model           *string      `json:"model"`
	Trigger         string       `json:"trigger"`
	State           string       `json:"state"`
	QueuePosition   *int         `json:"queue_position"`
	PromptPreview   string       `json:"prompt_preview"`
	Prompt          string       `json:"prompt,omitempty"`
	Cwd             string       `json:"cwd"`
	GitIsolation    *bool        `json:"git_isolation"`
	TimeoutSeconds  int          `json:"timeout_seconds"`
	CancelRequested bool         `json:"cancel_requested"`
	ExitCode        *int         `json:"exit_code"`
	ErrorCode       *string      `json:"error_code"`
	Result          *RunResult   `json:"result"`
	Usage           *RunUsage    `json:"usage"`
	LastSeq         int64        `json:"last_seq"`
	CreatedAt       string       `json:"created_at"`
	StartedAt       *string      `json:"started_at"`
	FinishedAt      *string      `json:"finished_at"`
}

// CreateRunRequest is the body of POST /v1/runs. Pointers are the fields the
// platform defaults when they are left out, which is not the same as zero.
type CreateRunRequest struct {
	Workspace      string  `json:"workspace"`
	Agent          string  `json:"agent"`
	Prompt         string  `json:"prompt"`
	Cwd            *string `json:"cwd,omitempty"`
	GitIsolation   *bool   `json:"git_isolation,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	Model          *string `json:"model,omitempty"`
	Trigger        string  `json:"trigger,omitempty"`
}

type RunList struct {
	Object     string  `json:"object"`
	Data       []Run   `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

type ListRunsParams struct {
	Workspace string
	State     string
	Limit     int
	Cursor    string
}

// Event is one line of a run's event log. Data stays raw: its shape depends
// on Type, and the renderer is the one place that decides what to show.
type Event struct {
	V    int             `json:"v"`
	Seq  int64           `json:"seq"`
	TS   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type EventPage struct {
	Data      []Event `json:"data"`
	NextAfter int64   `json:"next_after"`
	Done      bool    `json:"done"`
}

// StateChange is the `state` frame of the event stream.
type StateChange struct {
	State     string  `json:"state"`
	ErrorCode *string `json:"error_code"`
}

// Workspace is the part of GET /api/workspaces this CLI reads.
type Workspace struct {
	ID     string `json:"workspace_uuid"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// APIError is a refusal from the platform, in the OpenAI envelope /v1/runs
// answers with. Code is what callers branch on; Message is for people.
type APIError struct {
	Status  int
	Type    string
	Code    string
	Param   string
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" && !strings.Contains(msg, e.Code) {
		return fmt.Sprintf("%s (%s)", msg, e.Code)
	}
	return msg
}

// ErrStreamLost is the event stream failing more times in a row than the
// client retries. The run itself is not affected: it goes on in the
// workspace, and following it again picks up where this left off.
var ErrStreamLost = errors.New("lost the event stream")

// RunsClient talks to /v1/runs. It is separate from Client because it can
// authenticate with the member's API key when there is no session, and
// because one of its requests - the event stream - must not have a timeout.
type RunsClient struct {
	baseURL string
	token   string
	apiKey  string
	http    *http.Client
	stream  *http.Client

	// The stream's retry policy. Fields so the tests can run it in
	// milliseconds instead of minutes.
	StreamRetries int
	IdleTimeout   time.Duration
	HealthyAfter  time.Duration
	Backoff       func(attempt int) time.Duration
}

// NewRunsClient builds a client for the session token or, when there is none,
// the member's API key. A session wins because it is what every other
// command uses; the key is the fallback for a machine that only ran Setup.
func NewRunsClient(token, apiKey string) *RunsClient {
	return &RunsClient{
		baseURL:       RunsBaseURL,
		token:         token,
		apiKey:        apiKey,
		http:          &http.Client{Timeout: requestTimeout, CheckRedirect: noRedirects},
		stream:        &http.Client{CheckRedirect: noRedirects},
		StreamRetries: 5,
		IdleTimeout:   45 * time.Second,
		HealthyAfter:  time.Minute,
		Backoff:       defaultBackoff,
	}
}

// WithBaseURL points the client somewhere else, for tests.
func (c *RunsClient) WithBaseURL(u string) *RunsClient {
	c.baseURL = strings.TrimRight(u, "/")
	return c
}

// noRedirects keeps the credential on the request it was meant for. /v1/runs
// never redirects, and Go's default policy would forward the cookie or the
// Authorization header to a same-host http:// target.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func defaultBackoff(attempt int) time.Duration {
	d := time.Second << attempt
	if d > 16*time.Second {
		d = 16 * time.Second
	}
	return d
}

func (c *RunsClient) hasCredentials() bool { return c.token != "" || c.apiKey != "" }

func (c *RunsClient) authorize(req *http.Request) {
	switch {
	case c.token != "":
		req.Header.Set("Cookie", "nan_session="+c.token)
	case c.apiKey != "":
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *RunsClient) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "nan-cli")
	c.authorize(req)
	return req, nil
}

// maxResponse caps what a JSON response may make this process hold. The
// largest legitimate one is a page of 1000 events of at most 64 KiB each.
const maxResponse = 80 << 20

// do sends a request and decodes a 2xx JSON answer into out. It returns the
// status so callers can tell a 201 from an idempotent 200.
func (c *RunsClient) do(req *http.Request, out any) (int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("could not read the answer from nan.builders: %w", err)
	}
	if err := c.statusError(resp.StatusCode, body); err != nil {
		return resp.StatusCode, err
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unexpected answer from nan.builders: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// statusError turns a non-2xx answer into an error. 401 with credentials is
// ErrSessionExpired; everything else is an APIError carrying the code from
// either envelope the platform uses: OpenAI's {"error":{...}} on /v1, and the
// bare {"error":"..."} on /api.
func (c *RunsClient) statusError(status int, body []byte) error {
	if status < 400 {
		return nil
	}
	if status == http.StatusUnauthorized && c.hasCredentials() {
		return ErrSessionExpired
	}
	apiErr := &APIError{Status: status}
	var openai struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    any     `json:"code"`
		} `json:"error"`
	}
	var bare struct {
		Error string `json:"error"`
	}
	switch {
	case json.Unmarshal(body, &openai) == nil && (openai.Error.Message != "" || openai.Error.Code != nil):
		apiErr.Message = openai.Error.Message
		apiErr.Type = openai.Error.Type
		if openai.Error.Param != nil {
			apiErr.Param = *openai.Error.Param
		}
		switch code := openai.Error.Code.(type) {
		case string:
			apiErr.Code = code
		case float64:
			apiErr.Code = strconv.Itoa(int(code))
		}
	case json.Unmarshal(body, &bare) == nil && bare.Error != "":
		apiErr.Message = bare.Error
	}
	return apiErr
}

// CreateRun queues a run. replayed is true when the platform answered an
// Idempotency-Key it had already seen with the run it created then.
func (c *RunsClient) CreateRun(ctx context.Context, in CreateRunRequest, idempotencyKey string) (run *Run, replayed bool, err error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/runs", in)
	if err != nil {
		return nil, false, err
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	run = &Run{}
	status, err := c.do(req, run)
	if err != nil {
		return nil, false, err
	}
	return run, status == http.StatusOK, nil
}

func (c *RunsClient) ListRuns(ctx context.Context, p ListRunsParams) (*RunList, error) {
	q := url.Values{}
	if p.Workspace != "" {
		q.Set("workspace", p.Workspace)
	}
	if p.State != "" {
		q.Set("state", p.State)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	path := "/v1/runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	list := &RunList{}
	if _, err := c.do(req, list); err != nil {
		return nil, err
	}
	return list, nil
}

func (c *RunsClient) GetRun(ctx context.Context, id string) (*Run, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	run := &Run{}
	if _, err := c.do(req, run); err != nil {
		return nil, err
	}
	return run, nil
}

// CancelRun asks for a run to stop. changed is false when the run had
// already finished and the platform left it as it was (200 instead of 202).
func (c *RunsClient) CancelRun(ctx context.Context, id string) (run *Run, changed bool, err error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/cancel", nil)
	if err != nil {
		return nil, false, err
	}
	run = &Run{}
	status, err := c.do(req, run)
	if err != nil {
		return nil, false, err
	}
	return run, status == http.StatusAccepted, nil
}

// EventsPage reads one page of a run's events after seq `after`.
func (c *RunsClient) EventsPage(ctx context.Context, id string, after int64, limit int) (*EventPage, error) {
	q := url.Values{}
	q.Set("after", strconv.FormatInt(after, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	page := &EventPage{}
	if _, err := c.do(req, page); err != nil {
		return nil, err
	}
	return page, nil
}

// ListWorkspaces is GET /api/workspaces, which `nan run` reads to pick the
// workspace when the member has exactly one.
func (c *RunsClient) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/workspaces", nil)
	if err != nil {
		return nil, err
	}
	var list []Workspace
	if _, err := c.do(req, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Frame is what the event stream delivers to a Follow caller: either an
// event of the run's log or a change of its state.
type Frame struct {
	Event *Event
	State *StateChange
}

// maxSSELine bounds one line of the stream. The platform caps an event at
// 64 KiB; a line this long is not one of ours and is not worth buffering.
const maxSSELine = 1 << 20

// maxSSEFrame bounds the data of one frame, which can span many lines.
const maxSSEFrame = 4 << 20

var errIdle = errors.New("the stream went quiet")

// fatalStreamError is a failure reconnecting cannot fix.
type fatalStreamError struct{ err error }

func (e *fatalStreamError) Error() string { return e.err.Error() }
func (e *fatalStreamError) Unwrap() error { return e.err }

// Follow streams a run's events after seq `after`, calling onFrame for each
// one in order, until the platform sends `end` - whose run object it returns.
//
// The stream is expected to break: the platform closes it after 30 minutes,
// and a tunnel or a proxy in between can drop it at any time. A break is a
// reconnect with Last-Event-ID, so nothing is lost or repeated; only
// StreamRetries failed connections in a row give up, as ErrStreamLost. A 401 or a 404 is never retried.
func (c *RunsClient) Follow(ctx context.Context, id string, after int64, onFrame func(Frame)) (*Run, error) {
	lastSeq := after
	failures := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end, progressed, err := c.followOnce(ctx, id, &lastSeq, onFrame)
		if end != nil {
			return end, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var fatal *fatalStreamError
		if errors.As(err, &fatal) {
			return nil, fatal.err
		}
		if progressed {
			failures = 0
		}
		if err != nil {
			lastErr = err
		}
		failures++
		if failures > c.StreamRetries {
			if lastErr == nil {
				lastErr = errors.New("the platform closed the stream")
			}
			return nil, fmt.Errorf("%w: %v", ErrStreamLost, lastErr)
		}
		backoff := c.Backoff
		if backoff == nil {
			backoff = defaultBackoff
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff(failures - 1)):
		}
	}
}

// followOnce runs one connection of the stream. progressed is whether the
// connection was a working one: it delivered a frame, or it stayed up and
// heartbeating for HealthyAfter. Only a connection that did neither counts
// towards giving up - otherwise something that answers 200 and hangs up
// straight away, over and over, would never be given up on, and a quiet run
// whose stream the platform recycles every 30 minutes would be.
func (c *RunsClient) followOnce(ctx context.Context, id string, lastSeq *int64, onFrame func(Frame)) (end *Run, progressed bool, err error) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	idle := c.IdleTimeout
	if idle <= 0 {
		idle = 45 * time.Second
	}
	healthyAfter := c.HealthyAfter
	if healthyAfter <= 0 {
		healthyAfter = time.Minute
	}

	path := "/v1/runs/" + url.PathEscape(id) + "/events?after=" + strconv.FormatInt(*lastSeq, 10)
	req, err := c.newRequest(connCtx, http.MethodGet, path, nil)
	if err != nil {
		return nil, false, &fatalStreamError{err}
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if *lastSeq > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(*lastSeq, 10))
	}

	// The idle limit covers the wait for the response headers too: a
	// connection that is accepted and never answered is as dead as one that
	// goes quiet halfway.
	var headerTimedOut atomic.Bool
	headerTimer := time.AfterFunc(idle, func() {
		headerTimedOut.Store(true)
		cancel()
	})
	started := time.Now()
	resp, err := c.stream.Do(req)
	headerTimer.Stop()
	if err != nil {
		if headerTimedOut.Load() && ctx.Err() == nil {
			return nil, false, errIdle
		}
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		statusErr := c.statusError(resp.StatusCode, body)
		if statusErr == nil {
			statusErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, false, statusErr
		}
		return nil, false, &fatalStreamError{statusErr}
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/event-stream" {
		return nil, false, fmt.Errorf("expected an event stream, got %q", resp.Header.Get("Content-Type"))
	}

	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), maxSSELine)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-connCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readErr <- err
		} else {
			readErr <- io.EOF
		}
	}()

	timer := time.NewTimer(idle)
	defer timer.Stop()

	var (
		eventName string
		eventID   string
		data      []string
		dataBytes int
		heard     bool
	)
	healthy := func() bool { return progressed || (heard && time.Since(started) >= healthyAfter) }
	for {
		select {
		case <-ctx.Done():
			return nil, healthy(), ctx.Err()
		case <-timer.C:
			return nil, healthy(), errIdle
		case err := <-readErr:
			if errors.Is(err, bufio.ErrTooLong) {
				return nil, healthy(), &fatalStreamError{fmt.Errorf("the platform sent a stream line over %d bytes", maxSSELine)}
			}
			if errors.Is(err, io.EOF) {
				return nil, healthy(), nil
			}
			return nil, healthy(), err
		case line := <-lines:
			heard = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)

			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				if len(data) == 0 && eventName == "" {
					continue
				}
				payload := strings.Join(data, "\n")
				name := eventName
				frameID := eventID
				eventName, eventID, data, dataBytes = "", "", nil, 0
				run, delivered := dispatchFrame(name, frameID, payload, lastSeq, onFrame)
				if run != nil {
					return run, true, nil
				}
				if delivered {
					progressed = true
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				eventName = value
			case "id":
				eventID = value
			case "data":
				dataBytes += len(value)
				if dataBytes > maxSSEFrame {
					return nil, healthy(), &fatalStreamError{fmt.Errorf("the platform sent a stream frame over %d bytes", maxSSEFrame)}
				}
				data = append(data, value)
			}
		}
	}
}

// dispatchFrame hands one complete frame to the caller, and returns the run
// when the frame is `end`. Events at or below lastSeq were already delivered
// on a previous connection and are dropped, so a replay never shows twice.
func dispatchFrame(name, id, payload string, lastSeq *int64, onFrame func(Frame)) (end *Run, delivered bool) {
	switch name {
	case "", "run_event":
		var ev Event
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return nil, false
		}
		if ev.Seq == 0 {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil {
				ev.Seq = n
			}
		}
		if ev.Seq <= *lastSeq {
			return nil, true
		}
		*lastSeq = ev.Seq
		onFrame(Frame{Event: &ev})
		return nil, true
	case "state":
		var st StateChange
		if json.Unmarshal([]byte(payload), &st) != nil {
			return nil, false
		}
		onFrame(Frame{State: &st})
		return nil, true
	case "end":
		var run Run
		if json.Unmarshal([]byte(payload), &run) != nil || run.ID == "" {
			return nil, false
		}
		return &run, true
	}
	return nil, false
}

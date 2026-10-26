package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/contexts"
)

// fakeCtx is an in-memory /v1/contexts with the shapes, codes and status
// codes of cloud-api's handlers (internal/handlers/contexts.go,
// contexts_crud.go and internal/contexts/errors.go): the OpenAI error
// envelope with the extra fields at the top level, 201 for a new blob or
// version, 200 for a blob the member already has, Retry-After on 429/409.

const (
	fakeCtxID     = "c7000000-0000-4000-8000-000000000001"
	fakeCtxStamp  = "2026-10-21T10:00:00Z"
	fakeSecretPat = "ghp_"
)

type fakeContext struct {
	ID, Name, Description string
	Version               int
	Files                 []api.ContextFile
}

type fakeUpload struct {
	sha      string
	size     int64
	partSize int
	next     int
	data     []byte
}

type ctxReq struct {
	Method, Path, Query, Body, Idem, Filename string
	Size                                      int64
}

type fakeResp struct {
	status int
	body   map[string]any
	header map[string]string
}

type fakeCtx struct {
	mu       sync.Mutex
	off      bool
	contexts []*fakeContext
	blobs    map[string][]byte
	uploads  map[string]*fakeUpload
	partSize int
	reqs     []ctxReq
	// queued answers "METHOD /path" before the normal handling.
	queued map[string][]fakeResp
	// onPublish runs before a publish is handled (f.mu held).
	onPublish func(f *fakeCtx, n int)
	publishes int
	// corrupt serves wrong bytes for these paths on download.
	corrupt map[string]bool
	// failPart answers 500 to the first PUT of these part numbers.
	failPart map[int]bool
	srv      *httptest.Server
}

// server starts the fake once; every run against it sees the same URL, so
// the remembered uploads (keyed by API host and account) carry over.
func (f *fakeCtx) server(t *testing.T) *httptest.Server {
	if f.srv == nil {
		f.srv = httptest.NewServer(f)
		t.Cleanup(f.srv.Close)
	}
	return f.srv
}

// stateFile is where a push run against f remembers an upload of sha.
func (f *fakeCtx) stateFile(t *testing.T, stateDir, sha string) string {
	h := sha256.Sum256([]byte(f.server(t).URL + "\x00" + "sess-cli" + "\x00"))
	return filepath.Join(stateDir, hex.EncodeToString(h[:])[:32], sha+".json")
}

// strict decodes a JSON body like cloud-api: unknown fields are a 400.
func strict(w http.ResponseWriter, body []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		ctxFail(w, 400, "invalid_request", "body has an unknown or malformed field", nil, 0)
		return false
	}
	return true
}

func newFakeCtx() *fakeCtx {
	return &fakeCtx{
		blobs:    map[string][]byte{},
		uploads:  map[string]*fakeUpload{},
		partSize: api.ContextPartSize,
		queued:   map[string][]fakeResp{},
		corrupt:  map[string]bool{},
		failPart: map[int]bool{},
	}
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// seed adds a context whose files hold the given contents.
func (f *fakeCtx) seed(name string, version int, files map[string]string) *fakeContext {
	c := &fakeContext{ID: fakeCtxID, Name: name, Description: "Acme API conventions", Version: version}
	if len(f.contexts) > 0 {
		c.ID = fmt.Sprintf("c7000000-0000-4000-8000-%012d", len(f.contexts)+1)
	}
	for p, body := range files {
		sha := shaOf([]byte(body))
		f.blobs[sha] = []byte(body)
		c.Files = append(c.Files, api.ContextFile{Path: p, SHA256: sha, Size: int64(len(body)), Executable: strings.HasSuffix(p, ".sh")})
	}
	sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Path < c.Files[j].Path })
	f.contexts = append(f.contexts, c)
	return c
}

func (f *fakeCtx) queue(route string, r fakeResp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued[route] = append(f.queued[route], r)
}

func (f *fakeCtx) requests() []ctxReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ctxReq(nil), f.reqs...)
}

func (f *fakeCtx) sent(method, prefix string) []ctxReq {
	var out []ctxReq
	for _, r := range f.requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func (c *fakeContext) view() map[string]any {
	var total int64
	agents := false
	skills := []string{}
	seen := map[string]bool{}
	for _, f := range c.Files {
		total += f.Size
		agents = agents || f.Path == "AGENTS.md"
		if parts := strings.SplitN(f.Path, "/", 3); len(parts) == 3 && parts[0] == "skills" && !seen[parts[1]] {
			seen[parts[1]] = true
			skills = append(skills, parts[1])
		}
	}
	sort.Strings(skills)
	return map[string]any{"id": c.ID, "object": "context", "name": c.Name, "description": c.Description, "version": c.Version,
		"tree_sha256": strings.Repeat("ab", 32), "file_count": len(c.Files), "total_bytes": total, "has_instructions": agents,
		"skills": skills, "created_at": fakeCtxStamp, "updated_at": fakeCtxStamp}
}

func ctxFail(w http.ResponseWriter, status int, code, msg string, extra map[string]any, retryAfter int) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	typ := "invalid_request_error"
	if status == 429 {
		typ = "rate_limit_exceeded"
	} else if status >= 500 {
		typ = "server_error"
	}
	body := map[string]any{"error": map[string]any{"message": msg, "type": typ, "param": nil, "code": code}}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ctxJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeCtx) find(ref string) *fakeContext {
	for _, c := range f.contexts {
		if c.ID == ref || c.Name == ref {
			return c
		}
	}
	return nil
}

func (f *fakeCtx) blobView(sha string) map[string]any {
	b := f.blobs[sha]
	findings := []any{}
	if bytes.Contains(b, []byte(fakeSecretPat)) {
		findings = append(findings, map[string]any{"line": 1, "pattern": "github_token"})
	}
	return map[string]any{"sha256": sha, "size": len(b), "format": "text", "secret_findings": findings}
}

func (f *fakeCtx) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	isRaw := r.Header.Get("Content-Type") == "application/octet-stream"
	if r.Method == http.MethodPut && r.ContentLength < 0 {
		ctxFail(w, 411, "invalid_request", "Content-Length is required", nil, 0)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := ctxReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Idem: r.Header.Get("Idempotency-Key"),
		Filename: r.Header.Get("X-Nan-Filename"), Size: int64(len(body))}
	if !isRaw {
		rec.Body = string(body)
	}
	f.reqs = append(f.reqs, rec)
	if f.off {
		ctxFail(w, 404, "not_found", "Not found.", nil, 0)
		return
	}
	route := r.Method + " " + r.URL.Path
	if q := f.queued[route]; len(q) > 0 {
		f.queued[route] = q[1:]
		for k, v := range q[0].header {
			w.Header().Set(k, v)
		}
		if q[0].body == nil {
			w.WriteHeader(q[0].status)
			return
		}
		ctxJSONResp(w, q[0].status, q[0].body)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/contexts"), "/")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		data := []any{}
		for _, c := range f.contexts {
			data = append(data, c.view())
		}
		ctxJSONResp(w, 200, map[string]any{"data": data, "usage": map[string]any{"stored_bytes": 1536, "quota_bytes": 500 << 20}})
	case rest == "" && r.Method == http.MethodPost:
		var in struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !strict(w, body, &in) {
			return
		}
		if f.find(in.Name) != nil {
			ctxFail(w, 409, "context_name_taken", "You already have a context with this name.", nil, 0)
			return
		}
		c := &fakeContext{ID: fmt.Sprintf("c7000000-0000-4000-8000-%012d", len(f.contexts)+1), Name: in.Name, Description: in.Description, Version: 1}
		f.contexts = append(f.contexts, c)
		ctxJSONResp(w, 201, c.view())
	case parts[0] == "blobs":
		f.serveBlobs(w, r, parts[1:], body)
	default:
		c := f.find(parts[0])
		if c == nil {
			ctxFail(w, 404, "context_not_found", "context not found", nil, 0)
			return
		}
		sub := strings.Join(parts[1:], "/")
		switch r.Method + " " + sub {
		case "GET ":
			v := c.view()
			files := []any{}
			for _, fl := range c.Files {
				files = append(files, map[string]any{"path": fl.Path, "size": fl.Size, "sha256": fl.SHA256, "executable": fl.Executable})
			}
			v["files"] = files
			ctxJSONResp(w, 200, v)
		case "PATCH ":
			var in struct {
				Name        *string `json:"name"`
				Description *string `json:"description"`
			}
			if !strict(w, body, &in) {
				return
			}
			if in.Name != nil {
				c.Name = *in.Name
			}
			if in.Description != nil {
				c.Description = *in.Description
			}
			ctxJSONResp(w, 200, c.view())
		case "DELETE ":
			for i, x := range f.contexts {
				if x == c {
					f.contexts = append(f.contexts[:i], f.contexts[i+1:]...)
					break
				}
			}
			w.WriteHeader(204)
		case "GET file":
			p := r.URL.Query().Get("path")
			if v := r.URL.Query().Get("version"); v != strconv.Itoa(c.Version) {
				ctxFail(w, 404, "version_not_found", "version not found", nil, 0)
				return
			}
			for _, fl := range c.Files {
				if fl.Path == p {
					b := f.blobs[fl.SHA256]
					if f.corrupt[p] {
						b = append([]byte("X"), b[1:]...)
					}
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(b)
					return
				}
			}
			ctxFail(w, 404, "file_not_found", "file not found", nil, 0)
		case "POST versions":
			f.publishes++
			if f.onPublish != nil {
				f.onPublish(f, f.publishes)
			}
			var in api.PublishRequest
			if !strict(w, body, &in) {
				return
			}
			// cloud-api's order (service.Publish): the tree (paths, then
			// blobs), then base_version.
			paths := make([]string, len(in.Files))
			for i, fl := range in.Files {
				paths[i] = fl.Path
			}
			if bad := contexts.ValidateTree(paths); len(bad) > 0 {
				refused := []any{}
				for _, b := range bad {
					refused = append(refused, map[string]any{"path": b.Path, "reason": string(b.Reason)})
				}
				ctxFail(w, 400, "invalid_path", "Some file names are not allowed.", map[string]any{"files": refused}, 0)
				return
			}
			var missing []string
			for _, fl := range in.Files {
				if _, ok := f.blobs[fl.SHA256]; !ok {
					missing = append(missing, fl.SHA256)
				}
			}
			if len(missing) > 0 {
				ctxFail(w, 409, "blob_missing", "Some files are no longer stored; upload them again and publish.", map[string]any{"sha256": missing}, 0)
				return
			}
			if in.BaseVersion != c.Version {
				ctxFail(w, 409, "version_conflict", "The context changed since you loaded it; reload and publish again.",
					map[string]any{"current_version": c.Version}, 0)
				return
			}
			if !in.AllowSecretLike {
				var hits []any
				for _, fl := range in.Files {
					if bytes.Contains(f.blobs[fl.SHA256], []byte(fakeSecretPat)) {
						hits = append(hits, map[string]any{"path": fl.Path, "line": 1, "pattern": "github_token"})
					}
				}
				if len(hits) > 0 {
					ctxFail(w, 422, "context_secret_detected", "Some files look like they contain secrets. Keep secrets in Secrets, or publish anyway.",
						map[string]any{"findings": hits}, 0)
					return
				}
			}
			c.Version++
			c.Files = nil
			for _, fl := range in.Files {
				c.Files = append(c.Files, api.ContextFile{Path: fl.Path, SHA256: fl.SHA256, Size: int64(len(f.blobs[fl.SHA256])), Executable: fl.Executable})
			}
			ctxJSONResp(w, 201, c.view())
		default:
			ctxFail(w, 404, "not_found", "no route "+route, nil, 0)
		}
	}
}

func (f *fakeCtx) serveBlobs(w http.ResponseWriter, r *http.Request, parts []string, body []byte) {
	if len(parts) == 1 && parts[0] == "check" {
		var in struct {
			SHA256 []string `json:"sha256"`
		}
		if !strict(w, body, &in) {
			return
		}
		missing := []string{}
		for _, s := range in.SHA256 {
			if _, ok := f.blobs[s]; !ok {
				missing = append(missing, s)
			}
		}
		ctxJSONResp(w, 200, map[string]any{"missing": missing})
		return
	}
	sha := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodPut:
		if int64(len(body)) > api.ContextPartSize {
			ctxFail(w, 413, "invalid_request", "a single upload is at most 16 MiB", nil, 0)
			return
		}
		if _, ok := f.blobs[sha]; ok {
			ctxJSONResp(w, 200, f.blobView(sha))
			return
		}
		if shaOf(body) != sha {
			ctxFail(w, 422, "blob_hash_mismatch", "The uploaded bytes do not match the declared sha256 and size; nothing was stored.", nil, 0)
			return
		}
		f.blobs[sha] = body
		ctxJSONResp(w, 201, f.blobView(sha))
	case len(parts) == 2 && r.Method == http.MethodPost:
		if _, ok := f.blobs[sha]; ok {
			ctxJSONResp(w, 200, f.blobView(sha))
			return
		}
		var in struct {
			Size     int64  `json:"size"`
			Filename string `json:"filename"`
		}
		if !strict(w, body, &in) {
			return
		}
		id := fmt.Sprintf("u0000000-0000-4000-8000-%012d", len(f.uploads)+1)
		u := &fakeUpload{sha: sha, size: in.Size, partSize: f.partSize, next: 1}
		f.uploads[id] = u
		ctxJSONResp(w, 201, f.uploadView(id, u))
	case len(parts) >= 3:
		id := parts[2]
		u := f.uploads[id]
		if u == nil || u.sha != sha {
			ctxFail(w, 404, "upload_not_found", "upload not found", nil, 0)
			return
		}
		sub := strings.Join(parts[3:], "/")
		switch {
		case r.Method == http.MethodGet && sub == "":
			ctxJSONResp(w, 200, f.uploadView(id, u))
		case r.Method == http.MethodPut && strings.HasPrefix(sub, "parts/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(sub, "parts/"))
			if f.failPart[n] {
				delete(f.failPart, n)
				ctxFail(w, 500, "internal_error", "Something went wrong. Try again.", nil, 0)
				return
			}
			if n < u.next {
				ctxJSONResp(w, 200, map[string]int{"next_part": u.next})
				return
			}
			if n != u.next {
				ctxFail(w, 409, "part_conflict", "out of order", nil, 0)
				return
			}
			u.data = append(u.data, body...)
			u.next++
			ctxJSONResp(w, 200, map[string]int{"next_part": u.next})
		case r.Method == http.MethodPost && sub == "complete":
			if int64(len(u.data)) != u.size || shaOf(u.data) != sha {
				ctxFail(w, 422, "blob_hash_mismatch", "mismatch", nil, 0)
				return
			}
			f.blobs[sha] = u.data
			delete(f.uploads, id)
			ctxJSONResp(w, 201, f.blobView(sha))
		case r.Method == http.MethodDelete && sub == "":
			delete(f.uploads, id)
			w.WriteHeader(204)
		default:
			ctxFail(w, 404, "not_found", "no route", nil, 0)
		}
	default:
		ctxFail(w, 404, "not_found", "no route", nil, 0)
	}
}

func (f *fakeCtx) uploadView(id string, u *fakeUpload) map[string]any {
	parts := int((u.size + int64(u.partSize) - 1) / int64(u.partSize))
	return map[string]any{"upload_id": id, "part_size": u.partSize, "parts": parts, "next_part": u.next}
}

// ctxRun is one nan invocation against a fakeCtx.
type ctxRun struct {
	code           int
	msg            string
	stdout, stderr string
	sleeps         []time.Duration
	stateDir       string
}

type ctxSetup struct {
	stdin    string
	tty      bool
	stateDir string // "" = a fresh temp dir
}

func runCtx(t *testing.T, fake *fakeCtx, setup ctxSetup, args ...string) ctxRun {
	t.Helper()
	srv := fake.server(t)
	fakeHome(t, cliSession)
	t.Setenv(tokenEnvVar, "")
	swap(t, &runsBaseURL, srv.URL)
	swap(t, &writeStdin, io.Reader(strings.NewReader(setup.stdin)))
	tty := setup.tty
	swap(t, &writeStdinTTY, func() bool { return tty })
	var out, errOut bytes.Buffer
	swap(t, &writeStdout, io.Writer(&out))
	swap(t, &writeStderr, io.Writer(&errOut))
	state := setup.stateDir
	if state == "" {
		state = t.TempDir()
	}
	swap(t, &ctxUploadStateDir, func() (string, error) { return state, nil })
	var mu sync.Mutex
	var sleeps []time.Duration
	swap(t, &ctxSleep, func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		sleeps = append(sleeps, d)
		return nil
	})
	code, msg := execute(t, args...)
	mu.Lock()
	defer mu.Unlock()
	return ctxRun{code: code, msg: msg, stdout: out.String(), stderr: errOut.String(), sleeps: sleeps, stateDir: state}
}

func mustCtxExit(t *testing.T, r ctxRun, code int, wants ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d (want %d): %s\nstdout: %s\nstderr: %s", r.code, code, r.msg, r.stdout, r.stderr)
	}
	wantAll(t, "message", r.msg, wants...)
}

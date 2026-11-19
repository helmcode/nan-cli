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
	"testing"
	"time"
)

func ctxServer(t *testing.T, h http.HandlerFunc) *RunsClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewRunsClient("sess", "").WithBaseURL(srv.URL)
}

// The contexts API puts its extra fields next to the error envelope (cloud-api
// ContextsHandler.fail); every one of them reaches the caller.
func TestContextErrorCarriesTheExtraFields(t *testing.T) {
	for _, tc := range []struct {
		body  string
		check func(*ContextError) bool
	}{
		{`{"error":{"message":"changed","type":"invalid_request_error","param":"base_version","code":"version_conflict"},"current_version":7}`,
			func(e *ContextError) bool {
				return e.CurrentVersion != nil && *e.CurrentVersion == 7 && e.Param == "base_version"
			}},
		{`{"error":{"message":"gone","type":"invalid_request_error","param":null,"code":"blob_missing"},"sha256":["aa","bb"]}`,
			func(e *ContextError) bool { return strings.Join(e.MissingBlobs, ",") == "aa,bb" }},
		{`{"error":{"message":"names","type":"invalid_request_error","param":null,"code":"invalid_path"},"files":[{"path":"files/.env","reason":"dotfile"}]}`,
			func(e *ContextError) bool { return len(e.Files) == 1 && e.Files[0].Reason == "dotfile" }},
		{`{"error":{"message":"secrets","type":"invalid_request_error","param":null,"code":"context_secret_detected"},"findings":[{"path":"AGENTS.md","line":3,"pattern":"github_token"}]}`,
			func(e *ContextError) bool { return len(e.Findings) == 1 && e.Findings[0].Line == 3 }},
		{`{"error":{"message":"quota","type":"invalid_request_error","param":null,"code":"context_quota_exceeded"},"used":10,"quota":5,"growing":true}`,
			func(e *ContextError) bool { return e.Used == 10 && e.Quota == 5 }},
		{`{"error":{"message":"in use","type":"invalid_request_error","param":null,"code":"context_in_use"},"automations":[{"id":"a1","name":"PR review"}]}`,
			func(e *ContextError) bool { return len(e.Automations) == 1 && e.Automations[0].Name == "PR review" }},
	} {
		c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(409)
			_, _ = io.WriteString(w, tc.body)
		})
		_, err := c.GetContext(context.Background(), "acme")
		ce, ok := AsContextError(err)
		if !ok || !tc.check(ce) || ce.RetryAfter != 5*time.Second {
			t.Errorf("%s: %+v", tc.body, ce)
			continue
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code == "" {
			t.Errorf("does not unwrap to the APIError: %v", err)
		}
	}
}

func TestContextSessionExpired(t *testing.T) {
	c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	if _, err := c.ListContexts(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("%v", err)
	}
}

func TestCheckBlobsBatchesAtTheCap(t *testing.T) {
	var sizes []int
	c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			SHA256 []string `json:"sha256"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sizes = append(sizes, len(in.SHA256))
		_ = json.NewEncoder(w).Encode(map[string]any{"missing": in.SHA256[:1]})
	})
	shas := make([]string, 2001)
	for i := range shas {
		shas[i] = fmt.Sprintf("%064x", i)
	}
	missing, err := c.CheckBlobs(context.Background(), shas)
	if err != nil || len(missing) != 3 || fmt.Sprint(sizes) != "[1000 1000 1]" {
		t.Errorf("%v %v %v", err, missing, sizes)
	}
}

func TestPutBlobStreamsWithItsLengthAndHint(t *testing.T) {
	c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || r.URL.Path != "/v1/contexts/blobs/abc" || r.ContentLength != 5 || string(b) != "hello" ||
			r.Header.Get("X-Nan-Filename") != "a.md" || r.Header.Get("Content-Type") != "application/octet-stream" ||
			r.Header.Get("Cookie") != "nan_session=sess" {
			t.Errorf("%s %s len=%d %q %v", r.Method, r.URL.Path, r.ContentLength, b, r.Header)
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"sha256":"abc","size":5,"format":"text","secret_findings":[{"line":1,"pattern":"github_token"}]}`)
	})
	b, err := c.PutBlob(context.Background(), "abc", strings.NewReader("hello"), 5, "a.md")
	if err != nil || b.Size != 5 || len(b.SecretFindings) != 1 {
		t.Errorf("%+v %v", b, err)
	}
}

// A blob the member already has answers 200 with it instead of an upload.
func TestStartUploadTellsAStoredBlobFromAnUpload(t *testing.T) {
	status := 200
	c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["size"] != float64(20<<20) || in["filename"] != "big.pdf" {
			t.Errorf("body %v", in)
		}
		w.WriteHeader(status)
		if status == 200 {
			_, _ = io.WriteString(w, `{"sha256":"abc","size":20971520,"format":"pdf","secret_findings":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"upload_id":"u1","part_size":16777216,"parts":2,"next_part":1}`)
	})
	up, blob, err := c.StartUpload(context.Background(), "abc", 20<<20, "big.pdf")
	if err != nil || up != nil || blob == nil || blob.Format != "pdf" {
		t.Errorf("%+v %+v %v", up, blob, err)
	}
	status = 201
	up, blob, err = c.StartUpload(context.Background(), "abc", 20<<20, "big.pdf")
	if err != nil || blob != nil || up == nil || up.UploadID != "u1" || up.Parts != 2 {
		t.Errorf("%+v %+v %v", up, blob, err)
	}
}

func TestOpenContextFileRefusalIsAContextError(t *testing.T) {
	c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "files/a b.md" || r.URL.Query().Get("version") != "3" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":{"message":"file not found","type":"invalid_request_error","param":"path","code":"file_not_found"}}`)
	})
	_, err := c.OpenContextFile(context.Background(), "id", "files/a b.md", 3)
	if ce, ok := AsContextError(err); !ok || ce.Code != "file_not_found" {
		t.Errorf("%v", err)
	}
}

func TestPublishSendsAnEmptyListNotNull(t *testing.T) {
	c := ctxServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"base_version":1,"files":[],"allow_secret_like":false}` || r.Header.Get("Idempotency-Key") != "k" {
			t.Errorf("%s %v", b, r.Header)
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"id","name":"acme","version":2}`)
	})
	out, err := c.PublishContext(context.Background(), "id", PublishRequest{BaseVersion: 1}, "k")
	if err != nil || out.Version != 2 {
		t.Errorf("%+v %v", out, err)
	}
}

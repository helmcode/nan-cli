package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Agent contexts (helmcode/nan-devops AGENT-CONTEXT.md §2): versioned file
// trees a member keeps for their runs and automations, under /v1/contexts.
// Reads take a CLI session, the member's sk- key or a platform token with
// contexts:read; writes take a CLI session or a token with contexts:write.
//
// Content goes up as content-addressed blobs (sha256): POST blobs/check says
// which ones the platform lacks, a blob of at most PartSize bytes is one PUT,
// a larger one a multipart upload of PartSize parts that can be resumed. A
// version is then published with the full file list and the version it was
// based on. This client only speaks the protocol; the push and pull logic
// lives with the command.

// ContextPartSize is the platform's single-upload limit and multipart part
// size (16 MiB).
const ContextPartSize = 16 << 20

// ContextMaxFileBytes is the largest file a context holds (100 MiB).
const ContextMaxFileBytes = 100 << 20

// maxCheckShas is the platform's cap on one blobs/check body.
const maxCheckShas = 1000

// Context is a context's metadata at its current version (or, for a
// publish replay, at the version that publish created).
type Context struct {
	ID              string   `json:"id"`
	Object          string   `json:"object"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Version         int      `json:"version"`
	TreeSHA256      string   `json:"tree_sha256"`
	FileCount       int      `json:"file_count"`
	TotalBytes      int64    `json:"total_bytes"`
	HasInstructions bool     `json:"has_instructions"`
	Skills          []string `json:"skills"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

// ContextFile is one file of a version: what a pull writes.
type ContextFile struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

// ContextDetail is GET /v1/contexts/{id_or_name}: the context and the files
// of its current version.
type ContextDetail struct {
	Context
	Files []ContextFile `json:"files"`
}

type ContextUsage struct {
	StoredBytes int64 `json:"stored_bytes"`
	QuotaBytes  int64 `json:"quota_bytes"`
}

type ContextList struct {
	Data  []Context    `json:"data"`
	Usage ContextUsage `json:"usage"`
}

// SecretFinding is where a text blob looks like it holds a credential. The
// platform never returns the match itself.
type SecretFinding struct {
	Line    int    `json:"line"`
	Pattern string `json:"pattern"`
}

// ContextBlob is a stored blob.
type ContextBlob struct {
	SHA256         string          `json:"sha256"`
	Size           int64           `json:"size"`
	Format         string          `json:"format"`
	SecretFindings []SecretFinding `json:"secret_findings"`
}

// ContextUpload is a multipart upload in progress.
type ContextUpload struct {
	UploadID string `json:"upload_id"`
	PartSize int    `json:"part_size"`
	Parts    int    `json:"parts"`
	NextPart int    `json:"next_part"`
}

// PublishFile is one entry of a publish.
type PublishFile struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable"`
}

// PublishRequest is the body of POST /v1/contexts/{id}/versions.
type PublishRequest struct {
	BaseVersion     int           `json:"base_version"`
	Files           []PublishFile `json:"files"`
	AllowSecretLike bool          `json:"allow_secret_like"`
}

// ContextPatch is the body of PATCH /v1/contexts/{id}: nil fields stay.
type ContextPatch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// FileRefusal is one refused file of a publish: invalid_path,
// unsupported_file_type, skill_invalid (reason per file, detail optional).
type FileRefusal struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// SecretHit is one finding of 422 context_secret_detected.
type SecretHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Pattern string `json:"pattern"`
}

// ContextError is a refusal of the contexts API with the fields the
// platform adds next to the error envelope. It unwraps to the *APIError, so
// the generic exit mapping still sees the code.
type ContextError struct {
	*APIError
	// RetryAfter is the Retry-After header (0 when absent).
	RetryAfter time.Duration
	// CurrentVersion is set on 409 version_conflict.
	CurrentVersion *int
	// MissingBlobs is 409 blob_missing's sha256 list.
	MissingBlobs []string
	// Files is the per-file refusal list of invalid_path,
	// unsupported_file_type and skill_invalid.
	Files []FileRefusal
	// Findings is 422 context_secret_detected's list.
	Findings []SecretHit
	// Used and Quota are 413 context_quota_exceeded's figures.
	Used, Quota int64
}

func (e *ContextError) Error() string { return e.APIError.Error() }
func (e *ContextError) Unwrap() error { return e.APIError }

// AsContextError finds a *ContextError in err.
func AsContextError(err error) (*ContextError, bool) {
	var ce *ContextError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

func (c *RunsClient) contextError(status int, header http.Header, body []byte) error {
	err := c.statusError(status, body)
	apiErr, ok := err.(*APIError)
	if !ok {
		return err
	}
	ce := &ContextError{APIError: apiErr}
	if s := header.Get("Retry-After"); s != "" {
		if n, perr := strconv.Atoi(s); perr == nil && n >= 0 {
			ce.RetryAfter = time.Duration(n) * time.Second
		}
	}
	var extra struct {
		CurrentVersion *int          `json:"current_version"`
		SHA256         []string      `json:"sha256"`
		Files          []FileRefusal `json:"files"`
		Findings       []SecretHit   `json:"findings"`
		Used           int64         `json:"used"`
		Quota          int64         `json:"quota"`
		Automations    []Ref         `json:"automations"`
	}
	if json.Unmarshal(body, &extra) == nil {
		ce.CurrentVersion = extra.CurrentVersion
		ce.MissingBlobs = extra.SHA256
		ce.Files = extra.Files
		ce.Findings = extra.Findings
		ce.Used, ce.Quota = extra.Used, extra.Quota
		if len(apiErr.Automations) == 0 {
			apiErr.Automations = extra.Automations
		}
	}
	return ce
}

// ctxDo sends req with client and decodes a 2xx JSON answer into out.
func (c *RunsClient) ctxDo(client *http.Client, req *http.Request, out any) (int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("could not read the answer from nan.builders: %w", err)
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, c.contextError(resp.StatusCode, resp.Header, body)
	}
	if out != nil && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unexpected answer from nan.builders: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (c *RunsClient) ctxJSON(ctx context.Context, method, path string, body, out any, idemKey string) (int, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return 0, err
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	return c.ctxDo(c.http, req, out)
}

// rawRequest is a request whose body is streamed bytes of a known length.
func (c *RunsClient) rawRequest(ctx context.Context, method, path string, body io.Reader, size int64) (*http.Request, error) {
	if body == nil || size == 0 {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "nan-cli")
	c.authorize(req)
	return req, nil
}

// BaseURL is where this client sends its requests.
func (c *RunsClient) BaseURL() string { return c.baseURL }

func contextPath(id string) string { return "/v1/contexts/" + url.PathEscape(id) }

func blobPath(sha string) string { return "/v1/contexts/blobs/" + url.PathEscape(sha) }

func uploadPath(sha, uploadID string) string {
	return blobPath(sha) + "/uploads/" + url.PathEscape(uploadID)
}

// ListContexts is GET /v1/contexts.
func (c *RunsClient) ListContexts(ctx context.Context) (*ContextList, error) {
	out := &ContextList{}
	_, err := c.ctxJSON(ctx, http.MethodGet, "/v1/contexts", nil, out, "")
	return out, err
}

// GetContext is GET /v1/contexts/{id_or_name}.
func (c *RunsClient) GetContext(ctx context.Context, idOrName string) (*ContextDetail, error) {
	out := &ContextDetail{}
	_, err := c.ctxJSON(ctx, http.MethodGet, contextPath(idOrName), nil, out, "")
	return out, err
}

// CreateContext is POST /v1/contexts: the context with an empty version 1.
func (c *RunsClient) CreateContext(ctx context.Context, name, description, idemKey string) (*Context, error) {
	body := map[string]string{"name": name}
	if description != "" {
		body["description"] = description
	}
	out := &Context{}
	_, err := c.ctxJSON(ctx, http.MethodPost, "/v1/contexts", body, out, idemKey)
	return out, err
}

// UpdateContext is PATCH /v1/contexts/{id} (name and description only; no
// new version).
func (c *RunsClient) UpdateContext(ctx context.Context, id string, p ContextPatch) (*Context, error) {
	out := &Context{}
	_, err := c.ctxJSON(ctx, http.MethodPatch, contextPath(id), p, out, "")
	return out, err
}

// DeleteContext is DELETE /v1/contexts/{id}; 409 context_in_use names the
// automations that use it.
func (c *RunsClient) DeleteContext(ctx context.Context, id string) error {
	_, err := c.ctxJSON(ctx, http.MethodDelete, contextPath(id), nil, nil, "")
	return err
}

// CheckBlobs is POST /v1/contexts/blobs/check: which of shas the platform
// does not hold for this member (asked in batches of the platform's cap).
func (c *RunsClient) CheckBlobs(ctx context.Context, shas []string) ([]string, error) {
	missing := []string{}
	for start := 0; start < len(shas); start += maxCheckShas {
		end := min(start+maxCheckShas, len(shas))
		var out struct {
			Missing []string `json:"missing"`
		}
		if _, err := c.ctxJSON(ctx, http.MethodPost, "/v1/contexts/blobs/check", map[string][]string{"sha256": shas[start:end]}, &out, ""); err != nil {
			return nil, err
		}
		missing = append(missing, out.Missing...)
	}
	return missing, nil
}

// PutBlob is PUT /v1/contexts/blobs/{sha256}: one upload of at most
// ContextPartSize bytes. filename is a type hint for the platform only
// (X-Nan-Filename); it is never stored. The request has no client timeout:
// ctx bounds it.
func (c *RunsClient) PutBlob(ctx context.Context, sha string, body io.Reader, size int64, filename string) (*ContextBlob, error) {
	req, err := c.rawRequest(ctx, http.MethodPut, blobPath(sha), body, size)
	if err != nil {
		return nil, err
	}
	if filename != "" {
		req.Header.Set("X-Nan-Filename", filename)
	}
	out := &ContextBlob{}
	_, err = c.ctxDo(c.stream, req, out)
	return out, err
}

// StartUpload is POST /v1/contexts/blobs/{sha256}/uploads for a blob larger
// than ContextPartSize. When the platform already holds the blob it answers
// it instead of an upload (blob non-nil, upload nil).
func (c *RunsClient) StartUpload(ctx context.Context, sha string, size int64, filename string) (*ContextUpload, *ContextBlob, error) {
	req, err := c.newRequest(ctx, http.MethodPost, blobPath(sha)+"/uploads", map[string]any{"size": size, "filename": filename})
	if err != nil {
		return nil, nil, err
	}
	var raw json.RawMessage
	status, err := c.ctxDo(c.http, req, &raw)
	if err != nil {
		return nil, nil, err
	}
	if status == http.StatusOK {
		b := &ContextBlob{}
		return nil, b, json.Unmarshal(raw, b)
	}
	u := &ContextUpload{}
	return u, nil, json.Unmarshal(raw, u)
}

// UploadStatus is GET …/uploads/{upload_id}: where a multipart upload
// stands, to resume it.
func (c *RunsClient) UploadStatus(ctx context.Context, sha, uploadID string) (*ContextUpload, error) {
	out := &ContextUpload{}
	_, err := c.ctxJSON(ctx, http.MethodGet, uploadPath(sha, uploadID), nil, out, "")
	return out, err
}

// PutPart is PUT …/uploads/{upload_id}/parts/{n}; it answers the next part
// the platform expects.
func (c *RunsClient) PutPart(ctx context.Context, sha, uploadID string, n int, body io.Reader, size int64) (int, error) {
	req, err := c.rawRequest(ctx, http.MethodPut, uploadPath(sha, uploadID)+"/parts/"+strconv.Itoa(n), body, size)
	if err != nil {
		return 0, err
	}
	var out struct {
		NextPart int `json:"next_part"`
	}
	_, err = c.ctxDo(c.stream, req, &out)
	return out.NextPart, err
}

// CompleteUpload is POST …/uploads/{upload_id}/complete.
func (c *RunsClient) CompleteUpload(ctx context.Context, sha, uploadID string) (*ContextBlob, error) {
	req, err := c.newRequest(ctx, http.MethodPost, uploadPath(sha, uploadID)+"/complete", nil)
	if err != nil {
		return nil, err
	}
	out := &ContextBlob{}
	_, err = c.ctxDo(c.stream, req, out)
	return out, err
}

// AbortUpload is DELETE …/uploads/{upload_id}.
func (c *RunsClient) AbortUpload(ctx context.Context, sha, uploadID string) error {
	_, err := c.ctxJSON(ctx, http.MethodDelete, uploadPath(sha, uploadID), nil, nil, "")
	return err
}

// PublishContext is POST /v1/contexts/{id}/versions. The answer is the
// context at the new version. It can take a while (the platform rescans and
// reads every SKILL.md), so ctx bounds it instead of the client timeout.
func (c *RunsClient) PublishContext(ctx context.Context, id string, in PublishRequest, idemKey string) (*Context, error) {
	if in.Files == nil {
		in.Files = []PublishFile{}
	}
	req, err := c.newRequest(ctx, http.MethodPost, contextPath(id)+"/versions", in)
	if err != nil {
		return nil, err
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	out := &Context{}
	_, err = c.ctxDo(c.stream, req, out)
	return out, err
}

// OpenContextFile is GET /v1/contexts/{id}/file?path=…&version=N: the
// file's bytes as a stream the caller closes. The caller verifies them
// against the manifest; nothing here trusts the length or the content.
func (c *RunsClient) OpenContextFile(ctx context.Context, id, path string, version int) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("path", path)
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	req, err := c.newRequest(ctx, http.MethodGet, contextPath(id)+"/file?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach nan.builders: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, c.contextError(resp.StatusCode, resp.Header, body)
	}
	return resp.Body, nil
}

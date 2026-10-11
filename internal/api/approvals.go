package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
)

// Ref names another object: a workspace or an automation. The platform may
// send it as an object ({"id","name"}) or as a bare id; both decode here, so
// a change of shape on that side does not break this one.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func (r *Ref) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.ID)
	}
	type plain Ref
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*r = Ref(p)
	return nil
}

// Label is the name when there is one, else the id.
func (r *Ref) Label() string {
	if r == nil {
		return ""
	}
	if r.Name != "" {
		return r.Name
	}
	return r.ID
}

// Approval states, as the platform names them.
const (
	ApprovalAuto      = "auto"
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalRejected  = "rejected"
	ApprovalExpired   = "expired"
	ApprovalCancelled = "cancelled"
)

// ApprovalStates is every approval state, for flag validation.
var ApprovalStates = []string{ApprovalPending, ApprovalApproved, ApprovalRejected, ApprovalExpired, ApprovalCancelled, ApprovalAuto}

// ProposalAction is one action an automation run proposed. Every string in
// it except Kind was written by the agent (and masked by the platform), so
// it is printed as plain text through runs.Sanitize, never interpreted.
type ProposalAction struct {
	Kind          string   `json:"kind"`
	Repo          string   `json:"repo,omitempty"`
	Number        int64    `json:"number,omitempty"`
	Base          string   `json:"base,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	Head          string   `json:"head,omitempty"`
	Commit        string   `json:"commit,omitempty"`
	CommitMessage string   `json:"commit_message,omitempty"`
	Force         bool     `json:"force,omitempty"`
	Title         string   `json:"title,omitempty"`
	Body          string   `json:"body,omitempty"`
	Draft         bool     `json:"draft,omitempty"`
	Event         string   `json:"event,omitempty"`
	Labels        []string `json:"labels,omitempty"`
}

type DiffstatFile struct {
	Path    string `json:"path"`
	Added   int64  `json:"added"`
	Deleted int64  `json:"deleted"`
	Binary  bool   `json:"binary"`
}

type Diffstat struct {
	Files        []DiffstatFile `json:"files"`
	FilesChanged int64          `json:"files_changed"`
	Insertions   int64          `json:"insertions"`
	Deletions    int64          `json:"deletions"`
	Truncated    bool           `json:"truncated"`
}

// Proposal is the stored, masked proposal of a propose run.
type Proposal struct {
	V            int              `json:"v"`
	Actions      []ProposalAction `json:"actions"`
	Base         *string          `json:"base"`
	MergeBase    *string          `json:"merge_base"`
	BundleSHA256 *string          `json:"bundle_sha256"`
	DiffSHA256   *string          `json:"diff_sha256"`
	Diffstat     *Diffstat        `json:"diffstat"`
}

// Approval is a run's proposal and the member's decision on it (the approval
// object of the platform's /v1/approvals and of a run's "approval" field).
type Approval struct {
	RunID            string    `json:"run_id"`
	State            string    `json:"state"`
	ApprovalFrom     *int      `json:"approval_from"`
	ExpiresAt        *string   `json:"expires_at"`
	Proposal         *Proposal `json:"proposal"`
	ProposalSHA256   string    `json:"proposal_sha256"`
	Diffstat         *Diffstat `json:"diffstat"`
	HasDiff          bool      `json:"has_diff"`
	DiffMasked       bool      `json:"diff_masked"`
	DecidedAt        *string   `json:"decided_at"`
	RejectReason     *string   `json:"reject_reason,omitempty"`
	PrefixApplyRunID *string   `json:"prefix_apply_run_id"`
	ApplyRunID       *string   `json:"apply_run_id"`
	CreatedAt        string    `json:"created_at,omitempty"`

	// On the items of GET /v1/approvals only.
	RunState   string `json:"run_state,omitempty"`
	Workspace  *Ref   `json:"workspace,omitempty"`
	Automation *Ref   `json:"automation,omitempty"`
}

type ApprovalList struct {
	Object     string     `json:"object"`
	Data       []Approval `json:"data"`
	NextCursor *string    `json:"next_cursor"`
}

type ListApprovalsParams struct {
	State  string // "" = every state
	Limit  int
	Cursor string
}

// ListApprovals is GET /v1/approvals.
func (c *RunsClient) ListApprovals(ctx context.Context, p ListApprovalsParams) (*ApprovalList, error) {
	q := url.Values{}
	if p.State != "" {
		q.Set("state", p.State)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	path := "/v1/approvals"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	list := &ApprovalList{}
	if _, err := c.do(req, list); err != nil {
		return nil, err
	}
	return list, nil
}

type rejectBody struct {
	Reason string `json:"reason,omitempty"`
}

// RejectRun turns down the proposal of a run awaiting approval: the run ends
// cancelled (approval_rejected) and nothing it proposed is applied.
// Rejecting is always safe, so the platform accepts it from any credential
// that can read runs; approving is not, and has no counterpart here.
func (c *RunsClient) RejectRun(ctx context.Context, id, reason string) (*Run, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/reject", rejectBody{Reason: reason})
	if err != nil {
		return nil, err
	}
	run := &Run{}
	if _, err := c.do(req, run); err != nil {
		return nil, err
	}
	return run, nil
}

// maxDiff bounds the proposal diff this process holds. The platform stores
// at most 256 KiB; masking can only shorten what is longer than its marker.
const maxDiff = 1 << 20

// ProposalDiff is GET /v1/runs/{id}/proposal.diff: the stored, masked diff
// of a proposal whose approval covers a push. It is text written by the
// agent; the caller sanitises it before it reaches a terminal.
func (c *RunsClient) ProposalDiff(ctx context.Context, id string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/proposal.diff", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach nan.builders: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiff+1))
	if err != nil {
		return "", fmt.Errorf("could not read the answer from nan.builders: %w", err)
	}
	if err := c.statusError(resp.StatusCode, body); err != nil {
		return "", err
	}
	if len(body) > maxDiff {
		return "", fmt.Errorf("the diff is over %d bytes, more than the platform stores", maxDiff)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "" && mt != "text/plain" {
		return "", fmt.Errorf("expected a text diff, got %q", resp.Header.Get("Content-Type"))
	}
	return string(body), nil
}

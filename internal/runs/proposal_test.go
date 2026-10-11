package runs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

const (
	testRun = "6f1c2a9b-0000-4000-8000-000000000001"
	testWS  = "0e000000-0000-4000-8000-0000000000aa"
)

func awaitingRun(t *testing.T) *api.Run {
	t.Helper()
	body := `{"id":"` + testRun + `","state":"awaiting_approval","phase":"propose","workspace":{"id":"` + testWS + `","name":"develop"},
	"automation":{"id":"a1","name":"PR review \u001b[31mred"},
	"approval":{"run_id":"` + testRun + `","state":"pending","approval_from":1,"expires_at":"2026-10-14T12:00:00Z",
	"proposal":{"v":1,"actions":[
	  {"kind":"git_push","base":"main","branch":"fix/login","commit":"0123456789abcdef0123456789abcdef01234567","commit_message":"fix: login\u001b]52;c;ZXZpbA==\u0007","force":true},
	  {"kind":"github_create_pr","repo":"acme/api","base":"main","head":"fix/login","title":"Fix \u001b[2Jlogin","body":"line one\n<img src=x onerror=alert(1)>\n\u001b[1Aoverwrite","draft":true},
	  {"kind":"github_add_labels","repo":"acme/api","number":7,"labels":["bug","needs\u202erepro"]}],
	 "base":"main","merge_base":"fedcba9876543210","diffstat":{"files":[{"path":"src/\u001b[31mlogin.go","added":10,"deleted":2,"binary":false},{"path":"logo.png","added":0,"deleted":0,"binary":true}],"files_changed":2,"insertions":10,"deletions":2,"truncated":true}},
	"proposal_sha256":"abc123","has_diff":true,"diff_masked":true}}`
	var run api.Run
	if err := json.Unmarshal([]byte(body), &run); err != nil {
		t.Fatal(err)
	}
	return &run
}

func TestProposalIsPlainTextAndSaysWhereToDecide(t *testing.T) {
	var b bytes.Buffer
	Proposal(&b, awaitingRun(t))
	out := b.String()
	if strings.ContainsAny(out, "\x1b\x07\u202e\u009b") {
		t.Fatalf("control characters reached the output: %q", out)
	}
	for _, want := range []string{
		"waiting for your approval",
		"abc123",
		"main (merge base fedcba987654…)",
		"2 files, +10 -2",
		"1. git_push  fix/login onto main (commit 0123456789ab…) FORCE  [automatic]",
		`2. github_create_pr  acme/api  fix/login -> main  "Fix login" (draft)  [needs approval]`,
		"3. github_add_labels  acme/api#7  bug, needsrepro  [needs approval]",
		"<img src=x onerror=alert(1)>", // text, not markup: printed as it is
		"overwrite",
		"binary",
		"src/login.go",
		"more files than the summary lists",
		"nan runs proposal " + testRun + " --diff",
		"[masked]",
		"https://cloud.nan.builders/workspaces/" + testWS + "/runs/" + testRun,
		"nan runs reject " + testRun,
		"browser sign-in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("proposal lacks %q:\n%s", want, out)
		}
	}
}

func TestProposalOfADecidedRunHasNoApproveHint(t *testing.T) {
	run := awaitingRun(t)
	run.State = api.StateSucceeded
	run.Approval.State = api.ApprovalAuto
	run.Approval.ApprovalFrom = nil
	run.Approval.HasDiff, run.Approval.DiffMasked = false, false
	var b bytes.Buffer
	Proposal(&b, run)
	out := b.String()
	if strings.Contains(out, "approve:") || strings.Contains(out, "--diff") || strings.Contains(out, "needs approval") {
		t.Errorf("decided proposal still offers a decision:\n%s", out)
	}
	if !strings.Contains(out, "applied automatically") {
		t.Errorf("auto proposal not described:\n%s", out)
	}
}

func TestAwaitingApprovalExitAndOutcome(t *testing.T) {
	run := awaitingRun(t)
	code, ok := ExitCode(run)
	if !ok || code != 10 {
		t.Fatalf("ExitCode = %d %v", code, ok)
	}
	out := Outcome(run)
	for _, want := range []string{"awaiting approval", "nan runs proposal " + testRun, "nan runs reject " + testRun, "/runs/" + testRun} {
		if !strings.Contains(out, want) {
			t.Errorf("outcome lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nan runs approve") {
		t.Error("the hint names an approve command the CLI does not have")
	}
}

func TestRunURLFallsBackWithoutAWorkspaceID(t *testing.T) {
	run := awaitingRun(t)
	run.Workspace.ID = "../../evil"
	if got := RunURL(run); got != AutomationsURL {
		t.Errorf("RunURL = %q", got)
	}
}

func TestPhase2Events(t *testing.T) {
	var b bytes.Buffer
	r := &Renderer{Out: &b}
	r.Event(api.Event{Type: "approval_required", Data: json.RawMessage(`{"proposal_sha256":"0123456789abcdef0123","kinds":["git_push","github_create_pr\u001b[2J"]}`)})
	r.Event(api.Event{Type: "action", Data: json.RawMessage(`{"index":0,"kind":"git_push","status":"failed","message":"the base branch moved\u001b]0;x\u0007"}`)})
	out := b.String()
	for _, want := range []string{"approval required for: git_push, github_create_pr (proposal 0123456789ab…)", "action 1 git_push failed: the base branch moved"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("control characters: %q", out)
	}
}

func TestDiffIsSanitised(t *testing.T) {
	var b bytes.Buffer
	Diff(&b, "+ok\x1b[2J\r\n-gone")
	if b.String() != "+ok\n-gone\n" {
		t.Errorf("%q", b.String())
	}
}

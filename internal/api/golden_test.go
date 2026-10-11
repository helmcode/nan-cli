package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The golden responses below are copied verbatim from cloud-api's handler
// tests (internal/handlers/testdata/phase2 on helmcode/nan-cloud-api), so
// this client decodes exactly what the platform serialises.
func golden(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "phase2", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

const (
	goldenRun        = "6f1c2a9b-0000-4000-8000-000000000001"
	goldenAutomation = "a0000000-0000-4000-8000-000000000001"
	goldenWorkspace  = "11111111-2222-4333-8444-555555555555"
	goldenApplyRun   = "d0000000-0000-4000-8000-000000000004"
)

func checkGoldenApproval(t *testing.T, a *Approval) {
	t.Helper()
	if a == nil || a.RunID != goldenRun || a.State != ApprovalPending || a.ApprovalFrom == nil || *a.ApprovalFrom != 1 ||
		!a.HasDiff || a.DiffMasked || a.ProposalSHA256 == "" || a.ExpiresAt == nil ||
		a.PrefixApplyRunID == nil || *a.PrefixApplyRunID != goldenApplyRun || a.ApplyRunID != nil {
		t.Fatalf("approval = %+v", a)
	}
	p := a.Proposal
	if p == nil || p.V != 1 || len(p.Actions) != 3 || p.Actions[0].Kind != "git_push" || p.Actions[0].CommitMessage != "fix login" ||
		p.Actions[1].Number != 42 || p.Actions[1].Body != "<img onerror=x> looks fine" || p.Actions[2].Event != "COMMENT" ||
		*p.Base != "main" || p.Diffstat.Files[0].Path != "a.go" || a.Diffstat.Insertions != 3 {
		t.Fatalf("proposal = %+v", p)
	}
}

func TestGoldenRunPropose(t *testing.T) {
	var run Run
	golden(t, "run_get_propose.json", &run)
	if run.ID != goldenRun || run.State != StateAwaitingApproval || run.Phase != "propose" || run.Trigger != "webhook" ||
		run.Inference == nil || run.Inference.Source != "nan" || run.Automation == nil || run.Automation.ID != goldenAutomation ||
		run.Workspace.ID != goldenWorkspace || run.Workspace.Name != "develop" ||
		len(run.ApplyRunIDs) != 1 || run.ApplyRunIDs[0] != goldenApplyRun || *run.GitHubRepo != "acme/api" ||
		len(run.ApprovalKinds) != 1 || len(run.Secrets) != 1 || run.Secrets[0].Exposure != "agent" ||
		len(run.Result.ActionsApplied) != 1 || !run.Result.ActionsApplied[0].OK {
		t.Fatalf("run = %+v", run)
	}
	checkGoldenApproval(t, run.Approval)
}

func TestGoldenRunAdhoc(t *testing.T) {
	var run Run
	golden(t, "run_get_adhoc.json", &run)
	if run.Phase != "run" || run.Approval != nil || run.Automation != nil || len(run.ApplyRunIDs) != 0 ||
		run.State != StateSucceeded || run.Result.Summary != "done" || run.Prompt == "" {
		t.Fatalf("run = %+v", run)
	}
}

func TestGoldenRunList(t *testing.T) {
	var list RunList
	golden(t, "run_list_propose.json", &list)
	if len(list.Data) != 1 || list.Data[0].Approval == nil || list.Data[0].Automation.ID != goldenAutomation {
		t.Fatalf("list = %+v", list)
	}
}

func TestGoldenApprovals(t *testing.T) {
	var list ApprovalList
	golden(t, "approvals_list.json", &list)
	if list.Object != "list" || list.NextCursor == nil || *list.NextCursor != "cursor-2" || len(list.Data) != 1 {
		t.Fatalf("list = %+v", list)
	}
	a := list.Data[0]
	checkGoldenApproval(t, &a)
	if a.RunState != StateAwaitingApproval || a.Workspace.ID != goldenWorkspace || a.Automation.ID != goldenAutomation {
		t.Fatalf("item = %+v", a)
	}
}

func TestGoldenAutomations(t *testing.T) {
	var list AutomationList
	golden(t, "automations_list.json", &list)
	var one Automation
	golden(t, "automation_get.json", &one)
	for _, a := range []Automation{list.Data[0], one} {
		if a.ID != goldenAutomation || a.Name != "PR review api" || a.Workspace.ID != goldenWorkspace || a.Agent != "pi" ||
			*a.Model != "glm5.3" || a.Inference.Source != "nan" || *a.GitHubRepo != "acme/api" || !a.Enabled ||
			len(a.Secrets) != 1 || a.Secrets[0].Secret != "GITHUB_WRITE_TOKEN" || a.Secrets[0].Exposure != "actions" ||
			a.ApprovalTTLSeconds != 259200 || a.ConcurrencyPolicy != "skip_if_active" || a.Instructions == "" {
			t.Fatalf("automation = %+v", a)
		}
	}
}

func TestGoldenTriggersAndDeliveries(t *testing.T) {
	var triggers TriggerList
	golden(t, "automation_triggers.json", &triggers)
	tr := triggers.Data[0]
	if tr.Kind != "webhook" || *tr.TokenPrefix != "nan_whk_AbC1" || *tr.SignatureScheme != "github" || tr.Cron != nil || !tr.Enabled {
		t.Fatalf("trigger = %+v", tr)
	}
	var deliveries DeliveryList
	golden(t, "automation_deliveries.json", &deliveries)
	d := deliveries.Data[0]
	if d.Outcome != "run_created" || *d.RunID != goldenRun || d.TriggerID == "" || d.DeliveryID == "" {
		t.Fatalf("delivery = %+v", d)
	}
}

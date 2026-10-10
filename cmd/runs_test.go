package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

func resetRunsFlags() {
	lsWorkspace, lsState, lsLimit, lsJSON = "", "", 20, false
	showJSON = false
	logsFollow, logsAfter, logsJSON = false, 0, false
	cancelJSON = false
}

func TestRunsLsJSON(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{})
	lsJSON, lsWorkspace, lsState, lsLimit = true, "develop", "running", 5
	if err := doRunsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	var list api.RunList
	if err := json.Unmarshal(h.stdout.Bytes(), &list); err != nil {
		t.Fatalf("%v: %q", err, h.stdout.String())
	}
	if list.Object != "list" || len(list.Data) != 1 || list.Data[0].ID != testRunID {
		t.Errorf("list = %+v", list)
	}
	if h.fake.listQuery != "limit=5&state=running&workspace=develop" {
		t.Errorf("query = %q", h.fake.listQuery)
	}
}

func TestRunsLsTable(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{})
	if err := doRunsLs(context.Background(), h.env); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"ID", "STATE", testRunID, "running", "develop", "pi", "review"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestRunsLsValidatesFlags(t *testing.T) {
	defer resetRunsFlags()
	for _, set := range []func(){
		func() { lsState = "done" },
		func() { lsLimit = 0 },
		func() { lsLimit = 101 },
	} {
		resetRunsFlags()
		set()
		if err := checkLsFlags(); exitCode(err) != exitUsage {
			t.Errorf("exit %d (%v)", exitCode(err), err)
		}
	}
}

func TestRunsShow(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{finalState: "failed", finalError: "agent_failed"})
	if err := doRunsShow(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{testRunID, "failed", "agent_failed", "develop", "worktree", "auto", "prompt:"} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Errorf("show lacks %q:\n%s", want, h.stdout.String())
		}
	}

	h = newHarness(t, &fakePlatform{})
	showJSON = true
	if err := doRunsShow(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	var run api.Run
	if err := json.Unmarshal(h.stdout.Bytes(), &run); err != nil || run.ID != testRunID {
		t.Errorf("--json: %v %q", err, h.stdout.String())
	}
}

func TestRunsLogsJSONIsOneEventPerLine(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{})
	logsJSON = true
	logsAfter = 7
	if err := doRunsLogs(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
	var ev api.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev.Seq != 1 || ev.Type != "message" {
		t.Errorf("%v: %q", err, lines[0])
	}
	if strings.Contains(h.stdout.String(), "\x1b") {
		t.Error("a raw ESC in JSON output")
	}
	if len(h.fake.eventsQuery) != 1 || !strings.Contains(h.fake.eventsQuery[0], "after=7") {
		t.Errorf("queries = %q", h.fake.eventsQuery)
	}
}

func TestRunsLogsTextIsSanitised(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{})
	if err := doRunsLogs(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	if h.stdout.String() != "hello\n" {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

func TestRunsLogsFollowExitsWithTheRunsCode(t *testing.T) {
	defer resetRunsFlags()
	for _, tc := range []struct {
		state, errorCode string
		want             int
	}{{"succeeded", "", 0}, {"failed", "agent_failed", 1}, {"timed_out", "", 2}, {"cancelled", "", 3}, {"failed", "config_error", 4}} {
		state, want := tc.state, tc.want
		resetRunsFlags()
		logsFollow = true
		logsAfter = 1
		h := newHarness(t, &fakePlatform{finalState: state, finalError: tc.errorCode})
		err := doRunsLogs(context.Background(), h.env, testRunID)
		if exitCode(err) != want {
			t.Errorf("%s: exit %d (%v), want %d", state, exitCode(err), err, want)
		}
		if h.fake.lastEventIDs[0] != "1" {
			t.Errorf("%s: --after 1 sent Last-Event-ID %q", state, h.fake.lastEventIDs[0])
		}
	}
}

func TestRunsLogsFollowJSONEndsWithTheRun(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	logsFollow, logsJSON = true, true
	h := newHarness(t, &fakePlatform{})
	if err := doRunsLogs(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
	var run api.Run
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &run); err != nil || run.Object != "run" {
		t.Errorf("last line %q: %v", lines[len(lines)-1], err)
	}
}

func TestRunsCancel(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{})
	if err := doRunsCancel(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	if h.fake.cancels != 1 || !strings.Contains(h.stdout.String(), "cancel requested") {
		t.Errorf("cancels=%d stdout=%q", h.fake.cancels, h.stdout.String())
	}

	h = newHarness(t, &fakePlatform{cancelStatus: http.StatusOK})
	if err := doRunsCancel(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "already ended") {
		t.Errorf("stdout = %q", h.stdout.String())
	}

	h = newHarness(t, &fakePlatform{})
	cancelJSON = true
	if err := doRunsCancel(context.Background(), h.env, testRunID); err != nil {
		t.Fatal(err)
	}
	var run api.Run
	if err := json.Unmarshal(h.stdout.Bytes(), &run); err != nil || run.ID != testRunID {
		t.Errorf("--json: %v %q", err, h.stdout.String())
	}
}

func TestRunsNotFoundExits64(t *testing.T) {
	defer resetRunsFlags()
	resetRunsFlags()
	h := newHarness(t, &fakePlatform{})
	other := "00000000-0000-4000-8000-000000000000"
	for name, call := range map[string]func() error{
		"show":   func() error { return doRunsShow(context.Background(), h.env, other) },
		"cancel": func() error { return doRunsCancel(context.Background(), h.env, other) },
		"logs":   func() error { return doRunsLogs(context.Background(), h.env, other) },
	} {
		err := call()
		if exitCode(err) != exitUsage || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s: exit %d (%v)", name, exitCode(err), err)
		}
	}
}

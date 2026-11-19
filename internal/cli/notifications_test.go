package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Notification channels (helmcode/nan-devops
// AGENT-AUTOMATIONS-PHASE3-NOTIFICATIONS.md §4, helmcode/nan-cloud-api#349)
// against the fakeWrite platform: /v1/notification-channels with cloud-api's
// golden objects (internal/api/testdata/phase3/notification_*.json).

const (
	chanSlackID = "c0000000-0000-4000-8000-000000000001"
	chanHTTPSID = "c0000000-0000-4000-8000-000000000002"
	// testHookURL is a webhook URL: a credential, never printed, never sent
	// anywhere but the body of a create or a URL replace.
	testHookURL   = "https://hooks.slack.com/services/T0000/B0000/HOOKSECRETdoNotLeak42"
	testHookHTTPS = "https://hooks.example.com/nan/HOOKSECRETdoNotLeak42"
)

// defaultEvents are what the platform gives a channel created without
// events (spec §4.2: run.finished is opt-in).
var defaultEvents = []string{"approval.pending", "run.failed", "automation.disabled", "trigger.failing"}

func phase3File(t testing.TB, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "api", "testdata", "phase3", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func notifNotFound(w http.ResponseWriter, msg string) {
	w.WriteHeader(404)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "code": "not_found"}})
}

// serveNotifications is the fake /v1/notification-channels (f.mu held).
func (f *fakeWrite) serveNotifications(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
	if f.notifOff || !f.writeAPI {
		notifNotFound(w, "Not found.")
		return
	}
	if f.channels == nil {
		var list struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(phase3File(t, "notification_channels_list.json"), &list); err != nil {
			t.Fatal(err)
		}
		f.channels = list.Data
	}
	writeJSON := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/notification-channels"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			writeJSON(200, map[string]any{"object": "list", "data": f.channels})
		case http.MethodPost:
			var in struct {
				Name         string    `json:"name"`
				Kind         string    `json:"kind"`
				URL          string    `json:"url"`
				Events       *[]string `json:"events"`
				AutomationID *string   `json:"automation_id"`
			}
			_ = json.Unmarshal(body, &in)
			events := defaultEvents
			if in.Events != nil {
				events = *in.Events
			}
			subs := []any{}
			for _, e := range events {
				subs = append(subs, map[string]any{"event": e, "automation_id": in.AutomationID})
			}
			host := strings.SplitN(strings.TrimPrefix(in.URL, "https://"), "/", 2)[0]
			c := map[string]any{
				"id": "c0000000-0000-4000-8000-000000000003", "object": "notification_channel", "name": in.Name,
				"kind": in.Kind, "url_hint": host + "/…/ak42", "enabled": true, "disabled_reason": nil, "consecutive_failures": 0,
				"last_delivery_at": nil, "last_delivery_status": nil, "subscriptions": subs, "signing": nil, "updated_by": "cli_session",
				"created_at": "2026-10-14T12:00:00Z", "updated_at": "2026-10-14T12:00:00Z",
			}
			out := map[string]any{}
			for k, v := range c {
				out[k] = v
			}
			if in.Kind == "https" {
				c["signing"] = map[string]any{"previous_expires_at": nil}
				out["signing"] = c["signing"]
				var g map[string]any
				_ = json.Unmarshal(phase3File(t, "notification_channel_create_https.json"), &g)
				out["signing_secret"] = g["signing_secret"]
			}
			f.channels = append(f.channels, c)
			writeJSON(201, out)
		}
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	var ch map[string]any
	idx := -1
	for i, c := range f.channels {
		if c["id"] == parts[0] {
			ch, idx = c, i
		}
	}
	if ch == nil {
		notifNotFound(w, "notification channel not found")
		return
	}
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}
	switch r.Method + " " + sub {
	case "GET ":
		writeJSON(200, ch)
	case "PATCH ":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if v, ok := in["name"]; ok {
			ch["name"] = v
		}
		if v, ok := in["enabled"]; ok {
			ch["enabled"] = v
			if v == true {
				ch["disabled_reason"], ch["consecutive_failures"] = nil, 0
			} else {
				ch["disabled_reason"] = "member"
			}
		}
		if _, ok := in["url"]; ok {
			ch["consecutive_failures"] = 0
			if ch["disabled_reason"] != "member" {
				ch["enabled"], ch["disabled_reason"] = true, nil
			}
		}
		writeJSON(200, ch)
	case "DELETE ":
		f.channels = append(f.channels[:idx], f.channels[idx+1:]...)
		w.WriteHeader(204)
	case "PUT subscriptions":
		var in struct {
			Subscriptions []any `json:"subscriptions"`
		}
		_ = json.Unmarshal(body, &in)
		if in.Subscriptions == nil {
			in.Subscriptions = []any{}
		}
		ch["subscriptions"] = in.Subscriptions
		writeJSON(200, ch)
	case "POST test":
		if ch["enabled"] != true {
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"message": "the channel is disabled; enable it first",
				"type":    "invalid_request_error", "code": "channel_disabled",
			}})
			return
		}
		writeJSON(200, map[string]any{"delivered": true, "status": "delivered", "http_status": 200, "error": nil})
	case "POST rotate-signing-secret":
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write(phase3File(t, "notification_rotate.json"))
	case "GET deliveries":
		w.WriteHeader(200)
		_, _ = w.Write(phase3File(t, "notification_deliveries.json"))
	default:
		notifNotFound(w, "Not found.")
	}
}

func v1Err(_ int, code, param, msg string) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": param, "code": code}})
	return string(b)
}

// noHookLeak checks the webhook URL is only in the body of the request
// that had to carry it.
func noHookLeak(t *testing.T, r writeRun, fake *fakeWrite) {
	t.Helper()
	for what, text := range map[string]string{"stdout": r.stdout, "stderr": r.stderr, "message": r.msg} {
		if strings.Contains(text, "HOOKSECRET") {
			t.Errorf("%s carries the webhook URL: %q", what, text)
		}
	}
	for _, req := range fake.requests() {
		carries := (req.Method == http.MethodPost && req.Path == "/v1/notification-channels") || req.Method == http.MethodPatch
		if !carries && strings.Contains(req.Body+req.Path, "HOOKSECRET") {
			t.Errorf("%s %s carries the webhook URL", req.Method, req.Path)
		}
	}
	_ = filepath.Walk(r.home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "HOOKSECRET") {
				t.Errorf("%s holds the webhook URL", path)
			}
		}
		return nil
	})
}

func mustExit(t *testing.T, r writeRun, code int, wants ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d (want %d): %s\nstdout: %s\nstderr: %s", r.code, code, r.msg, r.stdout, r.stderr)
	}
	wantAll(t, "message", r.msg, wants...)
}

func TestNotificationsLs(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "ls")
	mustExit(t, r, 0)
	checkGolden(t, "notifications/ls.golden", r.stdout)
	wantAll(t, "stderr", r.stderr, "ci receiver", "nan notifications edit", "--enable")
	if len(fake.sent("GET", "/api/auth/me")) != 1 || len(fake.sent("GET", "/v1/notification-channels")) != 1 {
		t.Errorf("requests %v", fake.requests())
	}

	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "ls", "--json")
	mustExit(t, r, 0)
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || len(out.Data) != 2 || out.Data[0]["url_hint"] != "hooks.slack.com/…/abcd" {
		t.Errorf("%v %s", err, r.stdout)
	}

	fake = newFakeWrite()
	fake.channels = []map[string]any{}
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "ls")
	mustExit(t, r, 0)
	if r.stdout != "" {
		t.Errorf("stdout %q", r.stdout)
	}
	wantAll(t, "stderr", r.stderr, "No notification channels yet", "nan notifications add")
}

func TestNotificationsShow(t *testing.T) {
	fake := newFakeWrite()
	// Names are unique whatever the case: a different case still finds it.
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "show", "CI Receiver")
	mustExit(t, r, 0)
	checkGolden(t, "notifications/show_failing.golden", r.stdout)

	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "show", strings.ToUpper(chanSlackID))
	mustExit(t, r, 0)
	checkGolden(t, "notifications/show_slack.golden", r.stdout)

	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "show", "team alerts", "--json")
	mustExit(t, r, 0)
	if !strings.Contains(r.stdout, `"kind": "slack"`) || strings.Contains(r.stdout, "signing_secret") {
		t.Errorf("%s", r.stdout)
	}

	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "show", "nope")
	mustExit(t, r, exitUsage, "no notification channel named", "nan notifications ls")
}

func TestNotificationsAddSlackFromAFile(t *testing.T) {
	fake := newFakeWrite()
	file := writeValueFile(t, testHookURL+"\n")
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "add", "deploys", "--kind", "slack", "--url-file", file)
	mustExit(t, r, 0)
	posts := fake.sent("POST", "/v1/notification-channels")
	if len(posts) != 1 || posts[0].Body != `{"name":"deploys","kind":"slack","url":"`+testHookURL+`"}` {
		t.Fatalf("posts %v", posts)
	}
	if posts[0].Cookie != "nan_session=sess-cli" {
		t.Errorf("cookie %q", posts[0].Cookie)
	}
	checkGolden(t, "notifications/add_slack.golden", r.stdout)
	wantAll(t, "stderr", r.stderr, "nan notifications test deploys")
	noHookLeak(t, r, fake)
}

func TestNotificationsAddHTTPSPrintsTheSecretOnce(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession, stdin: testHookHTTPS + "\n"},
		"notifications", "add", "my receiver", "--kind", "https", "--url-stdin",
		"--event", "run.finished,run.failed", "--event", "approval.pending", "--automation", "PR review api")
	mustExit(t, r, 0)
	posts := fake.sent("POST", "/v1/notification-channels")
	want := `{"name":"my receiver","kind":"https","url":"` + testHookHTTPS + `","events":["run.finished","run.failed","approval.pending"],"automation_id":"` + testAutoID + `"}`
	if len(posts) != 1 || posts[0].Body != want {
		t.Fatalf("posts %v", posts)
	}
	checkGolden(t, "notifications/add_https.golden", r.stdout)
	if n := strings.Count(r.stdout, "nan_nsec_SHOWNONCE"); n != 1 {
		t.Errorf("secret printed %d times", n)
	}
	if strings.Contains(r.stderr, "nan_nsec_") {
		t.Error("secret on stderr")
	}
	wantAll(t, "stderr", r.stderr, notifSecretNote)
	noHookLeak(t, r, fake)

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession, stdin: testHookHTTPS},
		"notifications", "add", "my receiver", "--kind", "https", "--url-stdin", "--no-events", "--json")
	mustExit(t, r, 0)
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || !strings.HasPrefix(out["signing_secret"].(string), "nan_nsec_") {
		t.Errorf("%v %s", err, r.stdout)
	}
	if b := fake.sent("POST", "/v1/notification-channels")[0].Body; !strings.Contains(b, `"events":[]`) {
		t.Errorf("body %s", b)
	}
	wantAll(t, "stderr", r.stderr, notifSecretNote)
	noHookLeak(t, r, fake)
}

// The URL is a credential: never an argument, never --url.
func TestNotificationsURLNeverOnTheCommandLine(t *testing.T) {
	file := writeValueFile(t, testHookURL)
	for _, c := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"add", "x", "--kind", "slack", testHookURL}, []string{"--url-file", "shell history"}},
		{[]string{"add", "x", "--kind", "slack", "--url", testHookURL}, []string{"--url-file", "--url-stdin", "shell history"}},
		{[]string{"add", "x", "--kind", "slack", "--url=" + testHookURL}, []string{"--url-file", "shell history"}},
		{[]string{"edit", "team alerts", "--url", testHookURL}, []string{"--url-file", "shell history"}},
		{[]string{"edit", "team alerts", testHookURL}, []string{"shell history"}},
		{[]string{"add", "x", "--kind", "slack"}, []string{"--url-file PATH or --url-stdin"}},
		{[]string{"add", "x", "--kind", "slack", "--url-file", file, "--url-stdin"}, []string{"not both"}},
		{[]string{"add", "x", "--url-file", file}, []string{"--kind", "slack, teams, discord or https"}},
		{[]string{"add", "x", "--kind", "email", "--url-file", file}, []string{"slack, teams, discord or https"}},
		{[]string{"add", "x", "--kind", "slack", "--url-file", file, "--event", "run.started"}, []string{"run.started", "approval.pending"}},
		{[]string{"add", "x", "--kind", "slack", "--url-file", file, "--event", "run.failed", "--no-events"}, []string{"--no-events"}},
		{[]string{"add"}, []string{"name"}},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"notifications"}, c.args...)...)
		mustExit(t, r, exitUsage, c.wants...)
		for _, req := range fake.requests() {
			if req.Method != http.MethodGet {
				t.Errorf("%v sent %s %s", c.args, req.Method, req.Path)
			}
		}
		noHookLeak(t, r, fake)
	}
	// Too long or empty: refused before anything is sent, in the URL's words.
	for in, want := range map[string]string{
		"https://x.example/" + strings.Repeat("a", 2100): "over 2048 bytes", "\n": "the URL is empty",
		strings.Repeat("a", 20000): "the URL is over 16 KiB",
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession, stdin: in}, "notifications", "add", "x", "--kind", "https", "--url-stdin")
		mustExit(t, r, exitUsage, want)
		if strings.Contains(r.msg, "value") || strings.Contains(r.msg, "secret") {
			t.Errorf("message speaks of a secret value: %s", r.msg)
		}
		if len(fake.sent("POST", "/v1/notification-channels")) != 0 {
			t.Error("sent it")
		}
	}
	// --url-stdin from a terminal would echo the URL.
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession, tty: true}, "notifications", "add", "x", "--kind", "slack", "--url-stdin")
	mustExit(t, r, exitUsage, "stdin is a terminal")
}

func TestNotificationsEdit(t *testing.T) {
	fake := newFakeWrite()
	file := writeValueFile(t, testHookHTTPS+"\n")
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "edit", "ci receiver", "--url-file", file, "--enable", "--name", "ci")
	mustExit(t, r, 0)
	p := fake.sent("PATCH", "/v1/notification-channels/"+chanHTTPSID)
	if len(p) != 1 || p[0].Body != `{"name":"ci","enabled":true,"url":"`+testHookHTTPS+`"}` {
		t.Fatalf("patch %v", p)
	}
	wantAll(t, "stdout", r.stdout, "notification channel ci updated: on")
	noHookLeak(t, r, fake)

	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "edit", "team alerts", "--disable", "--json")
	mustExit(t, r, 0)
	if !strings.Contains(r.stdout, `"disabled_reason": "member"`) {
		t.Errorf("%s", r.stdout)
	}
	if p := fake.sent("PATCH", "/v1/notification-channels/"+chanSlackID); len(p) != 1 || p[0].Body != `{"enabled":false}` {
		t.Errorf("patch %v", p)
	}

	for args, want := range map[string]string{
		chanSlackID:                         "nothing to change",
		chanSlackID + " --enable --disable": "--enable or --disable",
		chanSlackID + " --kind teams":       "unknown flag",
		chanSlackID + " --name ":            "name",
	} {
		r = runWrite(t, newFakeWrite(), writeSetup{session: cliSession}, append([]string{"notifications", "edit"}, splitArgs(args)...)...)
		mustExit(t, r, exitUsage, want)
	}
}

// splitArgs splits on spaces, keeping a trailing empty value for "--name ".
func splitArgs(s string) []string {
	if strings.HasSuffix(s, " ") {
		return append(strings.Fields(s), "")
	}
	return strings.Fields(s)
}

func TestNotificationsRm(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "rm", "team alerts")
	mustExit(t, r, exitUsage, "--yes")
	if len(fake.sent("DELETE", "/v1/notification-channels/"+chanSlackID)) != 0 {
		t.Fatal("deleted without confirmation")
	}
	r = runWrite(t, fake, writeSetup{session: cliSession, tty: true, stdin: "n\n"}, "notifications", "rm", "team alerts")
	mustExit(t, r, exitFailed, "cancelled")
	r = runWrite(t, fake, writeSetup{session: cliSession, tty: true, stdin: "y\n"}, "notifications", "rm", "team alerts")
	mustExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "delete notification channel team alerts?")
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "delete", "ci receiver", "--yes")
	mustExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "notification channel ci receiver deleted")
	if len(fake.sent("DELETE", "/v1/notification-channels/"+chanSlackID)) != 1 || len(fake.sent("DELETE", "/v1/notification-channels/"+chanHTTPSID)) != 1 {
		t.Errorf("%v", fake.requests())
	}
}

func TestNotificationsTest(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "test", "team alerts")
	mustExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "test notification delivered to team alerts (HTTP 200)")

	// A disabled channel: the platform refuses, the CLI says how to enable it.
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "test", "ci receiver")
	mustExit(t, r, exitUsage, "ci receiver is disabled", "nan notifications edit 'ci receiver' --enable")

	fake = newFakeWrite()
	fake.failOn("POST /v1/notification-channels/"+chanSlackID+"/test", 200, string(phase3File(t, "notification_test_failed.json")))
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "test", "team alerts")
	mustExit(t, r, exitFailed, "not delivered", "HTTP 404", "refused", "--url-file")
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "test", "team alerts", "--json")
	mustExit(t, r, exitFailed)
	assertOneJSONDocument(t, r.stdout)
	if !strings.Contains(r.stdout, `"error": "http_4xx"`) {
		t.Errorf("%s", r.stdout)
	}

	fake = newFakeWrite()
	fake.failOn("POST /v1/notification-channels/"+chanSlackID+"/test", 429,
		`{"error":{"message":"too many notification requests recently; try again later","type":"rate_limit_exceeded","code":"rate_limited"}}`)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "test", "team alerts")
	mustExit(t, r, exitUnavailable, "too many test notifications")
}

func TestNotificationTestHints(t *testing.T) {
	for code, want := range map[string]string{
		"timeout": "did not answer", "connect": "did not answer", "tls": "TLS", "dns": "does not resolve",
		"blocked_address": "does not send to", "invalid_url": "does not send to", "redirect": "redirect",
		"http_4xx": "refused", "http_5xx": "error", "rate_limited": "budget", "channel_disabled": "--enable",
	} {
		if h := testFailureHint(code, "x"); !strings.Contains(h, want) {
			t.Errorf("%s: %q lacks %q", code, h, want)
		}
	}
}

func TestNotificationsRotate(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "rotate", "ci receiver")
	mustExit(t, r, 0)
	checkGolden(t, "notifications/rotate.golden", r.stdout)
	wantAll(t, "stderr", r.stderr, notifSecretNote, "previous secret keeps working until 2026-10-15 12:00")
	if strings.Contains(r.stderr, "nan_nsec_") {
		t.Error("secret on stderr")
	}
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "rotate", "ci receiver", "--json")
	mustExit(t, r, 0)
	if !strings.Contains(r.stdout, `"signing_secret": "nan_nsec_ROTATED`) {
		t.Errorf("%s", r.stdout)
	}
	wantAll(t, "stderr", r.stderr, notifSecretNote)

	// A slack channel has no signing secret: refused before anything is sent.
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "rotate", "team alerts")
	mustExit(t, r, exitUsage, "only https channels have a signing secret", "team alerts is a slack channel")
	if len(fake.sent("POST", "/v1/notification-channels/"+chanSlackID+"/rotate-signing-secret")) != 0 {
		t.Error("sent the rotate")
	}
	// The platform's own refusal reads the same.
	fake = newFakeWrite()
	fake.failOn("POST /v1/notification-channels/"+chanHTTPSID+"/rotate-signing-secret", 409,
		v1Err(409, "signing_not_supported", "", "only https channels have a signing secret"))
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "rotate", "ci receiver")
	mustExit(t, r, exitUsage, "only https channels have a signing secret")
}

func TestNotificationsDeliveries(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "deliveries", "ci receiver", "--limit", "5")
	mustExit(t, r, 0)
	checkGolden(t, "notifications/deliveries.golden", r.stdout)
	reqs := fake.requests()
	last := reqs[len(reqs)-1]
	if last.Path != "/v1/notification-channels/"+chanHTTPSID+"/deliveries" || last.Query != "limit=5" {
		t.Errorf("%v", last)
	}
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "deliveries", "ci receiver", "--json")
	mustExit(t, r, 0)
	assertOneJSONDocument(t, r.stdout)
	for _, l := range []string{"0", "101", "x"} {
		r = runWrite(t, newFakeWrite(), writeSetup{session: cliSession}, "notifications", "deliveries", "ci receiver", "--limit", l)
		mustExit(t, r, exitUsage, "--limit")
	}
}

func TestNotificationsDeliveriesSendsTheLimit(t *testing.T) {
	var got string
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "deliveries", "team alerts", "--limit", "7")
	mustExit(t, r, 0)
	for _, req := range fake.requests() {
		if strings.HasSuffix(req.Path, "/deliveries") {
			got = req.Query
		}
	}
	if got != "limit=7" {
		t.Fatalf("query %q", got)
	}
	// Without --limit: the 20 the help documents.
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "deliveries", "team alerts")
	mustExit(t, r, 0)
	if p := fake.sent("GET", "/v1/notification-channels/"+chanSlackID+"/deliveries"); len(p) != 1 || p[0].Query != "limit=20" {
		t.Errorf("%v", p)
	}
}

func TestNotificationsSubscriptions(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "subscriptions", "ci receiver")
	mustExit(t, r, 0)
	checkGolden(t, "notifications/subscriptions.golden", r.stdout)
	if len(fake.sent("PUT", "/v1/notification-channels/"+chanHTTPSID+"/subscriptions")) != 0 {
		t.Error("listing wrote")
	}

	put := func(f *fakeWrite, id string) string {
		t.Helper()
		p := f.sent("PUT", "/v1/notification-channels/"+id+"/subscriptions")
		if len(p) != 1 {
			t.Fatalf("puts %v", f.requests())
		}
		return p[0].Body
	}
	for _, c := range []struct {
		args []string
		id   string
		want string
	}{
		{
			[]string{"team alerts", "--add", "run.finished=PR review api"},
			chanSlackID,
			`{"subscriptions":[{"event":"approval.pending","automation_id":null},{"event":"run.failed","automation_id":null},{"event":"automation.disabled","automation_id":null},{"event":"trigger.failing","automation_id":null},{"event":"run.finished","automation_id":"` + testAutoID + `"}]}`,
		},
		{
			[]string{"team alerts", "--remove", "run.failed", "--remove", "trigger.failing"},
			chanSlackID,
			`{"subscriptions":[{"event":"approval.pending","automation_id":null},{"event":"automation.disabled","automation_id":null}]}`,
		},
		{
			[]string{"team alerts", "--add", "run.failed"},
			chanSlackID,
			`{"subscriptions":[{"event":"approval.pending","automation_id":null},{"event":"run.failed","automation_id":null},{"event":"automation.disabled","automation_id":null},{"event":"trigger.failing","automation_id":null}]}`,
		},
		{
			[]string{"ci receiver", "--set", "run.failed", "--set", "approval.pending=" + testAutoID},
			chanHTTPSID,
			`{"subscriptions":[{"event":"run.failed","automation_id":null},{"event":"approval.pending","automation_id":"` + testAutoID + `"}]}`,
		},
		{[]string{"ci receiver", "--remove", "run.finished=PR review api"}, chanHTTPSID, `{"subscriptions":[]}`},
		{[]string{"ci receiver", "--clear"}, chanHTTPSID, `{"subscriptions":[]}`},
		// An id typed in capitals is the same automation.
		{[]string{"ci receiver", "--remove", "run.finished=" + strings.ToUpper(testAutoID)}, chanHTTPSID, `{"subscriptions":[]}`},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"notifications", "subscriptions"}, c.args...)...)
		mustExit(t, r, 0)
		if got := put(fake, c.id); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.args, got, c.want)
		}
		if r.stdout == "" {
			t.Errorf("%v printed nothing", c.args)
		}
	}

	r = runWrite(t, newFakeWrite(), writeSetup{session: cliSession}, "notifications", "subscriptions", "ci receiver", "--clear", "--json")
	mustExit(t, r, 0)
	if !strings.Contains(r.stdout, `"subscriptions": []`) {
		t.Errorf("%s", r.stdout)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--set", "run.failed", "--add", "run.finished"}, "--set"},
		{[]string{"--clear", "--remove", "run.failed"}, "--clear"},
		{[]string{"--add", "run.started"}, "run.started"},
		{[]string{"--add", "=PR review api"}, "EVENT"},
		{[]string{"--remove", "approval.pending=PR review api"}, "not subscribed"},
		{[]string{"--add", "run.failed=nope"}, "no automation named"},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"notifications", "subscriptions", "team alerts"}, c.args...)...)
		mustExit(t, r, exitUsage, c.want)
		if len(fake.sent("PUT", "/v1/notification-channels/"+chanSlackID+"/subscriptions")) != 0 {
			t.Errorf("%v wrote", c.args)
		}
	}
}

// The refusals of the platform, each with what to do next.
func TestNotificationsErrors(t *testing.T) {
	file := writeValueFile(t, testHookURL)
	add := []string{"notifications", "add", "deploys", "--kind", "slack", "--url-file", file}
	for _, c := range []struct {
		status int
		body   string
		code   int
		wants  []string
	}{
		{400, v1Err(400, "invalid_url", "url", "the URL is not valid"), exitUsage, []string{"the URL was refused: the URL is not valid", "hooks.slack.com/services/"}},
		{400, v1Err(400, "invalid_url", "url", "the URL's host must resolve only to public addresses"), exitUsage, []string{"public addresses"}},
		{409, v1Err(409, "name_taken", "name", "a notification channel with this name already exists"), exitUsage, []string{"already taken", "notification channels"}},
		{409, v1Err(409, "too_many_channels", "", "a member can have at most 10 notification channels"), exitUsage, []string{"10 notification channels", "nan notifications rm"}},
		{404, v1Err(404, "automation_not_found", "automation_id", "automation not found"), exitUsage, []string{"automation", "nan automations ls"}},
		{429, `{"error":{"message":"Too many notification channel changes recently. Try again later.","type":"rate_limit_exceeded","code":"rate_limited"}}`, exitUnavailable, []string{"too many changes to your notification channels"}},
		{503, `{"error":{"message":"notifications are temporarily unavailable; try again","type":"server_error","code":"notifications_unavailable"}}`, exitUnavailable, []string{"temporarily unavailable"}},
		{403, `{"error":{"message":"this token lacks the notifications:write scope","type":"permission_error","code":"token_scope_denied"}}`, exitAuth, []string{"notifications:write", "Settings > Tokens"}},
		{403, `{"error":{"message":"x","type":"permission_error","code":"credential_not_allowed"}}`, exitAuth, []string{"nan auth login"}},
		{
			400, v1Err(400, "invalid_request", "name", "the name must be 1 to 64 characters without control characters"), exitUsage,
			[]string{"name: the name must be 1 to 64 characters"},
		},
	} {
		fake := newFakeWrite()
		fake.failOn("POST /v1/notification-channels", c.status, c.body)
		r := runWrite(t, fake, writeSetup{session: cliSession}, add...)
		mustExit(t, r, c.code, c.wants...)
		noHookLeak(t, r, fake)
	}
}

// Without the feature, every command says where things stand and exits 64.
func TestNotificationsFeatureOff(t *testing.T) {
	file := writeValueFile(t, testHookURL)
	cmds := [][]string{
		{"ls"},
		{"show", "team alerts"},
		{"add", "x", "--kind", "slack", "--url-file", file},
		{"edit", "team alerts", "--enable"},
		{"rm", "team alerts", "--yes"},
		{"test", "team alerts"},
		{"rotate", "ci receiver"},
		{"deliveries", "team alerts"},
		{"subscriptions", "team alerts"},
	}
	for _, args := range cmds {
		// A session asks /api/auth/me.
		fake := newFakeWrite()
		fake.notifOff = true
		r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"notifications"}, args...)...)
		mustExit(t, r, exitUsage, "not available for your account", "Settings > Notifications", "https://nan.builders/docs/notifications")
		if len(fake.sent("POST", "/v1/notification-channels")) != 0 {
			t.Errorf("%v sent a write", args)
		}
		// A token has no /api/auth/me: the platform's 404 says it.
		fake = newFakeWrite()
		fake.notifOff = true
		r = runWrite(t, fake, writeSetup{env: "nan_pat_" + strings.Repeat("a", 43)}, append([]string{"notifications"}, args...)...)
		mustExit(t, r, exitUsage, "not available for your account")
		noHookLeak(t, r, fake)
	}
	// The write API off: the CLI cannot manage channels, the portal can.
	fake := newFakeWrite()
	fake.writeAPI = false
	r := runWrite(t, fake, writeSetup{session: cliSession}, "notifications", "ls")
	mustExit(t, r, exitUsage, "from the CLI is not available for your account yet", "portal")
}

// A token with notifications:write only cannot list: an id still works.
func TestNotificationsWriteOnlyToken(t *testing.T) {
	tok := "nan_pat_" + strings.Repeat("b", 43)
	scope := `{"error":{"message":"this token lacks the notifications:read scope","type":"permission_error","code":"token_scope_denied"}}`
	fake := newFakeWrite()
	fake.failOn("GET /v1/notification-channels", 403, scope)
	r := runWrite(t, fake, writeSetup{env: tok}, "notifications", "edit", chanSlackID, "--disable")
	mustExit(t, r, 0)
	if p := fake.sent("PATCH", "/v1/notification-channels/"+chanSlackID); len(p) != 1 || p[0].Authz != "Bearer "+tok {
		t.Errorf("%v", fake.requests())
	}
	if len(fake.sent("GET", "/api/auth/me")) != 0 {
		t.Error("a token asked /api/auth/me")
	}
	r = runWrite(t, fake, writeSetup{env: tok}, "notifications", "rm", chanSlackID, "--yes")
	mustExit(t, r, 0)
	r = runWrite(t, fake, writeSetup{env: tok}, "notifications", "edit", "team alerts", "--disable")
	mustExit(t, r, exitAuth, "notifications:read", "channel id")

	// An unknown id with such a token: not found.
	fake = newFakeWrite()
	fake.failOn("GET /v1/notification-channels", 403, scope)
	r = runWrite(t, fake, writeSetup{env: tok}, "notifications", "test", "c0000000-0000-4000-8000-0000000000ff")
	mustExit(t, r, exitUsage, "no notification channel", "c0000000-0000-4000-8000-0000000000ff")
}

func TestNotificationsHelp(t *testing.T) {
	help := notificationsCmd.Long
	for _, want := range []string{
		"notifications:read", "notifications:write", "--url-file", "--url-stdin", "shell history",
		"https://nan.builders/docs/notifications", "Settings > Notifications", "approval.pending", "run.finished",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("nan notifications --help lacks %q", want)
		}
	}
	var all strings.Builder
	all.WriteString(help)
	for _, c := range notificationsCmd.Commands() {
		all.WriteString(c.Long + c.Short + c.Example)
		if c.InheritedFlags().Lookup("token-file") == nil {
			t.Errorf("nan notifications %s has no --token-file", c.Name())
		}
	}
	if strings.ContainsRune(all.String(), 0x2014) {
		t.Error("an em-dash in the notifications help")
	}
}

func TestNotificationsUnknownVerbs(t *testing.T) {
	for verb, want := range map[string]string{"create": "add", "list": "ls", "rotate-secret": "rotate", "remove-me": "unknown command"} {
		r := runWrite(t, newFakeWrite(), writeSetup{session: cliSession}, "notifications", verb)
		mustExit(t, r, exitUsage, want)
	}
}

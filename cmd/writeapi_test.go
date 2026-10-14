package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The write API (helmcode/nan-devops AGENT-AUTOMATIONS-PHASE3-WRITE-API.md,
// helmcode/nan-cloud-api#348) as a test server: /api/auth/me for the flag,
// /v1/secrets, the automation and trigger writes, with cloud-api's own
// golden responses where it has them.

// testValue is a secret value that must never be printed or sent anywhere
// but the body of a create or a value replace.
const testValue = "ghp_TESTVALUEdoNotLeak0123456789abcdef"

type writeReq struct {
	Method, Path, Cookie, Authz, IdemKey, Body string
	Query                                      string
}

type fakeWrite struct {
	mu       sync.Mutex
	writeAPI bool
	secrets  []map[string]any
	reqs     []writeReq
	idem     map[string]map[string]any
	// fail answers "METHOD /path" with status and body.
	fail map[string]struct {
		status int
		body   string
	}
	// dropFirstCreate closes the connection of the first POST /v1/secrets
	// after storing the secret: an answer lost on the way.
	dropFirstCreate bool
	dropped         bool
	automationPatch map[string]any
	// Notification channels (notifications_test.go): notifOff answers
	// features.notifications=false and 404 on every channel route;
	// channels nil loads cloud-api's golden list.
	notifOff bool
	channels []map[string]any
}

func newFakeWrite() *fakeWrite {
	return &fakeWrite{
		writeAPI: true,
		idem:     map[string]map[string]any{},
		secrets: []map[string]any{{
			"id": "5e000000-0000-4000-8000-000000000001", "object": "secret", "name": "GITHUB_TOKEN", "kind": "generic",
			"provider": nil, "base_url": nil, "hint": "…a1b2", "value_bytes": 93, "description": "fine-grained",
			"used_by": []any{map[string]any{"id": testAutoID, "name": "PR review api"}}, "updated_by": "cli_session",
			"created_at": "2026-10-10T12:00:00Z", "updated_at": "2026-10-10T12:00:00Z", "last_used_at": nil,
		}},
	}
}

func (f *fakeWrite) failOn(route string, status int, body string) {
	if f.fail == nil {
		f.fail = map[string]struct {
			status int
			body   string
		}{}
	}
	f.fail[route] = struct {
		status int
		body   string
	}{status, body}
}

func phase2File(t testing.TB, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "internal", "api", "testdata", "phase2", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fakeWrite) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, writeReq{r.Method, r.URL.Path, r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), string(body), r.URL.RawQuery})
		w.Header().Set("Content-Type", "application/json")
		if f.dropFirstCreate {
			// A fresh connection per request: net/http only retries a
			// request itself on a reused connection, so what follows a
			// dropped answer is the CLI's own retry.
			w.Header().Set("Connection", "close")
		}
		route := r.Method + " " + r.URL.Path
		if fe, ok := f.fail[route]; ok {
			if fe.status == 204 {
				w.WriteHeader(204)
				return
			}
			w.WriteHeader(fe.status)
			_, _ = io.WriteString(w, fe.body)
			return
		}
		writeJSON := func(status int, v any) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		const triggerID = "b0000000-0000-4000-8000-000000000002"
		if strings.HasPrefix(r.URL.Path, "/v1/notification-channels") {
			f.serveNotifications(t, w, r, body)
			return
		}
		switch {
		case route == "GET /api/auth/me":
			writeJSON(200, map[string]any{"features": map[string]bool{"write_api": f.writeAPI, "secrets": true, "automations": true, "notifications": !f.notifOff}})
		case route == "GET /api/account/secrets", route == "GET /v1/secrets":
			if route == "GET /v1/secrets" && !f.writeAPI {
				writeJSON(404, map[string]any{"error": map[string]any{"message": "Not found", "type": "invalid_request_error", "code": "not_found"}})
				return
			}
			writeJSON(200, map[string]any{"object": "list", "data": f.secrets})
		case route == "POST /v1/secrets":
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			key := r.Header.Get("Idempotency-Key")
			if s, ok := f.idem[key]; ok && key != "" {
				writeJSON(200, s)
				return
			}
			s := map[string]any{"id": "5e000000-0000-4000-8000-000000000002", "object": "secret", "name": in["name"], "kind": "generic",
				"hint": "…cdef", "value_bytes": len(in["value"].(string)), "description": "", "used_by": []any{}, "updated_by": "cli_session",
				"created_at": "2026-10-14T10:00:00Z", "updated_at": "2026-10-14T10:00:00Z", "last_used_at": nil}
			if k, ok := in["kind"].(string); ok {
				s["kind"] = k
			}
			f.secrets = append(f.secrets, s)
			f.idem[key] = s
			if f.dropFirstCreate && !f.dropped {
				f.dropped = true
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			writeJSON(201, s)
		case strings.HasPrefix(r.URL.Path, "/v1/secrets/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/secrets/"), "/value")
			var s map[string]any
			idx := -1
			for i, x := range f.secrets {
				if x["id"] == id {
					s, idx = x, i
				}
			}
			if s == nil {
				writeJSON(404, map[string]any{"error": map[string]any{"message": "Not found", "type": "invalid_request_error", "code": "not_found"}})
				return
			}
			switch r.Method {
			case http.MethodPut:
				var in map[string]any
				_ = json.Unmarshal(body, &in)
				s["value_bytes"] = len(in["value"].(string))
				writeJSON(200, s)
			case http.MethodPatch:
				var in map[string]any
				_ = json.Unmarshal(body, &in)
				for k, v := range in {
					s[k] = v
				}
				writeJSON(200, s)
			case http.MethodDelete:
				f.secrets = append(f.secrets[:idx], f.secrets[idx+1:]...)
				w.WriteHeader(204)
			}
		case route == "GET /v1/automations":
			_, _ = w.Write(phase2File(t, "automations_list.json"))
		case route == "GET /v1/automations/"+testAutoID:
			_, _ = w.Write(phase2File(t, "automation_get.json"))
		case route == "POST /v1/automations", route == "PATCH /v1/automations/"+testAutoID:
			var a map[string]any
			_ = json.Unmarshal(phase2File(t, "automation_get.json"), &a)
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			for k, v := range in {
				a[k] = v
			}
			a["updated_by"] = "cli_session"
			status := 200
			if r.Method == http.MethodPost {
				status = 201
			}
			writeJSON(status, a)
		case route == "DELETE /v1/automations/"+testAutoID, route == "DELETE /v1/automations/"+testAutoID+"/triggers/"+triggerID:
			w.WriteHeader(204)
		case route == "GET /v1/automations/"+testAutoID+"/triggers":
			_, _ = w.Write(phase2File(t, "automation_triggers.json"))
		case route == "POST /v1/automations/"+testAutoID+"/triggers":
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(201)
			_, _ = w.Write(phase2File(t, "trigger_create.json"))
		case route == "PATCH /v1/automations/"+testAutoID+"/triggers/"+triggerID:
			var tc struct {
				Trigger map[string]any `json:"trigger"`
			}
			_ = json.Unmarshal(phase2File(t, "trigger_create.json"), &tc)
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			for k, v := range in {
				tc.Trigger[k] = v
			}
			writeJSON(200, tc.Trigger)
		case route == "POST /v1/automations/"+testAutoID+"/triggers/"+triggerID+"/rotate":
			w.Header().Set("Cache-Control", "no-store")
			var in struct {
				Rotate        []string `json:"rotate"`
				SigningSecret *string  `json:"signing_secret"`
			}
			_ = json.Unmarshal(body, &in)
			out := map[string]any{}
			for _, x := range in.Rotate {
				switch x {
				case "url":
					out["webhook_url"] = "https://api.nan.builders/hooks/nan_whk_ROTATEDROTATEDROTATED"
				case "secret":
					if in.SigningSecret != nil {
						continue // Stripe's whsec_ is stored, never echoed
					}
					var rot map[string]any
					_ = json.Unmarshal(phase2File(t, "trigger_rotate.json"), &rot)
					out["signing_secret"] = rot["signing_secret"]
				}
			}
			writeJSON(200, out)
		default:
			writeJSON(404, map[string]any{"error": map[string]any{"message": "no route " + route, "type": "invalid_request_error", "code": "not_found"}})
		}
	})
}

func (f *fakeWrite) requests() []writeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writeReq(nil), f.reqs...)
}

func (f *fakeWrite) sent(method, path string) []writeReq {
	var out []writeReq
	for _, r := range f.requests() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// writeRun is one nan invocation against a fakeWrite.
type writeRun struct {
	code           int
	msg            string
	stdout, stderr string
	home           string
}

type writeSetup struct {
	session string // session.json; "" signs nobody in
	env     string // NAN_TOKEN
	stdin   string
	tty     bool
}

const triggerUUID = "b0000000-0000-4000-8000-000000000002"

// resetCommandFlags puts every flag of the tree back to its default and
// unchanged, so one execute does not leak into the next.
func resetCommandFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			// Set("[]") would append a literal "[]" to a slice flag.
			_ = sv.Replace(nil)
			f.Changed = false
			return
		}
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetCommandFlags(sub)
	}
}

func runWrite(t *testing.T, fake *fakeWrite, setup writeSetup, args ...string) writeRun {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	home := fakeHome(t, setup.session)
	t.Setenv(tokenEnvVar, setup.env)

	swap(t, &runsBaseURL, srv.URL)
	swap(t, &writeStdin, io.Reader(strings.NewReader(setup.stdin)))
	tty := setup.tty
	swap(t, &writeStdinTTY, func() bool { return tty })
	var out, errOut bytes.Buffer
	swap(t, &writeStdout, io.Writer(&out))
	swap(t, &writeStderr, io.Writer(&errOut))

	code, msg := execute(t, args...)
	return writeRun{code: code, msg: msg, stdout: out.String(), stderr: errOut.String(), home: home}
}

const cliSession = `{"token":"sess-cli"}`

func writeValueFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// noLeak checks the value is nowhere but where it has to be.
func noLeak(t *testing.T, r writeRun, fake *fakeWrite) {
	t.Helper()
	for what, text := range map[string]string{"stdout": r.stdout, "stderr": r.stderr, "message": r.msg} {
		if strings.Contains(text, testValue) || strings.Contains(text, "TESTVALUE") {
			t.Errorf("%s carries the value: %q", what, text)
		}
	}
	for _, req := range fake.requests() {
		writesValue := (req.Method == http.MethodPost && req.Path == "/v1/secrets") ||
			(req.Method == http.MethodPut && strings.HasSuffix(req.Path, "/value"))
		if !writesValue && strings.Contains(req.Body+req.Path+req.IdemKey, "TESTVALUE") {
			t.Errorf("%s %s carries the value", req.Method, req.Path)
		}
	}
	_ = filepath.Walk(r.home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte("TESTVALUE")) {
				t.Errorf("%s holds the value", path)
			}
		}
		return nil
	})
}

var idemKeyPattern = regexp.MustCompile(`^nan-cli-[0-9a-f]{32}$`)

func TestSecretsSetCreatesFromAFile(t *testing.T) {
	fake := newFakeWrite()
	file := writeValueFile(t, testValue+"\n")
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "NEW_TOKEN", "--value-file", file, "--description", "for ci")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	posts := fake.sent("POST", "/v1/secrets")
	if len(posts) != 1 {
		t.Fatalf("posts: %v", fake.requests())
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(posts[0].Body), &body)
	if body["value"] != testValue || body["name"] != "NEW_TOKEN" || body["description"] != "for ci" {
		t.Errorf("body %v", body)
	}
	if _, ok := body["kind"]; ok {
		t.Errorf("sent a kind nobody asked for: %v", body)
	}
	if !idemKeyPattern.MatchString(posts[0].IdemKey) {
		t.Errorf("Idempotency-Key %q", posts[0].IdemKey)
	}
	if posts[0].Cookie != "nan_session=sess-cli" {
		t.Errorf("cookie %q", posts[0].Cookie)
	}
	if len(fake.sent("GET", "/api/auth/me")) != 1 {
		t.Error("did not ask the platform whether it takes CLI writes")
	}
	wantAll(t, "stdout", r.stdout, "NEW_TOKEN created", "hint …cdef")
	noLeak(t, r, fake)
}

func TestSecretsSetReplacesTheValueOfAnExistingSecret(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession, stdin: testValue + "\r\n"},
		"secrets", "set", "GITHUB_TOKEN", "--value-stdin", "--description", "rotated")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	const path = "/v1/secrets/5e000000-0000-4000-8000-000000000001"
	puts := fake.sent("PUT", path+"/value")
	if len(puts) != 1 || puts[0].Body != `{"value":"`+testValue+`"}` {
		t.Fatalf("puts %v", puts)
	}
	if p := fake.sent("PATCH", path); len(p) != 1 || p[0].Body != `{"description":"rotated"}` {
		t.Errorf("patch %v", p)
	}
	if len(fake.sent("POST", "/v1/secrets")) != 0 {
		t.Error("created a second secret")
	}
	wantAll(t, "stdout", r.stdout, "GITHUB_TOKEN: value replaced")
	wantAll(t, "stderr", r.stderr, "PR review api")
	noLeak(t, r, fake)
}

func TestSecretsSetReadsTheValueExactly(t *testing.T) {
	for in, want := range map[string]string{
		"abc\n":     "abc",
		"abc\r\n":   "abc",
		"abc\n\n":   "abc\n",
		"abc":       "abc",
		" a b c \n": " a b c ",
	} {
		swap(t, &writeStdin, io.Reader(strings.NewReader(in)))
		swap(t, &writeStdinTTY, func() bool { return false })
		got, err := readSecretValue("", true)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
}

func TestSecretsSetRefusals(t *testing.T) {
	file := writeValueFile(t, testValue)
	for name, tc := range map[string]struct {
		setup writeSetup
		args  []string
		wants []string
	}{
		"stdin is a terminal":   {writeSetup{session: cliSession, tty: true}, []string{"secrets", "set", "X_TOKEN", "--value-stdin"}, []string{"terminal", "--value-file"}},
		"no value source":       {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN"}, []string{"--value-file", "--value-stdin"}},
		"both value sources":    {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-stdin", "--value-file", file}, []string{"not both"}},
		"value on the command":  {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", testValue}, []string{"shell history", "revoke"}},
		"value as the name":     {writeSetup{session: cliSession}, []string{"secrets", "set", testValue, "--value-file", file}, []string{"shell history"}},
		"lower case name":       {writeSetup{session: cliSession}, []string{"secrets", "set", "x_token", "--value-file", file}, []string{"X_TOKEN"}},
		"empty value":           {writeSetup{session: cliSession, stdin: "\n"}, []string{"secrets", "set", "X_TOKEN", "--value-stdin"}, []string{"empty"}},
		"unknown kind":          {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-file", file, "--kind", "other"}, []string{"generic or provider_key"}},
		"provider_key no prov":  {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-file", file, "--kind", "provider_key"}, []string{"--provider"}},
		"provider google":       {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-file", file, "--provider", "google"}, []string{"Google's API is gemini"}},
		"provider unknown":      {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-file", file, "--provider", "mistral"}, []string{"anthropic, openai, gemini or openai_compatible"}},
		"provider with generic": {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-file", file, "--kind", "generic", "--provider", "openai"}, []string{"provider_key"}},
		"missing file":          {writeSetup{session: cliSession}, []string{"secrets", "set", "X_TOKEN", "--value-file", "/nonexistent/v"}, []string{"could not read"}},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, tc.setup, tc.args...)
		if r.code != exitUsage {
			t.Errorf("%s: exit %d: %s", name, r.code, r.msg)
		}
		for _, w := range tc.wants {
			if !strings.Contains(r.msg, w) {
				t.Errorf("%s: message lacks %q: %s", name, w, r.msg)
			}
		}
		if len(fake.requests()) != 0 {
			t.Errorf("%s: sent %v", name, fake.requests())
		}
		noLeak(t, r, fake)
	}
}

func TestSecretsSetKindAndProviderAreFixed(t *testing.T) {
	file := writeValueFile(t, testValue)
	for _, args := range [][]string{
		{"--kind", "provider_key", "--provider", "openai"},
		{"--provider", "anthropic"},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"secrets", "set", "GITHUB_TOKEN", "--value-file", file}, args...)...)
		if r.code != exitUsage || !strings.Contains(r.msg, "fixed when a secret is created") {
			t.Errorf("%v: exit %d: %s", args, r.code, r.msg)
		}
		for _, req := range fake.requests() {
			if req.Method != http.MethodGet {
				t.Errorf("%v: wrote %s %s", args, req.Method, req.Path)
			}
		}
	}
}

func TestSecretsSetProviderKey(t *testing.T) {
	fake := newFakeWrite()
	file := writeValueFile(t, testValue)
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "LOCAL_LLM", "--value-file", file,
		"--provider", "openai_compatible", "--base-url", "https://llm.example.com/v1")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(fake.sent("POST", "/v1/secrets")[0].Body), &body)
	if body["kind"] != "provider_key" || body["provider"] != "openai_compatible" || body["base_url"] != "https://llm.example.com/v1" {
		t.Errorf("body %v", body)
	}
}

// Every provider cloud-api accepts goes through as sent.
func TestSecretsSetEachProvider(t *testing.T) {
	file := writeValueFile(t, testValue)
	for _, p := range []string{"anthropic", "openai", "gemini"} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "LLM_KEY", "--value-file", file, "--provider", p)
		if r.code != 0 {
			t.Fatalf("%s: exit %d: %s", p, r.code, r.msg)
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(fake.sent("POST", "/v1/secrets")[0].Body), &body)
		if body["kind"] != "provider_key" || body["provider"] != p {
			t.Errorf("%s: body %v", p, body)
		}
	}
}

// A create whose answer is lost is sent again with the same key, and the
// platform answers the secret the first one created.
func TestSecretsSetRetriesWithTheSameIdempotencyKey(t *testing.T) {
	fake := newFakeWrite()
	fake.dropFirstCreate = true
	file := writeValueFile(t, testValue)
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "NEW_TOKEN", "--value-file", file)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	posts := fake.sent("POST", "/v1/secrets")
	if len(posts) != 2 || posts[0].IdemKey == "" || posts[0].IdemKey != posts[1].IdemKey {
		t.Fatalf("posts %v", posts)
	}
	if r.stderr != "" && strings.Contains(r.stderr, "could not reach") {
		t.Errorf("stderr %q", r.stderr)
	}
	if n := len(fake.secrets); n != 2 {
		t.Errorf("%d secrets: a retry created a second one", n)
	}
	wantAll(t, "stdout", r.stdout, "NEW_TOKEN created")
}

// Two invocations never share a key: a later set of the same name is a new
// request, not a replay.
func TestIdempotencyKeysAreFresh(t *testing.T) {
	a, b := newIdempotencyKey(), newIdempotencyKey()
	if a == b || !idemKeyPattern.MatchString(a) {
		t.Errorf("%q %q", a, b)
	}
}

func TestSecretsWriteAPIOffKeepsTheExplanation(t *testing.T) {
	file := writeValueFile(t, testValue)
	for name, args := range map[string][]string{
		"set":  {"secrets", "set", "NEW_TOKEN", "--value-file", file},
		"edit": {"secrets", "edit", "GITHUB_TOKEN", "--description", "x"},
		"rm":   {"secrets", "rm", "GITHUB_TOKEN", "--yes"},
	} {
		// A session: features.write_api is false.
		fake := newFakeWrite()
		fake.writeAPI = false
		r := runWrite(t, fake, writeSetup{session: cliSession}, args...)
		if r.code != exitUsage || !strings.Contains(r.msg, "https://cloud.nan.builders") || !strings.Contains(r.msg, "browser") {
			t.Errorf("%s (session): exit %d: %s", name, r.code, r.msg)
		}
		for _, req := range fake.requests() {
			if req.Path != "/api/auth/me" {
				t.Errorf("%s (session): sent %s %s", name, req.Method, req.Path)
			}
		}
		// A token: /v1/secrets is a 404.
		fake = newFakeWrite()
		fake.writeAPI = false
		r = runWrite(t, fake, writeSetup{env: testPAT}, args...)
		if r.code != exitUsage || !strings.Contains(r.msg, "https://cloud.nan.builders") {
			t.Errorf("%s (token): exit %d: %s", name, r.code, r.msg)
		}
		if strings.Contains(r.msg, testPAT) {
			t.Errorf("%s: the token is in the message", name)
		}
	}
}

// The CLI session goes first; NAN_TOKEN when there is none; an API key is
// never sent.
func TestSecretsCredentialOrder(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: `{"token":"sess-cli","platformToken":"` + testPAT + `"}`, env: testPAT}, "secrets", "ls")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	if got := fake.sent("GET", "/api/account/secrets"); len(got) != 1 || got[0].Cookie != "nan_session=sess-cli" || got[0].Authz != "" {
		t.Errorf("session ls: %v", fake.requests())
	}

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{env: testPAT}, "secrets", "ls")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	if got := fake.sent("GET", "/v1/secrets"); len(got) != 1 || got[0].Authz != "Bearer "+testPAT || got[0].Cookie != "" {
		t.Errorf("token ls: %v", fake.requests())
	}
	wantAll(t, "ls", r.stdout, "GITHUB_TOKEN", "CHANGED FROM", "CLI")

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: `{"platformToken":"` + testPAT + `"}`}, "secrets", "ls")
	if r.code != 0 || len(fake.sent("GET", "/v1/secrets")) != 1 {
		t.Errorf("saved token: exit %d %s %v", r.code, r.msg, fake.requests())
	}

	for _, setup := range []writeSetup{{env: testKey}, {session: `{"apiKey":"` + testKey + `"}`}, {}} {
		fake = newFakeWrite()
		r = runWrite(t, fake, setup, "secrets", "ls")
		if r.code != exitAuth || len(fake.requests()) != 0 || strings.Contains(r.msg, testKey) {
			t.Errorf("%+v: exit %d, sent %v: %s", setup, r.code, fake.requests(), r.msg)
		}
	}
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{env: testKey}, "secrets", "ls")
	wantAll(t, "sk- refusal", r.msg, "API key", "nan auth login")
}

func TestSecretsShow(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "show", "GITHUB_TOKEN")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	checkGolden(t, "writeapi/secrets_show.golden", r.stdout)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "show", "GITHUB_TOKEN", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"updated_by": "cli_session"`) || strings.Contains(r.stdout, `"value"`) {
		t.Errorf("exit %d: %s", r.code, r.stdout)
	}
	r = runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "show", "NOPE")
	if r.code != exitUsage || !strings.Contains(r.msg, "nan secrets ls") {
		t.Errorf("exit %d: %s", r.code, r.msg)
	}
}

func TestSecretsEdit(t *testing.T) {
	const path = "/v1/secrets/5e000000-0000-4000-8000-000000000001"
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "edit", "GITHUB_TOKEN", "--description", "")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	if p := fake.sent("PATCH", path); len(p) != 1 || p[0].Body != `{"description":""}` {
		t.Errorf("patch %v", p)
	}
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "edit", "GITHUB_TOKEN")
	if r.code != exitUsage || len(fake.requests()) != 0 {
		t.Errorf("no flags: exit %d %v", r.code, fake.requests())
	}
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "edit", "GITHUB_TOKEN", "--base-url", "https://x.example/v1")
	if p := fake.sent("PATCH", path); r.code != 0 || len(p) != 1 || p[0].Body != `{"base_url":"https://x.example/v1"}` {
		t.Errorf("exit %d patch %v", r.code, p)
	}
}

func TestSecretsRm(t *testing.T) {
	const path = "/v1/secrets/5e000000-0000-4000-8000-000000000001"
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "rm", "GITHUB_TOKEN")
	if r.code != exitUsage || !strings.Contains(r.msg, "--yes") || len(fake.sent("DELETE", path)) != 0 {
		t.Errorf("no tty, no --yes: exit %d %s", r.code, r.msg)
	}
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession, tty: true, stdin: "n\n"}, "secrets", "rm", "GITHUB_TOKEN")
	if r.code != exitFailed || len(fake.sent("DELETE", path)) != 0 {
		t.Errorf("answered no: exit %d %s", r.code, r.msg)
	}
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession, tty: true, stdin: "y\n"}, "secrets", "rm", "GITHUB_TOKEN")
	if r.code != 0 || len(fake.sent("DELETE", path)) != 1 || !strings.Contains(r.stdout, "deleted") {
		t.Errorf("answered yes: exit %d %s", r.code, r.msg)
	}
	fake = newFakeWrite()
	fake.failOn("DELETE "+path, 409, `{"error":{"message":"the secret is assigned to automations","type":"invalid_request_error","code":"secret_in_use",
		"automations":[{"id":"`+testAutoID+`","name":"PR review api"}]}}`)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "delete", "GITHUB_TOKEN", "--yes")
	if r.code != exitUsage || !strings.Contains(r.msg, "PR review api") {
		t.Errorf("in use: exit %d %s", r.code, r.msg)
	}
}

func TestWriteErrorsSayWhatToDo(t *testing.T) {
	tokenCred := credential{kind: credEnv, bearer: testPAT}
	sessCred := credential{kind: credSession, session: "s"}
	for name, tc := range map[string]struct {
		cred  credential
		body  string
		code  int
		wants []string
	}{
		"scope denied":       {tokenCred, `{"error":{"message":"This token lacks the \"secrets:write\" scope.","type":"permission_error","code":"token_scope_denied"}}`, exitAuth, []string{"secrets:write", "Settings > Tokens", "browser"}},
		"session not cli":    {sessCred, `{"error":{"message":"x","type":"permission_error","code":"credential_not_allowed"}}`, exitAuth, []string{"nan auth login"}},
		"token not allowed":  {tokenCred, `{"error":{"message":"x","type":"permission_error","code":"credential_not_allowed"}}`, exitAuth, []string{"secrets:write", tokenEnvVar}},
		"workspace key":      {tokenCred, `{"error":{"message":"x","type":"permission_error","code":"workspace_key_not_allowed"}}`, exitAuth, []string{"workspace key"}},
		"rate limited":       {sessCred, `{"error":{"message":"x","type":"rate_limit_exceeded","code":"rate_limited"}}`, exitUnavailable, []string{"try again later"}},
		"name taken":         {sessCred, `{"error":{"message":"x","type":"invalid_request_error","code":"name_taken"}}`, exitUsage, []string{"already taken"}},
		"too many":           {sessCred, `{"error":{"message":"x","type":"invalid_request_error","code":"too_many_secrets"}}`, exitUsage, []string{"nan secrets rm"}},
		"idempotency reused": {sessCred, `{"error":{"message":"x","type":"invalid_request_error","code":"idempotency_key_reused"}}`, exitUsage, []string{"idempotency key"}},
		"too large":          {sessCred, `{"error":{"message":"x","type":"invalid_request_error","code":"secrets_too_large","automations":[{"id":"a","name":"Nightly"}]}}`, exitUsage, []string{"Nightly", "size"}},
		"platform access":    {sessCred, `{"error":{"message":"x","type":"permission_error","code":"platform_access_required"}}`, exitAuth, []string{"membership"}},
		"browser only":       {sessCred, `{"error":{"message":"x","type":"permission_error","code":"browser_session_required"}}`, exitUsage, []string{"portal", "browser"}},
		"unavailable":        {sessCred, `{"error":{"message":"x","type":"api_error","code":"secrets_unavailable"}}`, exitUnavailable, []string{"try again"}},
		"bad param":          {sessCred, `{"error":{"message":"base_url must be https","type":"invalid_request_error","param":"base_url","code":"invalid_request"}}`, exitUsage, []string{"base_url"}},
	} {
		fake := newFakeWrite()
		fake.failOn("POST /v1/secrets", 400, tc.body)
		srv := httptest.NewServer(fake.handler(t))
		client := tc.cred.client().WithBaseURL(srv.URL)
		_, _, err := client.CreateSecret(t.Context(), apiCreate("X_TOKEN"), "")
		srv.Close()
		e := writeExit(err, tc.cred, "secrets", scopeSecretsWrite)
		if e.Code != tc.code {
			t.Errorf("%s: exit %d: %v", name, e.Code, e)
		}
		for _, w := range tc.wants {
			if !strings.Contains(e.Error(), w) {
				t.Errorf("%s: %q lacks %q", name, e.Error(), w)
			}
		}
		if strings.Contains(e.Error(), testPAT) || strings.Contains(e.Error(), "\x1b") {
			t.Errorf("%s: %q", name, e.Error())
		}
	}
}

func TestAutomationsCreateFromYAML(t *testing.T) {
	fake := newFakeWrite()
	file := filepath.Join(t.TempDir(), "a.yaml")
	_ = os.WriteFile(file, []byte("name: PR review api\nworkspace: "+testWSID+"\nagent: pi\napproval_kinds: []\nsecrets:\n  - secret: GITHUB_TOKEN\n    env: GITHUB_TOKEN\n    exposure: actions\nmax_runs_per_hour: 30\n"), 0o600)
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "create", "-f", file)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	posts := fake.sent("POST", "/v1/automations")
	if len(posts) != 1 {
		t.Fatalf("%v", fake.requests())
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(posts[0].Body), &body)
	if body["name"] != "PR review api" || body["max_runs_per_hour"] != float64(30) || fmt.Sprint(body["approval_kinds"]) != "[]" {
		t.Errorf("body %v", body)
	}
	if s, _ := body["secrets"].([]any); len(s) != 1 {
		t.Errorf("secrets %v", body["secrets"])
	}
	wantAll(t, "stdout", r.stdout, "PR review api created", testAutoID)
	wantAll(t, "stderr", r.stderr, "applies without asking", "triggers add")

	// JSON from stdin, sent compacted as written; --json prints the answer.
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession, stdin: "{\n \"name\": \"x\",\n \"approval_kinds\": [\"github_push\"]\n}\n"}, "automations", "create", "-f", "-", "--json")
	if r.code != 0 || fake.sent("POST", "/v1/automations")[0].Body != `{"name":"x","approval_kinds":["github_push"]}` {
		t.Fatalf("exit %d %s %v", r.code, r.msg, fake.requests())
	}
	if !strings.Contains(r.stdout, `"object": "automation"`) || !strings.Contains(r.stdout, `"updated_by": "cli_session"`) {
		t.Errorf("--json: %s", r.stdout)
	}
}

func TestReadBodyFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(content), 0o600)
		return p
	}
	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"list":     {write("l.yaml", "- a\n- b\n"), "one object"},
		"scalar":   {write("s.yaml", "hello\n"), "one object"},
		"bad json": {write("b.json", "{\"a\": }"), "not valid JSON"},
		"bad yaml": {write("b.yaml", "a: [1,\n"), "neither"},
		"missing":  {filepath.Join(dir, "nope.yaml"), "could not read"},
		"no file":  {"", "-f FILE"},
	} {
		_, err := readBodyFile(tc.path)
		if exitCode(err) != exitUsage || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	swap(t, &writeStdinTTY, func() bool { return true })
	if _, err := readBodyFile("-"); exitCode(err) != exitUsage || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("tty stdin: %v", err)
	}
}

func TestAutomationsEditEnableDisableRm(t *testing.T) {
	path := "/v1/automations/" + testAutoID
	file := filepath.Join(t.TempDir(), "patch.yaml")
	_ = os.WriteFile(file, []byte("approval_kinds: []\n"), 0o600)
	for name, tc := range map[string]struct {
		args   []string
		method string
		body   string
		out    string
	}{
		"edit by name": {[]string{"automations", "edit", "PR review api", "-f", file}, "PATCH", `{"approval_kinds":[]}`, "updated"},
		"enable":       {[]string{"automations", "enable", testAutoID}, "PATCH", `{"enabled":true}`, "enabled"},
		"disable":      {[]string{"automations", "disable", "PR review api"}, "PATCH", `{"enabled":false}`, "disabled"},
		"rm":           {[]string{"automations", "rm", "PR review api", "--yes"}, "DELETE", "", "deleted"},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, tc.args...)
		if r.code != 0 {
			t.Errorf("%s: exit %d: %s", name, r.code, r.msg)
			continue
		}
		got := fake.sent(tc.method, path)
		if len(got) != 1 || got[0].Body != tc.body {
			t.Errorf("%s: %v", name, fake.requests())
		}
		if !strings.Contains(r.stdout, tc.out) {
			t.Errorf("%s: %q", name, r.stdout)
		}
	}
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "rm", "PR review api")
	if r.code != exitUsage || len(fake.sent("DELETE", path)) != 0 {
		t.Errorf("rm without --yes: exit %d", r.code)
	}
}

func TestTriggersAddShowsTheSecretsOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "t.yaml")
	_ = os.WriteFile(file, []byte("kind: webhook\nsignature_scheme: github\nevent_filter:\n  events: [pull_request]\n"), 0o600)
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "add", "PR review api", "-f", file)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.msg)
	}
	posts := fake.sent("POST", "/v1/automations/"+testAutoID+"/triggers")
	if len(posts) != 1 || posts[0].Body != `{"event_filter":{"events":["pull_request"]},"kind":"webhook","signature_scheme":"github"}` {
		t.Fatalf("%v", fake.requests())
	}
	checkGolden(t, "writeapi/triggers_add.golden", r.stdout)
	for _, s := range []string{"whsec_shown_once", "https://api.nan.builders/hooks/nan_whk_"} {
		if n := strings.Count(r.stdout, s); n != 1 {
			t.Errorf("%q printed %d times", s, n)
		}
		if strings.Contains(r.stderr, s) {
			t.Errorf("%q on stderr", s)
		}
	}
	wantAll(t, "stderr", r.stderr, "Store these now", "never shown again", "triggers rotate")
	assertNothingWritten(t, r.home, "whsec_shown_once")

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "add", testAutoID, "-f", file, "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got["signing_secret"] != "whsec_shown_once" || got["webhook_url"] == nil || got["trigger"] == nil {
		t.Errorf("--json: %v %s", err, r.stdout)
	}
	wantAll(t, "stderr", r.stderr, "Store these now")
}

func assertNothingWritten(t *testing.T, home, needle string) {
	t.Helper()
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(needle)) {
				t.Errorf("%s holds %q", path, needle)
			}
		}
		return nil
	})
}

func TestTriggersRotate(t *testing.T) {
	path := "/v1/automations/" + testAutoID + "/triggers/" + triggerUUID + "/rotate"
	for name, tc := range map[string]struct {
		flags []string
		body  string
		note  string
	}{
		"default both":     {nil, `{"rotate":["url","secret"],"grace":true}`, "24 hours"},
		"secret, no grace": {[]string{"--secret", "--no-grace"}, `{"rotate":["secret"],"grace":false}`, "no longer works"},
		"url":              {[]string{"--url"}, `{"rotate":["url"],"grace":true}`, "Store these now"},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, append([]string{"automations", "triggers", "rotate", "PR review api", triggerUUID}, tc.flags...)...)
		if r.code != 0 {
			t.Errorf("%s: exit %d %s", name, r.code, r.msg)
			continue
		}
		if got := fake.sent("POST", path); len(got) != 1 || got[0].Body != tc.body {
			t.Errorf("%s: %v", name, fake.requests())
		}
		wantSecret := 1
		if strings.Join(tc.flags, " ") == "--url" {
			wantSecret = 0
			if strings.Count(r.stdout, "nan_whk_ROTATED") != 1 {
				t.Errorf("%s: no new URL: %q", name, r.stdout)
			}
		}
		if strings.Count(r.stdout, "new_secret_shown_once") != wantSecret || !strings.Contains(r.stderr, tc.note) {
			t.Errorf("%s: stdout %q stderr %q", name, r.stdout, r.stderr)
		}
	}
}

func TestTriggersLsEditRm(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "ls", "PR review api")
	if r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.msg)
	}
	wantAll(t, "triggers ls", r.stdout, triggerUUID, "webhook github", "CHANGED FROM", "Browser")

	file := filepath.Join(t.TempDir(), "t.json")
	_ = os.WriteFile(file, []byte(`{"enabled": false}`), 0o600)
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "edit", "PR review api", triggerUUID, "-f", file)
	path := "/v1/automations/" + testAutoID + "/triggers/" + triggerUUID
	if got := fake.sent("PATCH", path); r.code != 0 || len(got) != 1 || got[0].Body != `{"enabled":false}` || !strings.Contains(r.stdout, "disabled") {
		t.Errorf("edit: exit %d %s %v %q", r.code, r.msg, fake.requests(), r.stdout)
	}

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "rm", "PR review api", triggerUUID, "--yes")
	if r.code != 0 || len(fake.sent("DELETE", path)) != 1 {
		t.Errorf("rm: exit %d %s", r.code, r.msg)
	}

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "rm", "PR review api", "nan_whk_AbC1", "--yes")
	if r.code != exitUsage || len(fake.requests()) != 0 {
		t.Errorf("bad trigger id: exit %d %v", r.code, fake.requests())
	}

	other := "c0000000-0000-4000-8000-000000000009"
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "triggers", "rm", "PR review api", other, "--yes")
	if r.code != exitUsage || !strings.Contains(r.msg, "trigger "+other+" not found") {
		t.Errorf("unknown trigger: exit %d %s", r.code, r.msg)
	}
}

func TestAutomationWritesWhenTheServerSaysNo(t *testing.T) {
	// A session, write API off: one question, no write.
	fake := newFakeWrite()
	fake.writeAPI = false
	r := runWrite(t, fake, writeSetup{session: cliSession}, "automations", "disable", "PR review api")
	if r.code != exitUsage || !strings.Contains(r.msg, "portal") {
		t.Errorf("flag off: exit %d %s", r.code, r.msg)
	}
	if len(fake.sent("PATCH", "/v1/automations/"+testAutoID)) != 0 {
		t.Error("wrote with the write API off")
	}

	// A token on a Phase 2 platform: browser_session_required.
	fake = newFakeWrite()
	fake.failOn("PATCH /v1/automations/"+testAutoID, 403, `{"error":{"message":"needs a browser","type":"permission_error","code":"browser_session_required"}}`)
	r = runWrite(t, fake, writeSetup{env: testPAT}, "automations", "disable", "PR review api")
	if r.code != exitUsage || !strings.Contains(r.msg, "portal") || len(fake.sent("GET", "/api/auth/me")) != 0 {
		t.Errorf("phase 2: exit %d %s", r.code, r.msg)
	}

	// A token without automations:write.
	fake = newFakeWrite()
	fake.failOn("PATCH /v1/automations/"+testAutoID, 403, `{"error":{"message":"This token lacks the \"automations:write\" scope.","type":"permission_error","code":"token_scope_denied"}}`)
	r = runWrite(t, fake, writeSetup{env: testPAT}, "automations", "enable", "PR review api")
	if r.code != exitAuth || !strings.Contains(r.msg, "automations:write") || !strings.Contains(r.msg, "Settings > Tokens") || strings.Contains(r.msg, testPAT) {
		t.Errorf("scope: exit %d %s", r.code, r.msg)
	}
	if got := fake.sent("PATCH", "/v1/automations/"+testAutoID); len(got) != 1 || got[0].Authz != "Bearer "+testPAT {
		t.Errorf("token not sent as a bearer: %v", got)
	}

	// Rate limited.
	fake = newFakeWrite()
	fake.failOn("DELETE /v1/automations/"+testAutoID, 429, `{"error":{"message":"Too many","type":"rate_limit_exceeded","code":"rate_limited"}}`)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "rm", "PR review api", "--yes")
	if r.code != exitUnavailable || !strings.Contains(r.msg, "automations") {
		t.Errorf("rate limited: exit %d %s", r.code, r.msg)
	}

	// An expired session is an auth error, not "not available".
	fake = newFakeWrite()
	fake.failOn("GET /api/auth/me", 401, `{"error":"invalid session"}`)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "automations", "enable", "PR review api")
	if r.code != exitAuth || !strings.Contains(r.msg, "nan auth login") {
		t.Errorf("expired: exit %d %s", r.code, r.msg)
	}
}

func TestShowSaysWhereTheLastChangeCameFrom(t *testing.T) {
	resetPhase2Flags()
	env, out, _ := goldenEnv(t)
	if err := doAutomationsShow(t.Context(), env, testAutoID); err != nil {
		t.Fatal(err)
	}
	wantAll(t, "show", out.String(), "last changed from  Browser", "nan automations edit "+testAutoID)
}

func TestUpdatedByLabel(t *testing.T) {
	for in, want := range map[string]string{"browser_session": "Browser", "cli_session": "CLI", "platform_token": "API token", "": "", "x\x1b": "x"} {
		if got := updatedByLabel(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestVerbHintsPointAtTheCommand(t *testing.T) {
	fakeHome(t, "")
	for args, want := range map[string]string{
		"secrets add":        "nan secrets set",
		"secrets reveal":     "nan secrets show",
		"automations new":    "nan automations create",
		"automations rotate": "nan automations triggers rotate",
	} {
		code, msg := execute(t, strings.Fields(args)...)
		if code != exitUsage || !strings.Contains(msg, want) {
			t.Errorf("nan %s: exit %d: %s", args, code, msg)
		}
	}
}

func apiCreate(name string) api.CreateSecretRequest {
	return api.CreateSecretRequest{Name: name, Value: testValue}
}

// Without the CLI's own retry, the lost answer is an error: the second POST
// of the test above is this CLI's, not net/http's.
func TestSecretsSetWithoutRetryFailsOnALostAnswer(t *testing.T) {
	fake := newFakeWrite()
	fake.dropFirstCreate = true
	file := writeValueFile(t, testValue)
	swap(t, &createAttempts, 1)
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "NEW_TOKEN", "--value-file", file)
	if r.code == 0 || len(fake.sent("POST", "/v1/secrets")) != 1 {
		t.Fatalf("exit %d, posts %v", r.code, fake.sent("POST", "/v1/secrets"))
	}
	noLeak(t, r, fake)
}

func TestLostAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"network":   {fmt.Errorf("could not reach nan.builders: %w", io.ErrUnexpectedEOF), true},
		"refusal":   {&api.APIError{Status: 409, Code: "name_taken"}, false},
		"5xx":       {&api.APIError{Status: 502}, false},
		"expired":   {api.ErrSessionExpired, false},
		"cancelled": {fmt.Errorf("x: %w", context.Canceled), false},
	} {
		if got := lostAnswer(tc.err); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

const readScopeDenied = `{"error":{"message":"This token lacks the \"secrets:read\" scope.","type":"permission_error","code":"token_scope_denied"}}`

// A token with secrets:write and not secrets:read cannot look the name up:
// it still creates, and says what it needs when the name exists.
func TestSecretsSetWithAWriteOnlyToken(t *testing.T) {
	file := writeValueFile(t, testValue)
	fake := newFakeWrite()
	fake.failOn("GET /v1/secrets", 403, readScopeDenied)
	r := runWrite(t, fake, writeSetup{env: testPAT}, "secrets", "set", "NEW_TOKEN", "--value-file", file)
	if r.code != 0 || len(fake.sent("POST", "/v1/secrets")) != 1 {
		t.Fatalf("exit %d %s %v", r.code, r.msg, fake.requests())
	}
	if got := fake.sent("POST", "/v1/secrets")[0]; got.Authz != "Bearer "+testPAT || len(fake.sent("GET", "/api/auth/me")) != 0 {
		t.Errorf("token request %v", fake.requests())
	}

	fake = newFakeWrite()
	fake.failOn("GET /v1/secrets", 403, readScopeDenied)
	fake.failOn("POST /v1/secrets", 409, `{"error":{"message":"taken","type":"invalid_request_error","param":"name","code":"name_taken"}}`)
	r = runWrite(t, fake, writeSetup{env: testPAT}, "secrets", "set", "GITHUB_TOKEN", "--value-file", file)
	if r.code != exitUsage || !strings.Contains(r.msg, "secrets:read") || !strings.Contains(r.msg, "GITHUB_TOKEN already exists") || strings.Contains(r.msg, testPAT) {
		t.Errorf("exit %d %s", r.code, r.msg)
	}
	noLeak(t, r, fake)
}

func TestSecretsSetExistingWithBaseURLAndAFailedPatch(t *testing.T) {
	const path = "/v1/secrets/5e000000-0000-4000-8000-000000000001"
	file := writeValueFile(t, testValue)
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "GITHUB_TOKEN", "--value-file", file, "--base-url", "https://x.example/v1")
	if p := fake.sent("PATCH", path); r.code != 0 || len(p) != 1 || p[0].Body != `{"base_url":"https://x.example/v1"}` {
		t.Errorf("exit %d %s %v", r.code, r.msg, p)
	}

	fake = newFakeWrite()
	fake.failOn("PATCH "+path, 400, `{"error":{"message":"base_url must be an https URL","type":"invalid_request_error","param":"base_url","code":"invalid_request"}}`)
	r = runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "set", "GITHUB_TOKEN", "--value-file", file, "--base-url", "http://x")
	if r.code != exitUsage || !strings.Contains(r.msg, "base_url") || len(fake.sent("PUT", path+"/value")) != 1 {
		t.Errorf("exit %d %s", r.code, r.msg)
	}
	wantAll(t, "stderr", r.stderr, "the value was replaced, but not the description or base URL")
	noLeak(t, r, fake)
}

// A secret deleted between the lookup and the write.
func TestSecretWriteNotFound(t *testing.T) {
	const path = "/v1/secrets/5e000000-0000-4000-8000-000000000001"
	notFound := `{"error":{"message":"Not found","type":"invalid_request_error","code":"not_found"}}`
	file := writeValueFile(t, testValue)
	for name, tc := range map[string]struct {
		route string
		args  []string
	}{
		"set":  {"PUT " + path + "/value", []string{"secrets", "set", "GITHUB_TOKEN", "--value-file", file}},
		"edit": {"PATCH " + path, []string{"secrets", "edit", "GITHUB_TOKEN", "--description", "x"}},
		"rm":   {"DELETE " + path, []string{"secrets", "rm", "GITHUB_TOKEN", "--yes"}},
	} {
		fake := newFakeWrite()
		fake.failOn(tc.route, 404, notFound)
		r := runWrite(t, fake, writeSetup{session: cliSession}, tc.args...)
		if r.code != exitUsage || !strings.Contains(r.msg, "not found") {
			t.Errorf("%s: exit %d %s", name, r.code, r.msg)
		}
	}
}

func TestSecretValueSizeLimit(t *testing.T) {
	swap(t, &writeStdinTTY, func() bool { return false })
	swap(t, &writeStdin, io.Reader(strings.NewReader(strings.Repeat("a", maxSecretValue)+"\n")))
	if v, err := readSecretValue("", true); err != nil || len(v) != maxSecretValue {
		t.Errorf("16 KiB: %d %v", len(v), err)
	}
	writeStdin = strings.NewReader(strings.Repeat("a", maxSecretValue) + "\r\n")
	if v, err := readSecretValue("", true); err != nil || len(v) != maxSecretValue {
		t.Errorf("16 KiB + CRLF: %d %v", len(v), err)
	}
	writeStdin = strings.NewReader(strings.Repeat("a", maxSecretValue+1))
	if _, err := readSecretValue("", true); exitCode(err) != exitUsage || !strings.Contains(err.Error(), "16 KiB") {
		t.Errorf("16 KiB + 1: %v", err)
	}
	writeStdin = strings.NewReader("\xff\xfe")
	if _, err := readSecretValue("", true); exitCode(err) != exitUsage || strings.Contains(err.Error(), "\xff") {
		t.Errorf("not UTF-8: %v", err)
	}
}

func TestPastedValueIsNotEchoedAsACommand(t *testing.T) {
	fakeHome(t, "")
	code, msg := execute(t, "secrets", testValue)
	if code != exitUsage || strings.Contains(msg, "TESTVALUE") || !strings.Contains(msg, "shell history") {
		t.Errorf("exit %d: %s", code, msg)
	}
}

func TestSecretsReadWithTheWriteAPIOff(t *testing.T) {
	fake := newFakeWrite()
	fake.writeAPI = false
	r := runWrite(t, fake, writeSetup{env: testPAT}, "secrets", "ls")
	if r.code != exitUsage || !strings.Contains(r.msg, "reading secrets") {
		t.Errorf("exit %d %s", r.code, r.msg)
	}
}

func TestTriggersJSONAndTokens(t *testing.T) {
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{env: testPAT}, "automations", "triggers", "ls", "PR review api", "--json")
	var list map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &list); r.code != 0 || err != nil || list["data"] == nil {
		t.Errorf("ls --json: exit %d %v %s", r.code, err, r.stdout)
	}

	file := filepath.Join(t.TempDir(), "t.yaml")
	_ = os.WriteFile(file, []byte("kind: webhook\nsignature_scheme: github\n"), 0o600)
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{env: testPAT}, "automations", "triggers", "add", testAutoID, "-f", file)
	if got := fake.sent("POST", "/v1/automations/"+testAutoID+"/triggers"); r.code != 0 || len(got) != 1 || got[0].Authz != "Bearer "+testPAT {
		t.Errorf("add with a token: exit %d %s %v", r.code, r.msg, fake.requests())
	}
	if strings.Count(r.stdout, "whsec_shown_once") != 1 {
		t.Errorf("add with a token: %q", r.stdout)
	}

	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{env: testPAT}, "automations", "triggers", "rotate", testAutoID, triggerUUID, "--secret", "--json")
	var rot map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &rot); r.code != 0 || err != nil || rot["signing_secret"] != "new_secret_shown_once" || rot["webhook_url"] != nil {
		t.Errorf("rotate --json: exit %d %v %s", r.code, err, r.stdout)
	}
	wantAll(t, "stderr", r.stderr, "Store these now")
}

func TestWriteArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"secrets", "show"},
		{"secrets", "rm", "A_TOKEN", "B_TOKEN"},
		{"secrets", "set"},
		{"secrets", "edit", "lower"},
		{"automations", "triggers", "rm", "PR review api"},
		{"automations", "triggers", "rm", "PR review api", triggerUUID, "extra"},
		{"automations", "create", "-f", ""},
		{"automations", "triggers", "nope"},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, writeSetup{session: cliSession}, args...)
		if r.code != exitUsage || len(fake.requests()) != 0 {
			t.Errorf("nan %s: exit %d %s %v", strings.Join(args, " "), r.code, r.msg, fake.requests())
		}
	}
}

func TestWriteTokenFileAndBadEnv(t *testing.T) {
	path := writeTokenFile(t, testPAT+"\n", 0o600)
	fake := newFakeWrite()
	r := runWrite(t, fake, writeSetup{session: cliSession}, "secrets", "ls", "--token-file", path)
	if got := fake.sent("GET", "/v1/secrets"); r.code != 0 || len(got) != 1 || got[0].Authz != "Bearer "+testPAT || got[0].Cookie != "" {
		t.Errorf("--token-file wins over the session: exit %d %s %v", r.code, r.msg, fake.requests())
	}
	fake = newFakeWrite()
	r = runWrite(t, fake, writeSetup{env: "not a token"}, "secrets", "ls")
	if r.code != exitAuth || len(fake.requests()) != 0 || strings.Contains(r.msg, "not a token") {
		t.Errorf("bad NAN_TOKEN: exit %d %s", r.code, r.msg)
	}
}

const testWhsec = "whsec_STRIPEROLLEDSECRET"

// A Stripe trigger's new signing secret comes from Stripe: read from a file
// or a pipe, sent once, never printed or stored by the CLI.
func TestTriggersRotateStripeSigningSecret(t *testing.T) {
	path := "/v1/automations/" + testAutoID + "/triggers/" + triggerUUID + "/rotate"
	file := writeValueFile(t, testWhsec)
	for name, tc := range map[string]struct {
		setup writeSetup
		flags []string
		body  string
	}{
		"file":         {writeSetup{session: cliSession}, []string{"--signing-secret-file", file}, `{"rotate":["secret"],"grace":true,"signing_secret":"` + testWhsec + `"}`},
		"stdin":        {writeSetup{session: cliSession, stdin: testWhsec + "\n"}, []string{"--signing-secret-stdin", "--no-grace"}, `{"rotate":["secret"],"grace":false,"signing_secret":"` + testWhsec + `"}`},
		"url and file": {writeSetup{session: cliSession}, []string{"--url", "--signing-secret-file", file}, `{"rotate":["url","secret"],"grace":true,"signing_secret":"` + testWhsec + `"}`},
	} {
		fake := newFakeWrite()
		r := runWrite(t, fake, tc.setup, append([]string{"automations", "triggers", "rotate", "PR review api", triggerUUID}, tc.flags...)...)
		if r.code != 0 {
			t.Errorf("%s: exit %d %s", name, r.code, r.msg)
			continue
		}
		if got := fake.sent("POST", path); len(got) != 1 || got[0].Body != tc.body {
			t.Errorf("%s: %v", name, fake.requests())
		}
		if !strings.Contains(r.stderr, "from Stripe") {
			t.Errorf("%s: stderr %q", name, r.stderr)
		}
		for what, text := range map[string]string{"stdout": r.stdout, "stderr": r.stderr, "message": r.msg} {
			if strings.Contains(text, testWhsec) {
				t.Errorf("%s: %s echoes the signing secret", name, what)
			}
		}
		assertNothingWritten(t, r.home, testWhsec)
	}
}

func TestTriggersRotateStripeRefusals(t *testing.T) {
	path := "/v1/automations/" + testAutoID + "/triggers/" + triggerUUID + "/rotate"
	file := writeValueFile(t, testWhsec)
	stripe400 := `{"error":{"message":"paste the endpoint's signing secret from the Stripe dashboard (whsec_…)","type":"invalid_request_error","param":"signing_secret","code":"invalid_request"}}`
	generated400 := `{"error":{"message":"NaN generates the signing secret for this scheme","type":"invalid_request_error","param":"signing_secret","code":"invalid_request"}}`
	for name, tc := range map[string]struct {
		setup writeSetup
		flags []string
		fail  string
		sends int
		wants []string
	}{
		"stripe without the secret": {writeSetup{session: cliSession}, nil, stripe400, 1, []string{"Stripe dashboard", "--signing-secret-file", "--url"}},
		"malformed whsec":           {writeSetup{session: cliSession}, []string{"--signing-secret-file", file}, stripe400, 1, []string{"Stripe dashboard again"}},
		"secret on a github hook":   {writeSetup{session: cliSession}, []string{"--signing-secret-file", file}, generated400, 1, []string{"NaN generates", "Stripe"}},
		"both sources":              {writeSetup{session: cliSession}, []string{"--signing-secret-file", file, "--signing-secret-stdin"}, "", 0, []string{"not both"}},
		"terminal":                  {writeSetup{session: cliSession, tty: true}, []string{"--signing-secret-stdin"}, "", 0, []string{"--signing-secret-stdin", "terminal", "--signing-secret-file"}},
		"empty":                     {writeSetup{session: cliSession, stdin: "\n"}, []string{"--signing-secret-stdin"}, "", 0, []string{"empty"}},
	} {
		fake := newFakeWrite()
		if tc.fail != "" {
			fake.failOn("POST "+path, 400, tc.fail)
		}
		r := runWrite(t, fake, tc.setup, append([]string{"automations", "triggers", "rotate", "PR review api", triggerUUID}, tc.flags...)...)
		if r.code != exitUsage {
			t.Errorf("%s: exit %d %s", name, r.code, r.msg)
		}
		for _, w := range tc.wants {
			if !strings.Contains(r.msg, w) {
				t.Errorf("%s: message lacks %q: %s", name, w, r.msg)
			}
		}
		if got := fake.sent("POST", path); len(got) != tc.sends {
			t.Errorf("%s: sent %d rotate requests", name, len(got))
		}
		if strings.Contains(r.stdout+r.stderr+r.msg, testWhsec) {
			t.Errorf("%s: output echoes the signing secret", name)
		}
	}
}

func TestRotateRequestRedactsSigningSecret(t *testing.T) {
	s := testWhsec
	in := api.RotateRequest{Rotate: []string{"secret"}, Grace: true, SigningSecret: &s}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(f, in); strings.Contains(out, testWhsec) {
			t.Errorf("%s prints the signing secret: %s", f, out)
		}
	}
}

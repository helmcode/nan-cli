package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
)

// nan context (helmcode/nan-devops AGENT-CONTEXT.md §6, C1 test list),
// against fakeCtx: the shapes and codes of cloud-api's /v1/contexts.

// writeTree creates files (path -> content) under a new temp folder. A path
// ending in ".sh" is made executable.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const skillMD = "---\nname: pr-review\ndescription: Review a pull request.\n---\nSteps.\n"

func goodTree() map[string]string {
	return map[string]string{
		"AGENTS.md":                       "Use tabs.\n",
		"skills/pr-review/SKILL.md":       skillMD,
		"skills/pr-review/scripts/run.sh": "#!/bin/sh\necho ok\n",
		"files/especificación técnica.md": "Spec.\n",
	}
}

func publishBody(t *testing.T, r ctxReq) api.PublishRequest {
	t.Helper()
	var in api.PublishRequest
	if err := json.Unmarshal([]byte(r.Body), &in); err != nil {
		t.Fatalf("publish body %q: %v", r.Body, err)
	}
	return in
}

func publishPaths(in api.PublishRequest) []string {
	var out []string
	for _, f := range in.Files {
		out = append(out, f.Path)
	}
	return out
}

// ─── ls, show, create, edit, rm ─────────────────────────────────────────────

func TestContextLsAndShowGoldens(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 3, map[string]string{"AGENTS.md": "Use tabs.\n", "skills/pr-review/SKILL.md": skillMD, "files/spec.md": "Spec.\n"})

	r := runCtx(t, fake, ctxSetup{}, "context", "ls")
	mustCtxExit(t, r, 0)
	checkGolden(t, "contexts/ls.golden", r.stdout)

	r = runCtx(t, fake, ctxSetup{}, "context", "show", "acme-api")
	mustCtxExit(t, r, 0)
	checkGolden(t, "contexts/show.golden", r.stdout)

	r = runCtx(t, fake, ctxSetup{}, "context", "show", "acme-api", "--json")
	mustCtxExit(t, r, 0)
	var d api.ContextDetail
	if err := json.Unmarshal([]byte(r.stdout), &d); err != nil || d.Version != 3 || len(d.Files) != 3 {
		t.Errorf("%v %s", err, r.stdout)
	}

	r = runCtx(t, fake, ctxSetup{}, "context", "show", "nope")
	mustCtxExit(t, r, exitUsage, "no such context")
}

func TestContextSurfaceOff(t *testing.T) {
	fake := newFakeCtx()
	fake.off = true
	r := runCtx(t, fake, ctxSetup{}, "context", "ls")
	mustCtxExit(t, r, exitUsage, "not available for your account yet", "portal")
}

func TestContextCreateValidatesTheName(t *testing.T) {
	for _, name := range []string{"Acme", "con", "blobs", "a_b", "123e4567-e89b-12d3-a456-426614174000"} {
		fake := newFakeCtx()
		r := runCtx(t, fake, ctxSetup{}, "context", "create", name)
		mustCtxExit(t, r, exitUsage, "not a valid context name")
		if len(fake.requests()) != 0 {
			t.Errorf("%s: sent %v", name, fake.requests())
		}
	}
}

func TestContextCreateFromAFolder(t *testing.T) {
	fake := newFakeCtx()
	dir := writeTree(t, goodTree())
	r := runCtx(t, fake, ctxSetup{}, "context", "create", "acme-api", "--description", "Acme", "--from", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "version 2 published", "4 files")
	creates := fake.sent("POST", "/v1/contexts")
	if len(creates) < 1 || !idemKeyPattern.MatchString(creates[0].Idem) || !strings.Contains(creates[0].Body, `"description":"Acme"`) {
		t.Errorf("create %+v", creates)
	}
	pubs := fake.sent("POST", "/v1/contexts/"+fake.contexts[0].ID+"/versions")
	if len(pubs) != 1 || publishBody(t, pubs[0]).BaseVersion != 1 {
		t.Fatalf("publishes %+v", pubs)
	}

	// A folder that cannot be pushed leaves no empty context behind.
	fake = newFakeCtx()
	bad := writeTree(t, map[string]string{"CLAUDE.md": "x"})
	r = runCtx(t, fake, ctxSetup{}, "context", "create", "acme-api", "--from", bad)
	mustCtxExit(t, r, exitDataErr)
	if len(fake.requests()) != 0 {
		t.Errorf("sent %v", fake.requests())
	}
}

func TestContextEditAndRm(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 2, nil)
	r := runCtx(t, fake, ctxSetup{}, "context", "edit", "acme-api", "--name", "acme-web", "--description", "")
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "context acme-web updated")
	p := fake.sent("PATCH", "/v1/contexts/"+fakeCtxID)
	if len(p) != 1 || p[0].Body != `{"name":"acme-web","description":""}` {
		t.Errorf("patch %+v", p)
	}
	mustCtxExit(t, runCtx(t, fake, ctxSetup{}, "context", "edit", "acme-web"), exitUsage, "nothing to change")
	mustCtxExit(t, runCtx(t, fake, ctxSetup{}, "context", "edit", "acme-web", "--name", "Bad Name"), exitUsage, "not a valid context name")

	// rm asks, and without a terminal needs --yes.
	mustCtxExit(t, runCtx(t, fake, ctxSetup{}, "context", "rm", "acme-web"), exitUsage, "--yes")
	fake.queue("DELETE /v1/contexts/"+fakeCtxID, fakeResp{status: 409, body: map[string]any{
		"error":       map[string]any{"message": "Automations use this context; remove it from them first.", "type": "invalid_request_error", "param": nil, "code": "context_in_use"},
		"automations": []any{map[string]any{"id": testAutoID, "name": "PR review api"}}}})
	r = runCtx(t, fake, ctxSetup{}, "context", "rm", "acme-web", "--yes")
	mustCtxExit(t, r, exitUsage, "automations use this context", "PR review api")
	r = runCtx(t, fake, ctxSetup{}, "context", "rm", "acme-web", "--yes")
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "context acme-web deleted")
	if len(fake.contexts) != 0 {
		t.Error("not deleted")
	}
}

// ─── push: local checks ─────────────────────────────────────────────────────

func TestContextPushLayoutChecks(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := writeTree(t, map[string]string{
		"CLAUDE.md":                  "Use tabs.\n",
		"README.md":                  "top level\n",
		"notes/a.md":                 "a\n",
		"skills/no-skill-md/x.md":    "x\n",
		"skills/Bad Skill/SKILL.md":  skillMD,
		"files/rocket\U0001F680.md":  "emoji\n",
		"files/con.txt":              "reserved\n",
		"files/ok.md":                "ok\n",
		"files/.env":                 "SECRET=1\n",
		".git/config":                "[core]\n",
		"files/sub/.hidden/inner.md": "hidden\n",
	})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr, "cannot be published", "nothing was uploaded", "--skip-invalid")
	checkGolden(t, "contexts/push_refused.stderr.golden", r.stderr)
	if n := len(fake.requests()); n != 0 {
		t.Errorf("a refused push sent %d requests: %v", n, fake.requests())
	}
}

func TestContextPushSkipInvalid(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := writeTree(t, map[string]string{"CLAUDE.md": "x\n", "files/ok.md": "ok\n", "files/a:b.md": "colon\n",
		"skills/no-skill-md/x.md": "x\n", "files/.env": "S=1\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--skip-invalid")
	mustCtxExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "skipped CLAUDE.md: bad_top_level (rename it to AGENTS.md", "skipped files/a:b.md: bad_character",
		"skipped skills/no-skill-md/SKILL.md: skill_invalid", "skipped files/.env (dotfiles")
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if len(pubs) != 1 {
		t.Fatalf("publishes %v", fake.requests())
	}
	if got := publishPaths(publishBody(t, pubs[0])); strings.Join(got, ",") != "files/ok.md" {
		t.Errorf("published %v", got)
	}
}

// macOS hands out decomposed (NFD) names; the platform refuses non-NFC
// paths, so push normalises before hashing and sending.
func TestContextPushNormalisesNFD(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	nfd := "files/especificación.md"
	dir := writeTree(t, map[string]string{nfd: "spec\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if len(pubs) != 1 {
		t.Fatalf("%v", fake.requests())
	}
	if got := publishPaths(publishBody(t, pubs[0])); len(got) != 1 || got[0] != "files/especificación.md" {
		t.Errorf("published %q, want the NFC form", got)
	}

	// Two names that are the same once normalised collide (a file system
	// that is not normalisation-insensitive can hold both).
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return
	}
	dir = writeTree(t, map[string]string{nfd: "a\n", "files/especificación.md": "b\n"})
	r = runCtx(t, newFakeCtx(), ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr)
	wantAll(t, "stderr", r.stderr, "case_collision")
}

func TestContextPushRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := writeTree(t, map[string]string{"files/a.md": "a\n"})
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "files", "passwd")); err != nil {
		t.Fatal(err)
	}
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr)
	wantAll(t, "stderr", r.stderr, "refused files/passwd: symlink")
}

func TestContextPushLimits(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := writeTree(t, map[string]string{"AGENTS.md": strings.Repeat("a", 32<<10+1)})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr)
	wantAll(t, "stderr", r.stderr, "refused AGENTS.md: too_large (AGENTS.md is at most 32 KiB)")
}

// ─── push: uploads and publish ──────────────────────────────────────────────

func TestContextPushDryRun(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 3, map[string]string{"AGENTS.md": "Old rules.\n", "files/old.md": "old\n", "files/same.md": "same\n"})
	dir := writeTree(t, map[string]string{"AGENTS.md": "Use tabs.\n", "files/same.md": "same\n", "files/new.md": "new file\n",
		"skills/pr-review/SKILL.md": skillMD})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--dry-run")
	mustCtxExit(t, r, 0)
	checkGolden(t, "contexts/push_dry_run.golden", r.stdout)

	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--dry-run", "--json")
	mustCtxExit(t, r, 0)
	checkGolden(t, "contexts/push_dry_run.json.golden", r.stdout)

	for _, req := range fake.requests() {
		if req.Method != http.MethodGet && !(req.Method == http.MethodPost && req.Path == "/v1/contexts/blobs/check") {
			t.Errorf("a dry run sent %s %s", req.Method, req.Path)
		}
	}
}

func TestContextPushUploadsOnlyMissingBlobs(t *testing.T) {
	fake := newFakeCtx()
	c := fake.seed("acme-api", 2, map[string]string{"files/kept.md": "kept\n"})
	tree := goodTree()
	tree["files/kept.md"] = "kept\n"
	tree["files/copy-of-agents.md"] = tree["AGENTS.md"] // same blob, two paths
	dir := writeTree(t, tree)
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	checkGolden(t, "contexts/push.golden", r.stdout)

	puts := fake.sent("PUT", "/v1/contexts/blobs/")
	if len(puts) != 4 { // AGENTS.md (once for two paths), SKILL.md, run.sh, the spec
		t.Errorf("uploaded %d blobs, want 4: %+v", len(puts), puts)
	}
	for _, p := range puts {
		if p.Path == "/v1/contexts/blobs/"+shaOf([]byte("kept\n")) {
			t.Error("uploaded a blob the platform already had")
		}
		if p.Filename == "" || strings.Contains(p.Filename, "/") {
			t.Errorf("X-Nan-Filename %q: the base name is the type hint", p.Filename)
		}
	}
	pubs := fake.sent("POST", "/v1/contexts/"+c.ID+"/versions")
	if len(pubs) != 1 {
		t.Fatalf("publishes %d", len(pubs))
	}
	in := publishBody(t, pubs[0])
	if in.BaseVersion != 2 || in.AllowSecretLike || len(in.Files) != 6 || !idemKeyPattern.MatchString(pubs[0].Idem) {
		t.Errorf("publish %+v idem %q", in, pubs[0].Idem)
	}
	for _, f := range in.Files {
		if f.Executable != (f.Path == "skills/pr-review/scripts/run.sh") && runtime.GOOS != "windows" {
			t.Errorf("%s executable=%v", f.Path, f.Executable)
		}
	}

	// Pushing the same folder again changes nothing.
	n := len(fake.requests())
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "is up to date (version 3)")
	if extra := fake.requests()[n:]; len(extra) != 1 || extra[0].Method != http.MethodGet {
		t.Errorf("an up-to-date push sent %v", extra)
	}
}

func TestContextPushCreate(t *testing.T) {
	fake := newFakeCtx()
	dir := writeTree(t, map[string]string{"files/a.md": "a\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitUsage, "nan context create acme-api", "--create")
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--create")
	mustCtxExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "context acme-api created")
	if len(fake.contexts) != 1 || fake.contexts[0].Version != 2 {
		t.Errorf("%+v", fake.contexts)
	}
}

// A blob over the single-PUT limit goes up in parts; a part that fails is
// resumed from where the platform says the upload stands.
func TestContextPushMultipartResumesAfterAFailedPart(t *testing.T) {
	swap(t, &ctxSingleMax, int64(8))
	fake := newFakeCtx()
	fake.partSize = 4
	fake.failPart[2] = true
	fake.seed("acme-api", 1, nil)
	big := "0123456789" // 3 parts of 4, 4, 2 bytes
	dir := writeTree(t, map[string]string{"files/big.csv": big, "files/small.md": "small\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "part 2 of files/big.csv did not go through; resuming at part 2")
	sha := shaOf([]byte(big))
	var parts []string
	for _, q := range fake.sent("PUT", "/v1/contexts/blobs/"+sha+"/uploads/") {
		parts = append(parts, q.Path[strings.LastIndex(q.Path, "/")+1:])
	}
	if strings.Join(parts, ",") != "1,2,2,3" {
		t.Errorf("parts sent %v", parts)
	}
	if string(fake.blobs[sha]) != big {
		t.Errorf("stored %q", fake.blobs[sha])
	}
	if starts := fake.sent("POST", "/v1/contexts/blobs/"+sha+"/uploads"); len(starts) != 2 { // start + complete
		t.Errorf("start/complete %v", starts)
	}
	if entries, _ := os.ReadDir(r.stateDir); len(entries) != 0 {
		t.Errorf("upload state left behind: %v", entries)
	}
}

// An upload a previous push left unfinished is resumed: no new upload, the
// stored parts are not sent again.
func TestContextPushMultipartResumesAcrossRuns(t *testing.T) {
	swap(t, &ctxSingleMax, int64(8))
	fake := newFakeCtx()
	fake.partSize = 4
	fake.seed("acme-api", 1, nil)
	big := "0123456789"
	sha := shaOf([]byte(big))
	const id = "u0000000-0000-4000-8000-000000000042"
	fake.uploads[id] = &fakeUpload{sha: sha, size: int64(len(big)), partSize: 4, next: 2, data: []byte("0123")}
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, sha+".json"), []byte(`{"upload_id":"`+id+`","size":10}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := writeTree(t, map[string]string{"files/big.csv": big})
	r := runCtx(t, fake, ctxSetup{stateDir: state}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "resuming the upload of files/big.csv at part 2 of 3")
	if starts := fake.sent("POST", "/v1/contexts/blobs/"+sha+"/uploads"); len(starts) != 1 || !strings.HasSuffix(starts[0].Path, "/complete") {
		t.Errorf("a new upload was started: %v", starts)
	}
	var parts []string
	for _, q := range fake.sent("PUT", "/v1/contexts/blobs/"+sha+"/uploads/"+id+"/parts/") {
		parts = append(parts, q.Path[strings.LastIndex(q.Path, "/")+1:])
	}
	if strings.Join(parts, ",") != "2,3" {
		t.Errorf("parts sent %v", parts)
	}
	if string(fake.blobs[sha]) != big {
		t.Errorf("stored %q", fake.blobs[sha])
	}

	// A remembered upload the platform no longer has starts over.
	fake2 := newFakeCtx()
	fake2.partSize = 4
	fake2.seed("acme-api", 1, nil)
	if err := os.WriteFile(filepath.Join(state, sha+".json"), []byte(`{"upload_id":"`+id+`","size":10}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCtx(t, fake2, ctxSetup{stateDir: state}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	if string(fake2.blobs[sha]) != big {
		t.Errorf("stored %q", fake2.blobs[sha])
	}
}

func TestContextPushVersionConflict(t *testing.T) {
	newFake := func() *fakeCtx {
		fake := newFakeCtx()
		fake.seed("acme-api", 2, map[string]string{"files/a.md": "a\n"})
		// Someone publishes version 3 between this push's read and its
		// publish.
		fake.onPublish = func(f *fakeCtx, n int) {
			if n == 1 {
				c := f.contexts[0]
				c.Version = 3
				c.Files = append(c.Files, api.ContextFile{Path: "files/theirs.md", SHA256: shaOf([]byte("a\n")), Size: 2})
			}
		}
		return fake
	}
	dir := writeTree(t, map[string]string{"files/a.md": "a\n", "files/mine.md": "mine\n"})

	fake := newFake()
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitFailed, "changed while pushing", "now at version 3", "based on version 2", "--force")
	if fake.contexts[0].Version != 3 {
		t.Error("published anyway")
	}

	fake = newFake()
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--force")
	mustCtxExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "changed to version 3 meanwhile", "- files/theirs.md")
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if len(pubs) != 2 || publishBody(t, pubs[1]).BaseVersion != 3 || pubs[0].Idem == pubs[1].Idem {
		t.Errorf("publishes %+v", pubs)
	}
	if fake.contexts[0].Version != 4 {
		t.Errorf("version %d", fake.contexts[0].Version)
	}
}

func TestContextPushSecretLikeContent(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := writeTree(t, map[string]string{"files/setup.md": "token: " + fakeSecretPat + "abcdef\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr, "look like they hold credentials", "nan secrets", "--allow-secrets")
	wantAll(t, "stderr", r.stderr, "files/setup.md:1 looks like github_token")
	if strings.Contains(r.stderr+r.stdout+r.msg, "abcdef") {
		t.Error("the secret was printed")
	}

	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--allow-secrets")
	mustCtxExit(t, r, 0)
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if !publishBody(t, pubs[len(pubs)-1]).AllowSecretLike {
		t.Error("allow_secret_like not sent")
	}
}

// The platform's cleanup can remove an unreferenced blob between the check
// and the publish: push checks again, uploads it again and publishes.
func TestContextPushBlobMissingReuploads(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := writeTree(t, map[string]string{"files/a.md": "a\n", "files/b.md": "b\n"})
	gone := shaOf([]byte("b\n"))
	fake.onPublish = func(f *fakeCtx, n int) {
		if n == 1 {
			delete(f.blobs, gone)
		}
	}
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stderr", r.stderr, "1 file is no longer stored on nan.builders; uploading again")
	if puts := fake.sent("PUT", "/v1/contexts/blobs/"+gone); len(puts) != 2 {
		t.Errorf("uploads of the missing blob: %d", len(puts))
	}
	if pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions"); len(pubs) != 2 {
		t.Errorf("publishes %d", len(pubs))
	}
}

func TestContextPushWaitsOutRetryAfter(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	fake.queue("POST /v1/contexts/"+fakeCtxID+"/versions", fakeResp{status: 429, header: map[string]string{"Retry-After": "7"},
		body: map[string]any{"error": map[string]any{"message": "Publish limit reached (120 attempts per hour); try again later.",
			"type": "rate_limit_exceeded", "param": nil, "code": "rate_limited"}}})
	dir := writeTree(t, map[string]string{"files/a.md": "a\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	if len(r.sleeps) != 1 || r.sleeps[0] != 7*time.Second {
		t.Errorf("sleeps %v", r.sleeps)
	}
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if len(pubs) != 2 || pubs[0].Idem != pubs[1].Idem {
		t.Errorf("a retried publish keeps its Idempotency-Key: %+v", pubs)
	}

	// A wait longer than a push waits by itself is reported, not slept.
	fake = newFakeCtx()
	fake.seed("acme-api", 1, nil)
	fake.queue("POST /v1/contexts/"+fakeCtxID+"/versions", fakeResp{status: 429, header: map[string]string{"Retry-After": "3600"},
		body: map[string]any{"error": map[string]any{"message": "Publish limit reached.", "type": "rate_limit_exceeded", "param": nil, "code": "rate_limited"}}})
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitUnavailable, "try again in 1h0m0s")
	if len(r.sleeps) != 0 {
		t.Errorf("slept %v", r.sleeps)
	}
}

func TestContextPushServerRefusalsArePerFile(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	fake.queue("POST /v1/contexts/"+fakeCtxID+"/versions", fakeResp{status: 415, body: map[string]any{
		"error": map[string]any{"message": "Some files are not allowed at their path.", "type": "invalid_request_error", "param": nil, "code": "unsupported_file_type"},
		"files": []any{map[string]any{"path": "files/a.md", "reason": "unsupported_file_type"}}}})
	dir := writeTree(t, map[string]string{"files/a.md": "a\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr, "not allowed at their path")
	wantAll(t, "stderr", r.stderr, "files/a.md: unsupported_file_type")

	fake = newFakeCtx()
	fake.seed("acme-api", 1, nil)
	fake.queue("POST /v1/contexts/blobs/check", fakeResp{status: 413, body: map[string]any{
		"error": map[string]any{"message": "This upload does not fit in your plan's context storage.", "type": "invalid_request_error", "param": nil, "code": "context_quota_exceeded"},
		"used":  500 << 20, "quota": 500 << 20}})
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr, "500.0 MiB of 500.0 MiB used")
}

// ─── pull ───────────────────────────────────────────────────────────────────

func TestContextPullWritesVerifiedFiles(t *testing.T) {
	fake := newFakeCtx()
	files := goodTree()
	fake.seed("acme-api", 3, files)
	dir := filepath.Join(t.TempDir(), "out")
	r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir)
	mustCtxExit(t, r, 0)
	checkGolden(t, "contexts/pull.golden", strings.ReplaceAll(r.stdout, dir, "DIR"))
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", p, got, err)
		}
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "skills", "pr-review", "scripts", "run.sh"))
		if st == nil || st.Mode().Perm()&0o100 == 0 {
			t.Errorf("run.sh is not executable: %v", st)
		}
	}
	for _, q := range fake.sent("GET", "/v1/contexts/"+fakeCtxID+"/file") {
		if !strings.Contains(q.Query, "version=3") {
			t.Errorf("a download is not pinned to the version read: %s", q.Query)
		}
	}

	// Round trip: what was pulled pushes as no change.
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "is up to date (version 3)")
}

func TestContextPullRefusesANonEmptyFolder(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, map[string]string{"files/a.md": "new\n"})
	dir := writeTree(t, map[string]string{"files/a.md": "mine\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir)
	mustCtxExit(t, r, exitUsage, "is not empty", "--force")
	if len(fake.requests()) != 0 {
		t.Errorf("sent %v", fake.requests())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "files", "a.md")); string(b) != "mine\n" {
		t.Error("overwrote a file")
	}
	r = runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir, "--force")
	mustCtxExit(t, r, 0)
	if b, _ := os.ReadFile(filepath.Join(dir, "files", "a.md")); string(b) != "new\n" {
		t.Errorf("--force did not write: %q", b)
	}
}

// A platform (or anything answering in its place) that sends a path the
// validator refuses gets nothing written, inside or outside DIR.
func TestContextPullRefusesHostilePaths(t *testing.T) {
	for name, paths := range map[string][]string{
		"dotdot":         {"files/../../evil.md"},
		"absolute":       {"/tmp/evil.md"},
		"top dotdot":     {"../evil.md"},
		"backslash":      {"files\\..\\..\\evil.md"},
		"case collision": {"files/Readme.md", "files/README.md"},
		"file vs folder": {"files/docs", "files/Docs/a.md"},
		"reserved":       {"files/CON"},
		"reserved ext":   {"files/nul.txt"},
		"dotfile":        {"files/.bashrc"},
		"not nfc":        {"files/especificación.md"},
		"bad top":        {"evil.md"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeCtx()
			c := fake.seed("acme-api", 1, nil)
			for _, p := range paths {
				fake.blobs[shaOf([]byte("x"))] = []byte("x")
				c.Files = append(c.Files, api.ContextFile{Path: p, SHA256: shaOf([]byte("x")), Size: 1})
			}
			parent := t.TempDir()
			dir := filepath.Join(parent, "out")
			r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir)
			mustCtxExit(t, r, exitDataErr, "nothing was written")
			if len(fake.sent("GET", "/v1/contexts/"+fakeCtxID+"/file")) != 0 {
				t.Error("downloaded a file")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Errorf("wrote %v", entries)
			}
		})
	}
}

func TestContextPullRefusesBadBytes(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 1, map[string]string{"files/a.md": "good\n", "files/b.md": "fine\n"})
	fake.corrupt["files/b.md"] = true
	dir := filepath.Join(t.TempDir(), "out")
	r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir)
	mustCtxExit(t, r, exitUnavailable, "does not match its sha256")
	if _, err := os.Stat(filepath.Join(dir, "files", "b.md")); !os.IsNotExist(err) {
		t.Errorf("kept a bad file: %v", err)
	}
}

func TestContextHelp(t *testing.T) {
	var all strings.Builder
	all.WriteString(contextCmd.Long)
	for _, c := range contextCmd.Commands() {
		all.WriteString(c.Long + c.Short + c.Example)
		if c.InheritedFlags().Lookup("token-file") == nil {
			t.Errorf("nan context %s has no --token-file", c.Name())
		}
	}
	for _, want := range []string{"contexts:read", "contexts:write", contextsDocs, "AGENTS.md", "--allow-secrets", "--skip-invalid", "65"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("nan context help lacks %q", want)
		}
	}
	if strings.Contains(all.String(), "—") {
		t.Error("an em-dash in the context help")
	}
	for verb, want := range map[string]string{"list": "ls", "upload": "push", "delete": "rm", "frobnicate": "unknown command"} {
		r := runCtx(t, newFakeCtx(), ctxSetup{}, "context", verb)
		mustCtxExit(t, r, exitUsage, want)
	}
}

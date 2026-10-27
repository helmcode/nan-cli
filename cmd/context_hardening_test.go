package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nxssie/nan-cli/internal/api"
)

// The review findings of #62 (QA and Security), each as a test first.

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks and Unix modes")
	}
}

// A DIR that is itself a symlink is the folder the member named: push walks
// what it points at instead of publishing an empty tree.
func TestContextPushThroughASymlinkedFolder(t *testing.T) {
	skipOnWindows(t)
	fake := newFakeCtx()
	fake.seed("acme-api", 2, map[string]string{"files/a.md": "a\n"})
	real := writeTree(t, map[string]string{"files/a.md": "a\n", "files/b.md": "b\n"})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", link)
	mustCtxExit(t, r, 0)
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if len(pubs) != 1 || strings.Join(publishPaths(publishBody(t, pubs[0])), ",") != "files/a.md,files/b.md" {
		t.Errorf("published %+v", pubs)
	}
}

// An empty folder never silently empties a context.
func TestContextPushRefusesEmptyingAContext(t *testing.T) {
	fake := newFakeCtx()
	fake.seed("acme-api", 2, map[string]string{"files/a.md": "a\n"})
	empty := t.TempDir()
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", empty)
	mustCtxExit(t, r, exitUsage, "no files to publish", "remove all 1 file", "--allow-empty")
	if len(fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")) != 0 {
		t.Fatal("published an empty tree")
	}
	// Only dotfiles left is the same thing.
	dotted := writeTree(t, map[string]string{".env": "S=1\n"})
	mustCtxExit(t, runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dotted), exitUsage, "--allow-empty")

	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", empty, "--allow-empty")
	mustCtxExit(t, r, 0)
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	if len(pubs) != 1 || len(publishBody(t, pubs[0]).Files) != 0 || fake.contexts[0].Version != 3 {
		t.Errorf("publishes %+v", pubs)
	}
	// An empty context takes an empty folder without the flag.
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", empty)
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "is up to date")
}

// Writes go through os.OpenRoot: a link already inside DIR never leads a
// pull outside it, and a link at a file's path is never replaced.
func TestContextPullNeverWritesThroughALink(t *testing.T) {
	skipOnWindows(t)
	t.Run("files is a link", func(t *testing.T) {
		fake := newFakeCtx()
		fake.seed("acme-api", 1, map[string]string{"files/a.md": "a\n"})
		outside := t.TempDir()
		dir := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "files")); err != nil {
			t.Fatal(err)
		}
		r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir, "--force")
		mustCtxExit(t, r, exitFailed)
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("wrote outside DIR: %v", entries)
		}
	})
	t.Run("files/a.md is a link", func(t *testing.T) {
		fake := newFakeCtx()
		fake.seed("acme-api", 1, map[string]string{"files/a.md": "a\n"})
		outside := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "files", "a.md")); err != nil {
			t.Fatal(err)
		}
		r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir, "--force")
		mustCtxExit(t, r, exitFailed, "link")
		if b, _ := os.ReadFile(outside); string(b) != "keep\n" {
			t.Errorf("wrote through the link: %q", b)
		}
		if st, err := os.Lstat(filepath.Join(dir, "files", "a.md")); err != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Errorf("replaced the link: %v", err)
		}
	})
}

// The limits of one version, checked before anything is hashed or sent.
func TestContextPushTreeLimits(t *testing.T) {
	sparse := func(t *testing.T, dir, rel string, size int64) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	for name, tc := range map[string]struct {
		build func(t *testing.T, dir string)
		want  string
	}{
		"31 skills": {func(t *testing.T, dir string) {
			for i := 0; i < 31; i++ {
				sparse(t, dir, fmt.Sprintf("skills/s%d/SKILL.md", i), 10)
			}
		}, "31 skills: a context holds at most 30"},
		"1001 files": {func(t *testing.T, dir string) {
			for i := 0; i < 1001; i++ {
				sparse(t, dir, fmt.Sprintf("files/f%d.md", i), 0)
			}
		}, "1001 files: a context version holds at most 1000"},
		"250 MiB": {func(t *testing.T, dir string) {
			for i := 0; i < 3; i++ {
				sparse(t, dir, fmt.Sprintf("files/big%d.bin", i), 90<<20)
			}
		}, "270.0 MiB in total: a context version holds at most 250 MiB"},
		"100 MiB file": {func(t *testing.T, dir string) {
			sparse(t, dir, "files/huge.bin", 100<<20+1)
		}, ""},
		"64 KiB SKILL.md": {func(t *testing.T, dir string) {
			sparse(t, dir, "skills/x/SKILL.md", 64<<10+1)
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeCtx()
			fake.seed("acme-api", 1, nil)
			dir := t.TempDir()
			tc.build(t, dir)
			r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
			mustCtxExit(t, r, exitDataErr, tc.want)
			switch name {
			case "100 MiB file":
				wantAll(t, "stderr", r.stderr, "refused files/huge.bin: too_large (a file is at most 100 MiB)")
			case "64 KiB SKILL.md":
				wantAll(t, "stderr", r.stderr, "refused skills/x/SKILL.md: skill_invalid (SKILL.md is at most 64 KiB)")
			}
			if len(fake.requests()) != 0 {
				t.Errorf("sent %d requests", len(fake.requests()))
			}
		})
	}
	// At the limits exactly, nothing is refused locally.
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		sparse(t, dir, fmt.Sprintf("skills/s%d/SKILL.md", i), 0)
	}
	sparse(t, dir, "skills/s0/SKILL.md", 64<<10)
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir, "--dry-run")
	mustCtxExit(t, r, 0)
}

// A remembered upload the platform describes in a way this CLI cannot
// continue is aborted (its marker would refuse a new one), and a new one
// is started.
func TestContextPushAbortsAnUnusableRememberedUpload(t *testing.T) {
	swap(t, &ctxSingleMax, int64(8))
	fake := newFakeCtx()
	fake.partSize = 4
	fake.seed("acme-api", 1, nil)
	big := "0123456789"
	sha := shaOf([]byte(big))
	const id = "u0000000-0000-4000-8000-000000000042"
	fake.uploads[id] = &fakeUpload{sha: sha, size: 10, partSize: 32 << 20, next: 1}
	state := t.TempDir()
	writeState(t, fake.stateFile(t, state, sha), id, 10)
	dir := writeTree(t, map[string]string{"files/big.csv": big})
	r := runCtx(t, fake, ctxSetup{stateDir: state}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	if dels := fake.sent("DELETE", "/v1/contexts/blobs/"+sha+"/uploads/"+id); len(dels) != 1 {
		t.Errorf("abort not sent: %v", fake.requests())
	}
	if _, err := os.Stat(fake.stateFile(t, state, sha)); !os.IsNotExist(err) {
		t.Errorf("state file left: %v", err)
	}
	if string(fake.blobs[sha]) != big {
		t.Errorf("stored %q", fake.blobs[sha])
	}
}

// An upload the platform dropped mid-way (expired, or gone) is forgotten:
// the next push starts a new one instead of asking for it again.
func TestContextPushForgetsAnUploadThePlatformDropped(t *testing.T) {
	for code, status := range map[string]int{"upload_not_found": 404, "upload_expired": 409} {
		t.Run(code, func(t *testing.T) {
			swap(t, &ctxSingleMax, int64(8))
			fake := newFakeCtx()
			fake.partSize = 4
			fake.seed("acme-api", 1, nil)
			big := "0123456789"
			sha := shaOf([]byte(big))
			const id = "u0000000-0000-4000-8000-000000000001" // the fake's first upload id
			fake.queue("PUT /v1/contexts/blobs/"+sha+"/uploads/"+id+"/parts/2", fakeResp{status: status, body: map[string]any{
				"error": map[string]any{"message": "The upload expired before it finished; upload the file again.", "type": "invalid_request_error", "param": nil, "code": code}}})
			state := t.TempDir()
			dir := writeTree(t, map[string]string{"files/big.csv": big})
			r := runCtx(t, fake, ctxSetup{stateDir: state}, "context", "push", "acme-api", dir)
			if r.code == 0 {
				t.Fatalf("push succeeded: %s", r.stdout)
			}
			wantAll(t, "message", r.msg, "upload of files/big.csv failed")
			if _, err := os.Stat(fake.stateFile(t, state, sha)); !os.IsNotExist(err) {
				t.Errorf("state file left: %v", err)
			}
		})
	}
}

func TestContextExitMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
		want   string
	}{
		{403, "entitlement_required", exitAuth, "membership does not include agent runs"},
		{503, "storage_unavailable", exitUnavailable, "unavailable"},
		{409, "context_name_taken", exitUsage, "already have a context"},
		{409, "too_many_contexts", exitDataErr, "at most 20"},
	} {
		fake := newFakeCtx()
		msg := "Context storage is unavailable right now; try again later."
		if tc.code == "too_many_contexts" {
			msg = "You can keep at most 20 contexts."
		}
		// Retryable refusals are retried (3 times): queue enough of them.
		for i := 0; i < 4; i++ {
			fake.queue("POST /v1/contexts", fakeResp{status: tc.status, body: map[string]any{
				"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": nil, "code": tc.code}}})
		}
		r := runCtx(t, fake, ctxSetup{}, "context", "create", "acme-api")
		mustCtxExit(t, r, tc.exit, tc.want)
	}
}

// Where the file system has no executable bit, an unchanged file keeps the
// bit of the version it replaces.
func TestContextPushKeepsTheRemoteExecutableBit(t *testing.T) {
	swap(t, &ctxExecKnown, false)
	fake := newFakeCtx()
	fake.seed("acme-api", 2, map[string]string{"skills/x/SKILL.md": skillMD, "skills/x/run.sh": "#!/bin/sh\n"})
	dir := writeTree(t, map[string]string{"skills/x/SKILL.md": skillMD, "skills/x/run.sh": "#!/bin/sh\n"})
	r := runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	wantAll(t, "stdout", r.stdout, "is up to date (version 2)")

	// A changed file has no remote bit to inherit.
	dir = writeTree(t, map[string]string{"skills/x/SKILL.md": skillMD, "skills/x/run.sh": "#!/bin/sh\necho 2\n"})
	r = runCtx(t, fake, ctxSetup{}, "context", "push", "acme-api", dir)
	mustCtxExit(t, r, 0)
	pubs := fake.sent("POST", "/v1/contexts/"+fakeCtxID+"/versions")
	for _, f := range publishBody(t, pubs[len(pubs)-1]).Files {
		if f.Executable {
			t.Errorf("%s published executable", f.Path)
		}
	}
}

// pull --force replaces files whole: the mode follows the remote file both
// ways, and a failed download leaves the member's file as it was.
func TestContextPullForceReplacesFilesWhole(t *testing.T) {
	skipOnWindows(t)
	fake := newFakeCtx()
	fake.seed("acme-api", 1, map[string]string{"files/a.md": "new\n", "files/b.md": "fine\n"})
	dir := writeTree(t, map[string]string{"files/a.md": "mine\n", "files/b.md": "keep me\n"})
	if err := os.Chmod(filepath.Join(dir, "files", "a.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake.corrupt["files/b.md"] = true
	r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir, "--force")
	mustCtxExit(t, r, exitUnavailable, "does not match", "1 of 2 files were already written", "--force")
	if b, _ := os.ReadFile(filepath.Join(dir, "files", "b.md")); string(b) != "keep me\n" {
		t.Errorf("a failed download changed the member's file: %q", b)
	}
	st, _ := os.Stat(filepath.Join(dir, "files", "a.md"))
	if b, _ := os.ReadFile(filepath.Join(dir, "files", "a.md")); string(b) != "new\n" || st.Mode().Perm() != 0o644 {
		t.Errorf("a.md %q %v", b, st.Mode())
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "files"))
	if len(entries) != 2 {
		t.Errorf("left temporary files: %v", entries)
	}
}

func TestContextPullCapsTheTotal(t *testing.T) {
	fake := newFakeCtx()
	c := fake.seed("acme-api", 1, nil)
	for i := 0; i < 3; i++ {
		c.Files = append(c.Files, api.ContextFile{Path: fmt.Sprintf("files/big%d.bin", i), SHA256: shaOf([]byte{byte(i)}), Size: 90 << 20})
	}
	dir := filepath.Join(t.TempDir(), "out")
	r := runCtx(t, fake, ctxSetup{}, "context", "pull", "acme-api", dir)
	mustCtxExit(t, r, exitDataErr, "more than a context version holds", "nothing was written")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("created %s", dir)
	}
}

func TestContextRefIsCheckedLocally(t *testing.T) {
	for _, ref := range []string{"..", ".", "a/b", "Acme", "%2e%2e"} {
		for _, args := range [][]string{{"show", ref}, {"pull", ref, t.TempDir()}, {"rm", ref, "--yes"}, {"edit", ref, "--name", "x"}} {
			fake := newFakeCtx()
			r := runCtx(t, fake, ctxSetup{}, append([]string{"context"}, args...)...)
			mustCtxExit(t, r, exitUsage, "neither a context name")
			if len(fake.requests()) != 0 {
				t.Errorf("%v sent %v", args, fake.requests())
			}
		}
	}
	// A context id is accepted as is.
	fake := newFakeCtx()
	fake.seed("acme-api", 1, nil)
	mustCtxExit(t, runCtx(t, fake, ctxSetup{}, "context", "show", fakeCtxID), 0)
	if got := fake.requests(); len(got) != 1 || got[0].Method != http.MethodGet {
		t.Errorf("%v", got)
	}
}

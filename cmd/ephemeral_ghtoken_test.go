package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Ad-hoc ephemeral runs on a private repository (cloud-api #382): nan run
// --ephemeral --repo OWNER/NAME --github-token-secret NAME names one of the
// member's secrets holding a GitHub token. The platform clones with it and
// never gives it to the agent. The flag needs --ephemeral and --repo; the
// value is a secret NAME, never the token (a pasted token is not echoed).
// The platform answers 404 not_found (no such secret) or 400 (a provider
// key, an empty name) with param github_token_secret.

func TestEphemeralGitHubTokenSecretIsSent(t *testing.T) {
	h := newHarness(t, &fakePlatform{ephemeral: true})
	opts := ephemeralOpts()
	opts.githubTokenSecret, opts.githubTokenSecretSet = "GH_READ", true
	if err := doRun(context.Background(), h.env, opts, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	c := h.fake.created
	if c["github_token_secret"] != "GH_READ" || c["github_repo"] != "acme/api" || c["executor"] != "ephemeral" {
		t.Errorf("created with %v", c)
	}
}

func TestEphemeralWithoutGitHubTokenSecretSendsNone(t *testing.T) {
	h := newHarness(t, &fakePlatform{ephemeral: true})
	if err := doRun(context.Background(), h.env, ephemeralOpts(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.fake.created["github_token_secret"]; ok {
		t.Errorf("sent a token secret nobody asked for: %v", h.fake.created)
	}
}

func TestGitHubTokenSecretFlagMistakesAreUsageErrors(t *testing.T) {
	const pasted = "ghp_0123456789abcdefghijABCDEFGHIJ012345"
	for _, tc := range []struct {
		name          string
		edit          func(*runOptions)
		wants, avoids []string
	}{
		{"without --ephemeral", func(o *runOptions) { o.ephemeral, o.repo, o.model = false, "", "" },
			[]string{"--github-token-secret is for ephemeral runs", "--ephemeral", "--repo"}, nil},
		{"without --repo", func(o *runOptions) { o.repo = "" }, []string{"--repo OWNER/NAME"}, nil},
		{"empty", func(o *runOptions) { o.githubTokenSecret = "" }, []string{"--github-token-secret", "NAME"}, nil},
		{"lowercase name", func(o *runOptions) { o.githubTokenSecret = "gh_read" }, []string{"Did you mean GH_READ?"}, nil},
		{"bad name", func(o *runOptions) { o.githubTokenSecret = "my token" }, []string{"capital letters, digits and _", "nan secrets ls"}, []string{"my token"}},
		{"pasted token", func(o *runOptions) { o.githubTokenSecret = pasted }, []string{"never the token", "shell history", "revoke it"}, []string{pasted, "ghp_"}},
		{"pasted fine-grained token", func(o *runOptions) { o.githubTokenSecret = "github_pat_11ABCDEFG0123456789_abcdefghijk" },
			[]string{"shell history"}, []string{"github_pat_11"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &fakePlatform{ephemeral: true})
			opts := ephemeralOpts()
			opts.githubTokenSecret, opts.githubTokenSecretSet = "GH_READ", true
			tc.edit(&opts)
			err := doRun(context.Background(), h.env, opts, []string{"x"})
			if exitCode(err) != exitUsage {
				t.Fatalf("exit %d (%v)", exitCode(err), err)
			}
			wantAll(t, tc.name, err.Error(), tc.wants...)
			for _, a := range tc.avoids {
				if strings.Contains(err.Error(), a) {
					t.Errorf("%q repeats %q", err.Error(), a)
				}
			}
			if h.fake.created != nil {
				t.Error("sent the run anyway")
			}
		})
	}
}

func TestGitHubTokenSecretRefusalsSayWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		code, message string
		wants, avoids []string
	}{
		{"unknown secret", 404, "not_found", "secret not found",
			[]string{"no secret named GH_READ", "Settings > Secrets", "nan secrets ls"}, []string{"runs are not enabled"}},
		{"a provider key", 400, "invalid_request", "the secret is a provider key, not a GitHub token \u001b[2J",
			[]string{"--github-token-secret GH_READ", "provider key, not a GitHub token", "nan secrets ls"}, []string{"\u001b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, _ := json.Marshal(tc.message)
			h := newHarness(t, &fakePlatform{ephemeral: true, createStatus: tc.status,
				createBody: fmt.Sprintf(`{"error":{"message":%s,"type":"invalid_request_error","param":"github_token_secret","code":%q}}`, msg, tc.code)})
			h.env.cred = credential{kind: credSession}
			opts := ephemeralOpts()
			opts.githubTokenSecret, opts.githubTokenSecretSet = "GH_READ", true
			err := doRun(context.Background(), h.env, opts, []string{"x"})
			if exitCode(err) != exitUsage {
				t.Fatalf("exit %d (%v)", exitCode(err), err)
			}
			wantAll(t, tc.name, err.Error(), tc.wants...)
			for _, a := range tc.avoids {
				if strings.Contains(err.Error(), a) {
					t.Errorf("%q mentions %q", err.Error(), a)
				}
			}
			assertNoControls(t, tc.name, err.Error())
		})
	}
}

// nan run --help tells how to run on a private repository, and no longer
// says an ephemeral run started here has to use a public one.
func TestRunHelpExplainsGitHubTokenSecret(t *testing.T) {
	wantAll(t, "nan run --help", runCmd.Long, "--github-token-secret NAME", "private repository", "never")
	if strings.Contains(runCmd.Long, "has to be public") {
		t.Error("help still says the repository has to be public")
	}
	f := runCmd.Flags().Lookup("github-token-secret")
	if f == nil || !strings.Contains(f.Usage, "--ephemeral") || !strings.Contains(f.Usage, "never the token") {
		t.Fatalf("flag: %+v", f)
	}
}

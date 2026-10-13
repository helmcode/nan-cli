package update

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nxssie/nan-cli/internal/build"
)

func TestNewerComparesSemanticVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.10.0", "0.9.9", true}, // numbers, not strings
		{"v1.0.0", "0.9.0", true},
		{"0.2.0", "0.2.0-rc.1", true}, // a release beats its pre-release
		{"0.2.0-rc.1", "0.2.0", false},
		{"0.2.0", "0.2.0", false},
		{"0.1.0", "0.2.0", false},
		{"0.2.0", "0.1.0-demo.abc123", true},
		{"garbage", "0.1.0", false}, // a malformed answer never nags
		{"0.2", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A development build is never told to update: it is not behind a release,
// it is beside one.
func TestADevelopmentBuildIsNotNagged(t *testing.T) {
	was := build.Version
	t.Cleanup(func() { build.Version = was })

	s := State{Latest: "9.9.9"}
	build.Version = "0.1.0-dev.abc"
	if s.Available() != "" {
		t.Error("a dev build was offered an update")
	}
	build.Version = "0.1.0"
	if s.Available() != "9.9.9" {
		t.Error("a release that is behind was not offered one")
	}
}

func TestTheCheckIsDailyAtMost(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if !(State{}).DueForCheck(now) {
		t.Error("never checked, and not due")
	}
	recent := State{CheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	if recent.DueForCheck(now) {
		t.Error("checked two hours ago, and due again")
	}
	old := State{CheckedAt: now.Add(-25 * time.Hour).Format(time.RFC3339)}
	if !old.DueForCheck(now) {
		t.Error("checked yesterday, and not due")
	}
}

// The update command is the one that replaces THIS copy: the same install
// method, and the same place.
func TestTheCommandMatchesHowItWasInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]struct {
		goos, exe, gopath, want string
	}{
		"windows":     {"windows", `C:\tools\helmcode.exe`, "", "irm https://nan.builders/install.ps1 | iex"},
		"go install":  {"linux", filepath.Join(home, "go", "bin", "helmcode"), "", "go install github.com/nxssie/nan-cli/cmd/nan@latest"},
		"GOPATH":      {"linux", "/opt/gopath/bin/helmcode", "/opt/gopath", "go install github.com/nxssie/nan-cli/cmd/nan@latest"},
		"script":      {"darwin", "/usr/local/bin/helmcode", "", "curl -fsSL https://nan.builders/install | bash"},
		"script, dir": {"linux", filepath.Join(home, ".local", "bin", "helmcode"), "", "INSTALL_DIR=" + filepath.Join(home, ".local", "bin") + " curl -fsSL https://nan.builders/install | bash"},
	}
	for name, c := range cases {
		// Paths are the host's: Unix layouts cannot be reproduced on a
		// Windows host, nor the other way round.
		if (c.goos == "windows") != (runtime.GOOS == "windows") && name != "windows" {
			continue
		}
		if got := commandFor(c.goos, c.exe, c.gopath); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// The first look records the catalogue without announcing it; after that,
// a model that appears is new until the list is opened.
func TestNewModelsAreFoundAfterTheFirstLook(t *testing.T) {
	var s State
	if got := s.SeeModels([]string{"glm5.3", "deepseek-v4-flash"}); len(got) != 0 {
		t.Fatalf("the first look announced %v", got)
	}
	if got := s.SeeModels([]string{"glm5.3", "deepseek-v4-flash"}); len(got) != 0 {
		t.Fatalf("nothing changed, and %v is new", got)
	}
	got := s.SeeModels([]string{"glm5.3", "deepseek-v4-flash", "glm5.4-flash"})
	if len(got) != 1 || got[0] != "glm5.4-flash" {
		t.Fatalf("the new model is %v", got)
	}
	// Still new on the next run, until it has been seen.
	if got := s.SeeModels([]string{"glm5.3", "deepseek-v4-flash", "glm5.4-flash"}); len(got) != 1 {
		t.Errorf("a new model was forgotten before it was seen: %v", got)
	}
	s.MarkSeen()
	if got := s.SeeModels([]string{"glm5.3", "deepseek-v4-flash", "glm5.4-flash"}); len(got) != 0 {
		t.Errorf("a seen model is still new: %v", got)
	}
}

func TestStateSurvivesARun(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	s := State{Latest: "0.2.0", KnownModels: []string{"a"}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.Latest != "0.2.0" || len(got.KnownModels) != 1 {
		t.Errorf("loaded %+v", got)
	}
}

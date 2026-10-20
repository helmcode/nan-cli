package pulse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/nxssie/nan-cli/internal/ui"
)

// ── fixtures ─────────────────────────────────────────────────────────────

func usagePeriod(total float64, models ...api.ModelUsage) api.Period {
	return api.Period{TotalTokens: api.Count(total), ByModel: models}
}

func demoSnapshot() snapshot {
	return snapshot{
		token: "t",
		me: &api.Me{
			Email:      "cristian@example.com",
			Tier:       "inference",
			GraceUntil: "2026-11-20T10:00:00Z",
		},
		usage: &api.Usage{
			Last24h: usagePeriod(3e6,
				api.ModelUsage{Model: "glm5.3-flash", InputTokens: 2e6, OutputTokens: 1e6}),
			Last30d: usagePeriod(90e6,
				api.ModelUsage{Model: "glm5.3-flash", InputTokens: 50e6, OutputTokens: 10e6},
				api.ModelUsage{Model: "deepseek-v4-flash", InputTokens: 20e6, OutputTokens: 10e6}),
			AllTime: usagePeriod(240e6,
				api.ModelUsage{Model: "glm5.3-flash", InputTokens: 150e6, OutputTokens: 30e6},
				api.ModelUsage{Model: "deepseek-v4-flash", InputTokens: 40e6, OutputTokens: 20e6}),
		},
		keyStatus: &api.KeyStatus{Exists: true, Alias: "laptop"},
		models: []modelInfo{
			{id: "deepseek-v4-flash", kind: kindOf("deepseek-v4-flash")},
			{id: "glm5.3", kind: kindOf("glm5.3")},
			{id: "glm5.3-flash", kind: kindOf("glm5.3-flash")},
			{id: "qwen3-embedding", kind: kindOf("qwen3-embedding")},
		},
	}
}

// platform is a nan.builders that records what it is asked.
type platform struct {
	mu   sync.Mutex
	seen []string
}

func (p *platform) saw(want string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.seen {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// sandbox points everything this package touches at a test's own folders
// and servers: HOME (the session and the tools' configs), the cache (the
// update state), the platform and the inference API.
func sandbox(t *testing.T) *platform {
	t.Helper()
	p := &platform{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer sk-good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"glm5.3-flash"},{"id":"deepseek-v4-flash"}]}`))
		case "/api/auth/logout":
			_, _ = w.Write([]byte(`{"status":"logged out"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("NAN_API_URL", srv.URL)
	t.Setenv("NAN_INFERENCE_URL", srv.URL+"/v1")
	t.Setenv("PATH", t.TempDir()) // no tool on PATH: tests see none installed
	t.Setenv(session.TokenEnvVar, "")
	return p
}

func demoModel(t *testing.T, w, h int) (model, *platform) {
	t.Helper()
	p := sandbox(t)
	sess := &session.Session{Token: "t", APIKey: "sk-good"}
	m := newModel(context.Background(), api.New(sess.Token), sess)
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(model)
	next, _ = m.Update(loadedMsg{snap: demoSnapshot()})
	m = next.(model)
	m.reveal, m.animating = 1, false // the tests read the settled screen
	return m, p
}

func keyOf(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// press sends a key and settles the screen: the commands it returns are run
// and fed back, the way Bubble Tea would, so a test sees what a person
// would. A reload keeps the fixture rather than asking the fake for one.
func press(t *testing.T, m model, k tea.KeyPressMsg) model {
	t.Helper()
	next, cmd := m.Update(k)
	return settle(t, next.(model), cmd)
}

func settle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 50; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		got := make(chan tea.Msg, 1)
		go func() { got <- c() }()
		var msg tea.Msg
		select {
		case msg = <-got:
		case <-time.After(150 * time.Millisecond):
			continue // a timer: the flash, the loader, a cursor blink
		}
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg, animTickMsg:
		case loadedMsg:
			keep := m.snap
			keep.token = m.session.Token
			next, _ := m.Update(loadedMsg{snap: keep})
			m = next.(model)
		default:
			next, more := m.Update(msg)
			m = next.(model)
			queue = append(queue, more)
		}
	}
	m.reveal = 1
	return m
}

func screen(m model) string { return ansi.Strip(m.render()) }

func footerOf(screen string) string {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	return lines[len(lines)-1]
}

// ── what there is and where ──────────────────────────────────────────────

func TestTheAreasAreAlwaysOnScreen(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	for _, a := range []string{"1 Home", "2 Profile", "3 Usage", "4 Models", "5 Costs", "6 Harnesses", "7 About"} {
		for _, at := range []model{m, press(t, m, keyOf("3")), press(t, m, keyOf("7"))} {
			if !strings.Contains(screen(at), a) {
				t.Errorf("%q is not on the %s screen", a, at.area.title())
			}
		}
	}
}

func TestNumbersAndTabMoveBetweenAreas(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	for a := areaProfile; a < areaCount; a++ {
		if n := press(t, m, keyOf(string(rune('1'+a)))); n.area != a {
			t.Errorf("%c went to %s", '1'+a, n.area.title())
		}
	}
	for want := areaProfile; want < areaCount; want++ {
		m = press(t, m, keyOf("tab"))
		if m.area != want {
			t.Fatalf("tab went to %s, want %s", m.area.title(), want.title())
		}
	}
	if m = press(t, m, keyOf("tab")); m.area != areaHome {
		t.Errorf("tab from the last area went to %s", m.area.title())
	}
}

func TestEachAreaShowsItsThings(t *testing.T) {
	m, _ := demoModel(t, 140, 44)
	cases := map[string][]string{
		"1": {"welcome to", "nan.builders", "Nothing needs you right now", "Set up your tools", "240.0M"},
		"2": {"Email", "cristi...", "Plan", "inference", "Membership ends", "20 Nov 2026"},
		"3": {"glm5.3-flash", "deepseek-v4-flash", "240.0M", "67%"},
		"4": {"glm5.3-flash", "chat", "chat · premium", "embedding"},
		"5": {"Claude Opus 5.5", "Anthropic", "GPT-6 Luna", "No per-token charges"},
		"6": {"Set up the tools", "saved on this machine", "Checks the key against the cluster"},
		"7": {"https://cloud.nan.builders", "@Nxssie", ".config/nan/session.json"},
	}
	for k, wants := range cases {
		out := screen(press(t, m, keyOf(k)))
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("area %s does not show %q:\n%s", k, want, out)
			}
		}
	}
}

// Profile is the email, cut short because a screen is shared and recorded,
// and the plan: nothing else the platform says about the account.
func TestProfileShowsTheAccountAndNothingElse(t *testing.T) {
	m, _ := demoModel(t, 140, 40)
	out := screen(press(t, m, keyOf("2")))
	if strings.Contains(out, "cristian@example.com") {
		t.Errorf("the email is on screen whole:\n%s", out)
	}
	if got := shorten("abc"); got != "..." {
		t.Errorf("a short identifier is %q", got)
	}
	if got := len(m.rows(areaProfile)); got != 3 {
		t.Errorf("Profile has %d rows, want email, plan and membership end", got)
	}
	m.snap.me.GraceUntil = ""
	if got := len(m.rows(areaProfile)); got != 2 {
		t.Errorf("a live membership still shows when it ends (%d rows)", got)
	}
}

// Without a session, the areas that need one say how to get it, rather
// than the platform's `unauthorized`.
func TestDataAreasSayToSignIn(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	m.session.Token = ""
	for _, k := range []string{"2", "3", "5"} {
		out := screen(press(t, m, keyOf(k)))
		if !strings.Contains(out, "not signed in - press s") || strings.Contains(strings.ToLower(out), "unauthorized") {
			t.Errorf("area %s without a session:\n%s", k, out)
		}
	}
	// Models does not need one: a key alone can ask the cluster.
	if out := screen(press(t, m, keyOf("4"))); strings.Contains(out, "not signed in - press s") {
		t.Errorf("Models asks for a sign-in it does not need:\n%s", out)
	}
}

// With a key and no session, the models still load, from the cluster.
func TestModelsLoadWithAKeyAndNoSession(t *testing.T) {
	sandbox(t)
	s := load(context.Background(), api.New(""), "sk-good", false)
	if len(s.models) != 2 || s.err != nil {
		t.Fatalf("models=%v err=%v", s.models, s.err)
	}
	s = load(context.Background(), api.New(""), "sk-wrong", false)
	if !s.keyRefused {
		t.Error("a refused key is not said to be refused")
	}
}

// ── the frame ────────────────────────────────────────────────────────────

func TestTheFrameFitsTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{72, 20}, {100, 30}, {160, 50}} {
		m, _ := demoModel(t, size[0], size[1])
		for _, k := range []string{"1", "2", "3", "4", "5", "6", "7"} {
			v := press(t, m, keyOf(k))
			for _, view := range []model{v, press(t, v, keyOf("enter")), press(t, v, keyOf("/"))} {
				lines := strings.Split(view.render(), "\n")
				if len(lines) != size[1] {
					t.Errorf("%dx%d area %s is %d lines tall", size[0], size[1], k, len(lines))
				}
				for _, l := range lines {
					if w := ansi.StringWidth(l); w > size[0] {
						t.Errorf("%dx%d area %s has a line %d wide: %q", size[0], size[1], k, w, ansi.Strip(l))
						break
					}
				}
			}
		}
	}
}

func TestNoEmDashReachesTheScreen(t *testing.T) {
	m, _ := demoModel(t, 140, 44)
	for _, k := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		if strings.ContainsRune(screen(press(t, m, keyOf(k))), 0x2014) {
			t.Errorf("an em dash is on area %s", k)
		}
	}
}

// ── what can be done ─────────────────────────────────────────────────────

// e is Setup from anywhere: "paste your key" is what a member is told from
// whichever area they are looking at.
func TestSetupOpensFromAnywhere(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	for _, k := range []string{"2", "5", "7"} {
		n := press(t, press(t, m, keyOf(k)), keyOf("e"))
		if !n.setup.Active() || n.area != areaHarness {
			t.Errorf("e from area %s: setup=%v area=%s", k, n.setup.Active(), n.area.title())
		}
	}
}

func TestEnterOpensTheRowsActions(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	m = press(t, press(t, m, keyOf("4")), keyOf("enter"))
	if m.modal.kind != modalMenu {
		t.Fatal("enter on a model did not open its menu")
	}
	out := screen(m)
	for _, want := range []string{"Details", "Copy the id"} {
		if !strings.Contains(out, want) {
			t.Errorf("the menu does not offer %q:\n%s", want, out)
		}
	}
	m = press(t, m, keyOf("enter"))
	if !m.inCard() || !strings.Contains(screen(m), "tokens of context") {
		t.Errorf("Details did not open the model:\n%s", screen(m))
	}
}

func TestASearchLandsOnTheThing(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	m = press(t, m, keyOf("/"))
	for _, r := range "deepseek" {
		m = press(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = press(t, m, keyOf("enter"))
	if m.area != areaModels || m.selectedRow() == nil || m.selectedRow().id != "deepseek-v4-flash" {
		t.Errorf("the search landed on %s, row %+v", m.area.title(), m.selectedRow())
	}
}

// The footer offers what works here: sign out when signed in, set up from
// anywhere, and the ways around.
func TestTheFooterOffersWhatWorks(t *testing.T) {
	m, _ := demoModel(t, 200, 40)
	foot := footerOf(screen(m))
	for _, want := range []string{"e set up", "o sign out", "tab next area", "/ find", "q quit"} {
		if !strings.Contains(foot, want) {
			t.Errorf("the footer does not offer %q: %s", want, foot)
		}
	}
	m.session.Token = ""
	if foot := footerOf(screen(m)); !strings.Contains(foot, "s sign in") {
		t.Errorf("signed out, the footer does not offer the sign-in: %s", foot)
	}
}

// ── signing in and out ───────────────────────────────────────────────────

// A fresh machine is asked to sign in, a signed-in one without a key is
// asked for the key, and a machine with both is asked for nothing.
func TestTheFirstRunAsksForWhatIsMissing(t *testing.T) {
	sandbox(t)
	fresh := newModel(context.Background(), api.New(""), &session.Session{})
	_ = fresh.opening()
	if !fresh.signIn.Active() {
		t.Error("a fresh machine was not asked to sign in")
	}
	noKey := newModel(context.Background(), api.New("t"), &session.Session{Token: "t"})
	_ = noKey.opening()
	if noKey.signIn.Active() || !noKey.setup.Active() {
		t.Error("a signed-in machine without a key was not asked for one")
	}
	done := newModel(context.Background(), api.New("t"), &session.Session{Token: "t", APIKey: "sk-good"})
	_ = done.opening()
	if done.inFlow() {
		t.Error("a configured machine was asked for something")
	}
}

// Signing out takes two presses of o, ends the session on the platform,
// deletes it here, and opens the sign-in again.
func TestSigningOutTakesTwoPresses(t *testing.T) {
	m, p := demoModel(t, 130, 40)
	if err := session.Save(m.session); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, keyOf("o"))
	if p.saw("logout") || !m.session.SignedIn() {
		t.Fatal("one o signed out")
	}
	if !strings.Contains(screen(m), "press o again") {
		t.Errorf("the first o does not say what the second does:\n%s", screen(m))
	}
	m = press(t, m, keyOf("o"))
	if !p.saw("POST /api/auth/logout") {
		t.Error("the platform was not asked to end the session")
	}
	if m.session.SignedIn() || !m.signIn.Active() {
		t.Errorf("after signing out: signed in=%v sign-in open=%v", m.session.SignedIn(), m.signIn.Active())
	}
	if _, err := os.Stat(session.Path()); !os.IsNotExist(err) {
		t.Error("session.json is still on disk")
	}
}

// Any other key between the two presses keeps the session.
func TestAnotherKeyKeepsTheSession(t *testing.T) {
	m, p := demoModel(t, 130, 40)
	m = press(t, press(t, press(t, m, keyOf("o")), keyOf("3")), keyOf("o"))
	if p.saw("logout") || !m.confirmSignOut {
		t.Error("o, another key, o signed out")
	}
}

func TestAStraySignedOutMsgIsIgnored(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	next, _ := m.Update(signedOutMsg{})
	if n := next.(model); !n.session.SignedIn() {
		t.Error("an answer to a sign-out nobody asked for signed out")
	}
}

// A refusal that arrives after a newer sign-in says nothing about the new
// session.
func TestALateRefusalDoesNotEndTheNewSession(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	old := demoSnapshot()
	old.token, old.expired = "an-older-token", true
	next, _ := m.Update(loadedMsg{snap: old})
	if n := next.(model); !n.session.SignedIn() || n.expiredNotice {
		t.Error("a late refusal of an old session ended the current one")
	}
}

// Opening on an expired session asks to sign in; one that expires later is
// said, and the keyboard is left alone.
func TestAnExpiredSessionIsHandled(t *testing.T) {
	sandbox(t)
	sess := &session.Session{Token: "t", APIKey: "sk-good"}
	m := newModel(context.Background(), api.New("t"), sess)
	m.w, m.h = 130, 40
	expired := snapshot{token: "t", expired: true}
	next, _ := m.Update(loadedMsg{snap: expired})
	m = next.(model)
	if m.session.SignedIn() || !m.signIn.Active() || m.session.APIKey != "sk-good" {
		t.Errorf("opening expired: signed in=%v sign-in=%v key=%q", m.session.SignedIn(), m.signIn.Active(), m.session.APIKey)
	}

	later, _ := demoModel(t, 130, 40)
	expired.token = "t"
	next, _ = later.Update(loadedMsg{snap: expired})
	later = next.(model)
	if later.signIn.Active() || !strings.Contains(later.flash, "press s") {
		t.Errorf("a later expiry: sign-in=%v flash=%q", later.signIn.Active(), later.flash)
	}
	if !strings.Contains(screen(later), "Your session has expired") {
		t.Errorf("Home does not say the session expired:\n%s", screen(later))
	}
}

// ── Costs ────────────────────────────────────────────────────────────────

func TestPricingTableIsPlausible(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range pricingTable {
		if seen[p.model] {
			t.Errorf("%s is listed twice", p.model)
		}
		seen[p.model] = true
		if p.inPer1M <= 0 || p.outPer1M < p.inPer1M {
			t.Errorf("%s: in %.2f out %.2f", p.model, p.inPer1M, p.outPer1M)
		}
		if _, ok := providerColor[p.provider]; !ok {
			t.Errorf("%s has no colour", p.provider)
		}
	}
}

// Fails on the day the promotional rate ends, so the number is changed
// rather than forgotten.
func TestGeminiFlashPromoHasNotExpired(t *testing.T) {
	if !time.Now().Before(geminiFlashPromoEnds) {
		t.Error("Gemini 3.8 Flash is no longer on its promotional rate: update pricingTable")
	}
}

func TestCostsAreDollarsWithSeparators(t *testing.T) {
	for v, want := range map[float64]string{0: "$0.00", 12.5: "$12.50", 1234.5: "$1,234.50", 1234567: "$1,234,567.00"} {
		if got := fmtCost(v); got != want {
			t.Errorf("fmtCost(%v) = %q, want %q", v, got, want)
		}
	}
	p := usagePeriod(0, api.ModelUsage{InputTokens: 1e6, OutputTokens: 1e6})
	if got := calcCost(p, providerPricing{inPer1M: 2, outPer1M: 10}); got != 12 {
		t.Errorf("a million in and out at 2/10 costs %v", got)
	}
}

// One line a row, a blank line only where the provider changes.
func TestCostRowsAreNotDoubleSpaced(t *testing.T) {
	m, _ := demoModel(t, 140, 44)
	out := screen(press(t, m, keyOf("5")))
	blanks := 0
	in := false
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Claude Fable") {
			in = true
		}
		if in && strings.Contains(l, "Gemini 3.8 Flash") {
			break
		}
		if in && strings.TrimSpace(strings.TrimLeft(l, " │")) == "" {
			blanks++
		}
	}
	if blanks != 2 {
		t.Errorf("%d blank lines between the rows, want 2 (one per change of provider):\n%s", blanks, out)
	}
}

// ── the mascot ───────────────────────────────────────────────────────────

func TestTheFaceFollowsWhatTheDashboardIsDoing(t *testing.T) {
	m, _ := demoModel(t, 130, 40)
	if m.mood() != ui.MoodHappy {
		t.Errorf("signed in with a key: %v", m.mood())
	}
	m.loading = true
	if m.mood() != ui.MoodThinking {
		t.Errorf("loading: %v", m.mood())
	}
	m.loading = false
	m.session.Token = ""
	if m.mood() != ui.MoodAsleep {
		t.Errorf("signed out: %v", m.mood())
	}
}

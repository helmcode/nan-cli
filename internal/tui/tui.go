package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/auth"
	catalog "github.com/nxssie/nan-cli/internal/models"
	"github.com/nxssie/nan-cli/internal/session"
)

// ── palette ───────────────────────────────────────────────────────────────────

var (
	cCyan    = lipgloss.AdaptiveColor{Light: "#6d28d9", Dark: "#a78bfa"}
	cBlue    = lipgloss.AdaptiveColor{Light: "#5b21b6", Dark: "#8b5cf6"}
	cBlueDim = lipgloss.AdaptiveColor{Light: "#ddd6fe", Dark: "#2e1065"}
	cGray    = lipgloss.AdaptiveColor{Light: "#52525b", Dark: "#71717a"}
	cDimGray = lipgloss.AdaptiveColor{Light: "#a1a1aa", Dark: "#52525b"}
	cWhite   = lipgloss.AdaptiveColor{Light: "#18181b", Dark: "#ffffff"}
	cText    = lipgloss.AdaptiveColor{Light: "#374151", Dark: "#cbd5e1"}
	cRed     = lipgloss.AdaptiveColor{Light: "#dc2626", Dark: "#ef4444"}

	modelColors = []lipgloss.Color{
		lipgloss.Color("#8b5cf6"),
		lipgloss.Color("#a78bfa"),
		lipgloss.Color("#10B981"),
		lipgloss.Color("#F59E0B"),
		lipgloss.Color("#EF4444"),
	}
)

// ── layout ────────────────────────────────────────────────────────────────────

// layout holds all computed dimensions for a given terminal size.
type layout struct {
	w      int // terminal width
	h      int // terminal height
	barW   int // progress bar width
	keyW   int // label column width for KV pairs
	nameW  int // model name column width in usage
	indent string
}

func newLayout(w, h int) layout {
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}
	// bar: leave room for "  <nameW> <bar> XX.X%"
	nameW := clamp(w/6, 8, 18)
	barW := clamp(w-nameW-12, 8, 56)
	keyW := clamp(w/4, 12, 22)
	return layout{w: w, h: h, barW: barW, keyW: keyW, nameW: nameW, indent: "  "}
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// ── tabs ──────────────────────────────────────────────────────────────────────

type tabID int

const (
	tabHome tabID = iota
	tabProfile
	tabUsage
	tabModels
	tabCosts
	tabSetup
	tabAbout
)

var tabDefs = []struct {
	id   tabID
	name string
}{
	{tabHome, "Home"},
	{tabProfile, "Profile"},
	{tabUsage, "Usage"},
	{tabModels, "Models"},
	{tabCosts, "Costs"},
	{tabSetup, "Setup"},
	{tabAbout, "About"},
}

// ── messages ──────────────────────────────────────────────────────────────────

type fetchedMsg struct {
	tab  tabID
	data any
}
type fetchErrMsg struct{ err error }

// The Setup tab never blocks on the network: it is the one tab a member can
// use with no connection, and the two things below are extra information
// rather than its content. They arrive when they arrive.
type keyStatusMsg struct{ status *api.KeyStatus }
type keyCheckedMsg struct {
	models int
	err    error
}

// What an unauthenticated tab says. Not session.ErrNotLoggedIn, which names
// the shell command: inside the panel there is a key for this, and telling
// someone to quit and run something is the detour this replaced.
var errNotSignedIn = errors.New("not signed in — press s")

type configuredMsg struct {
	msg     string
	written []string
	failed  []toolFailure
}

// Sent once, by Init, so the panel can pick up wherever the setup was left.
type firstRunMsg struct{}

type linkSentMsg struct{ err error }
type signedInMsg struct{ err error }

// ── model ─────────────────────────────────────────────────────────────────────

type model struct {
	client      *api.Client
	sess        *session.Session
	active      int
	loading     bool
	err         error
	spin        spinner.Model
	cache       map[tabID]any
	lay         layout
	scrollY     int
	showHelp    bool
	keyInput    textinput.Model
	editingKey  bool
	setupMsg    string
	setupCursor int
	keyStatus   *api.KeyStatus
	keyAsked    bool
	keyCheck    string

	// Signing in, without leaving the panel. See startLogin.
	configuring bool
	configured  []string
	failures    []toolFailure

	// Set by the first press of `o`, cleared by anything else: signing out is
	// not something to do to somebody on a stray keystroke.
	confirmSignOut bool
	wizard         wizardStep
	loginStage     loginStage
	loginInput     textinput.Model
	loginEmail     string
	loginMsg       string
	loginBusy      bool
}

// Where a member is in the guided setup. The inner inputs still drive
// themselves - loginStage for the two sign-in questions, editingKey for the
// key - and this is the step the screen is showing around them, so the panel
// can draw the four of them as one sequence with a banner over it.
type wizardStep int

const (
	wizardOff wizardStep = iota
	wizardEmail
	wizardLink
	wizardKey
	wizardTools
	wizardDone
)

// The words are the member's, from the report that asked for this.
var wizardSteps = [...]struct {
	n     int
	title string
}{
	{1, "Let's get you logged in"},
	{2, "Let's confirm it with the magic link"},
	{3, "Let's set up your API key"},
	{4, "Let's configure your tools"},
}

// Which of the four a step belongs to. wizardLink is step 2, and everything
// after the tools is step 4 still finishing.
func (w wizardStep) index() int {
	switch w {
	case wizardEmail:
		return 0
	case wizardLink:
		return 1
	case wizardKey:
		return 2
	}
	return 3
}

// The face for each, so the mascot is doing something that matches the step
// rather than staring through it.
func (w wizardStep) mood() mascotMood {
	switch w {
	case wizardEmail, wizardKey:
		return moodNormal
	case wizardLink:
		return moodThinking
	case wizardDone:
		return moodHappy
	}
	return moodNormal
}

// Where a member is in the sign-in flow. It is two questions - an address and
// the link that arrives at it.
type loginStage int

const (
	loginOff loginStage = iota
	loginAskEmail
	loginAskLink
)

func newModel(client *api.Client, sess *session.Session) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(cBlue)

	ti := textinput.New()
	ti.Placeholder = "paste your NaN API key here"
	ti.CharLimit = 512
	ti.PromptStyle = lipgloss.NewStyle().Foreground(cCyan)
	ti.TextStyle = lipgloss.NewStyle().Foreground(cText)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(cDimGray)

	li := textinput.New()
	li.CharLimit = 2048 // a sign-in link is long
	li.PromptStyle = lipgloss.NewStyle().Foreground(cCyan)
	li.TextStyle = lipgloss.NewStyle().Foreground(cText)
	li.PlaceholderStyle = lipgloss.NewStyle().Foreground(cDimGray)

	return model{
		client:     client,
		sess:       sess,
		loginInput: li,
		cache:      make(map[tabID]any),
		spin:       sp,
		lay:        newLayout(80, 24),
		keyInput:   ti,
	}
}

func (m model) Init() tea.Cmd {
	// Init takes a value receiver and returns only a Cmd, so anything it
	// changes about the model is thrown away. The first-run question is asked
	// as a message instead, and answered in Update where the state lives.
	return tea.Batch(m.spin.Tick, func() tea.Msg { return firstRunMsg{} })
}

func (m model) activeID() tabID { return tabDefs[m.active].id }

// Where a tab sits in the bar, for the keys that jump straight to one.
func tabIndex(id tabID) int {
	for i, t := range tabDefs {
		if t.id == id {
			return i
		}
	}
	return 0
}

// ── update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.lay = newLayout(msg.Width, msg.Height)

	case spinner.TickMsg:
		if m.loading || m.configuring {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}

	case fetchedMsg:
		m.loading = false
		m.err = nil
		m.cache[msg.tab] = msg.data

	case fetchErrMsg:
		m.loading = false
		m.err = msg.err

	case configuredMsg:
		m.configuring = false
		m.setupMsg = msg.msg
		m.configured = msg.written
		m.failures = msg.failed
		// The step is finished either way. One tool refusing to be configured
		// is not a reason to hold somebody on the last screen of a setup with
		// no way on but escape - which is exactly what it did, and what was
		// reported. What failed is shown, and they can carry on.
		if m.wizard == wizardTools {
			m.wizard = wizardDone
		}

	case firstRunMsg:
		return m, m.resumeSetup()

	case linkSentMsg:
		m.loginBusy = false
		if msg.err != nil {
			m.loginMsg = "error: " + msg.err.Error()
			m.loginStage = loginAskEmail
			return m, m.loginInput.Focus()
		}
		m.wizard = wizardLink
		m.loginStage = loginAskLink
		m.loginMsg = "a link is on its way to " + m.loginEmail +
			" — copy it out of the email without opening it, the link works once"
		m.loginInput.SetValue("")
		m.loginInput.Placeholder = "https://nan.builders/...?token=..."
		m.loginInput.Prompt = "Paste the link: "
		return m, m.loginInput.Focus()

	case signedInMsg:
		m.loginBusy = false
		if msg.err != nil {
			m.loginMsg = "error: " + msg.err.Error()
			return m, m.loginInput.Focus()
		}
		// Reload the session so the token is the one just written, and drop
		// the cached tabs: they are holding the `not logged in` they answered
		// a moment ago.
		if sess, err := session.Load(); err == nil {
			m.sess = sess
		}
		// And a client that carries it. The old one was built at start-up with
		// whatever token existed then - none - so leaving it in place signed a
		// member in and then answered every tab `unauthorized`, which reads
		// exactly like the login having failed.
		m.client = api.New(m.sess.Token)
		wasGuided := m.wizard != wizardOff
		m.cancelLogin()
		if wasGuided {
			m.wizard = wizardKey
		}
		m.cache = make(map[tabID]any)
		m.err = nil
		m.keyAsked = false
		// And straight on to whatever is still missing, which on a fresh
		// machine is the key.
		return m, tea.Batch(m.maybeLoad(), m.resumeSetup())

	case keyStatusMsg:
		m.keyStatus = msg.status

	case keyCheckedMsg:
		// A key that the cluster refuses is worth saying once, here, rather
		// than five times later as a 401 inside five different tools.
		if msg.err != nil {
			m.keyCheck = "error: the cluster refused this key — " + msg.err.Error()
		} else {
			m.keyCheck = fmt.Sprintf("key accepted by the cluster · %d models", msg.models)
			// The last link in the chain. Signing in leads to the key, and the
			// key leads here - to the tools, which is the step the panel
			// cannot take for someone: writing into their opencode or their
			// codex is a side effect that has to be asked for, so this names
			// the key to press rather than pressing it.
			if n := installedTools(); n > 0 {
				m.keyCheck += fmt.Sprintf("  ·  press c to configure the %d tools found", n)
			}
			if m.wizard == wizardKey {
				m.wizard = wizardTools
			}
		}

	case tea.KeyMsg:
		// The sign-in input takes every key while it is up, the same way the
		// API key input does below it.
		if m.loginStage != loginOff {
			switch msg.String() {
			case "enter":
				if m.loginBusy {
					return m, nil
				}
				value := strings.TrimSpace(m.loginInput.Value())
				if value == "" {
					return m, nil
				}
				m.loginBusy = true
				if m.loginStage == loginAskEmail {
					if !strings.Contains(value, "@") {
						m.loginBusy = false
						m.loginMsg = "error: that is not an email address"
						return m, nil
					}
					m.loginEmail = value
					m.loginMsg = "sending a link to " + value + "…"
					return m, sendLink(value)
				}
				m.loginMsg = "signing in…"
				return m, signIn(value)
			case "esc":
				m.cancelLogin()
				return m, nil
			default:
				var cmd tea.Cmd
				m.loginInput, cmd = m.loginInput.Update(msg)
				return m, cmd
			}
		}

		// When the API key input is active, route all keys to it
		if m.editingKey {
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(m.keyInput.Value())
				var check tea.Cmd
				if val != "" {
					m.sess.APIKey = val
					if err := session.Save(m.sess); err != nil {
						m.setupMsg = "error saving: " + err.Error()
					} else {
						m.setupMsg = "API key saved"
						m.keyCheck = "checking it against the cluster…"
						check = checkKey(val)
					}
				}
				m.editingKey = false
				m.keyInput.Blur()
				if check != nil {
					return m, check
				}
			case "esc":
				m.editingKey = false
				m.keyInput.Blur()
				m.setupMsg = ""
				// Leaving the field during the guided setup leaves the setup:
				// staying would show step 3 with nothing to type into.
				m.wizard = wizardOff
			default:
				var cmd tea.Cmd
				m.keyInput, cmd = m.keyInput.Update(msg)
				return m, cmd
			}
			return m, nil
		}

		if m.confirmSignOut && msg.String() != "o" {
			m.confirmSignOut = false
			m.setupMsg = ""
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			switch {
			case m.showHelp:
				m.showHelp = false
			case m.wizard != wizardOff:
				// Out of the setup and into the panel. Not out of the program:
				// someone who wanted that has q, and quitting the whole CLI
				// because they skipped a step would be its own small betrayal.
				m.wizard = wizardOff
				m.active = tabIndex(tabSetup)
			default:
				return m, tea.Quit
			}
		case "enter":
			if m.wizard == wizardDone {
				m.wizard = wizardOff
				m.active = tabIndex(tabHome)
				return m, m.maybeLoad()
			}
		case "?":
			m.showHelp = !m.showHelp
		case "right", "l", "tab":
			if !m.showHelp && m.active < len(tabDefs)-1 {
				m.active++
				m.scrollY = 0
				m.setupMsg = ""
				m.setupCursor = 0
				return m, m.maybeLoad()
			}
		case "left", "h", "shift+tab":
			if !m.showHelp && m.active > 0 {
				m.active--
				m.scrollY = 0
				m.setupMsg = ""
				m.setupCursor = 0
				return m, m.maybeLoad()
			}
		case "up", "k":
			if !m.showHelp {
				if m.activeID() == tabSetup || m.wizard == wizardTools {
					if m.setupCursor > 0 {
						m.setupCursor--
					}
				} else if m.scrollY > 0 {
					m.scrollY--
				}
			}
		case "down", "j":
			if !m.showHelp {
				if m.activeID() == tabSetup || m.wizard == wizardTools {
					m.setupCursor++
				} else {
					m.scrollY++
				}
			}
		case " ":
			if !m.showHelp && !m.configuring && (m.activeID() == tabSetup || m.wizard == wizardTools) {
				tools := detectTools()
				if m.setupCursor < len(tools) && tools[m.setupCursor].installed {
					name := tools[m.setupCursor].name
					if m.sess.EnabledTools == nil {
						m.sess.EnabledTools = map[string]bool{}
					}
					m.sess.EnabledTools[name] = !m.toolEnabled(name)
					_ = session.Save(m.sess)
				}
			}
		case "r":
			if !m.showHelp {
				id := m.activeID()
				if id == tabCosts {
					delete(m.cache, tabUsage)
				} else {
					delete(m.cache, id)
				}
				m.scrollY = 0
				return m, m.maybeLoad()
			}
		// Signing out, in two presses. `nan auth logout` already existed as a
		// command, which is no use to someone who is looking at the panel and
		// wants to switch accounts.
		case "o":
			if m.showHelp || m.wizard != wizardOff || m.sess.Token == "" {
				break
			}
			if !m.confirmSignOut {
				m.confirmSignOut = true
				m.setupMsg = "press o again to sign out, any other key to keep the session"
				m.active = tabIndex(tabSetup)
				break
			}
			m.confirmSignOut = false
			if err := session.Delete(); err != nil {
				m.setupMsg = "error: " + err.Error()
				break
			}
			// Everything the session was holding goes with it: the key lives in
			// the same file, and the tabs are full of answers that were true
			// for somebody else.
			m.sess = &session.Session{}
			m.client = api.New("")
			m.cache = make(map[tabID]any)
			m.keyStatus, m.keyAsked, m.keyCheck = nil, false, ""
			m.configured, m.setupMsg = nil, "signed out"
			m.err = nil
			return m, m.resumeSetup()

		// `s` and not `l`: l is already the vim spelling of "next tab".
		case "s":
			// Any tab, because the one a member is looking at when this is
			// needed is whichever they walked into and got told to sign in.
			if !m.showHelp && m.sess.Token == "" {
				return m, m.startLogin()
			}

		case "e":
			// From any tab. It used to do nothing at all anywhere but Setup,
			// which is the tab you have to already be on to know that - and
			// Home tells everyone to press `e` from Home.
			if !m.showHelp && m.loginStage == loginOff {
				return m, m.startKeyEdit()
			}
		case "c":
			if !m.showHelp && !m.configuring && (m.activeID() == tabSetup || m.wizard == wizardTools) {
				// Pressing the key that configures everything and having
				// nothing happen, with nothing said, is the worst of the
				// three possible answers.
				if m.sess.APIKey == "" {
					m.setupMsg = "error: set your API key first — press e"
					return m, nil
				}
				m.configuring = true
				m.configured = nil
				m.setupMsg = ""
				key, tools := m.sess.APIKey, m.sess.EnabledTools
				return m, tea.Batch(m.spin.Tick, func() tea.Msg {
					msg, written, failed := configureTools(key, tools)
					return configuredMsg{msg, written, failed}
				})
			}
		}
	}
	return m, nil
}

func (m *model) maybeLoad() tea.Cmd {
	id := m.activeID()
	// Setup asks the platform one thing, once, and stays usable while it
	// waits: no spinner, no error state, nothing that stops a member pasting
	// a key on a train.
	if id == tabSetup {
		// Nothing to ask on a machine that has not logged in: the call would
		// come back 401 and be swallowed.
		if m.keyAsked || m.sess.Token == "" {
			return nil
		}
		m.keyAsked = true
		return m.fetchKeyStatus()
	}
	if id == tabHome || id == tabAbout {
		return nil
	}
	// Costs tab derives from usage data — load that if needed
	if id == tabCosts {
		if _, ok := m.cache[tabUsage]; ok {
			return nil
		}
		m.loading = true
		m.err = nil
		return tea.Batch(m.spin.Tick, m.fetchTab(tabUsage))
	}
	if _, ok := m.cache[id]; ok {
		return nil
	}
	m.loading = true
	m.err = nil
	return tea.Batch(m.spin.Tick, m.fetchTab(id))
}

// Whether the account has a key, and what it is called. Never the key
// itself: the platform hands that over once, at creation, and will not
// repeat it - so the Setup tab cannot fill the field in for a member, only
// tell them there is one to go and copy.
// Signing in from inside the panel.
//
// The flow used to be: read Home, quit the panel, run `nan auth login`, answer
// two prompts, start the panel again. Every one of those steps is somewhere to
// get stuck, and the prompts are the worst of them - they are a bare
// fmt.Print on stdin, and a terminal that renders a command as a block, or
// eats the Enter that would answer them, leaves a member with no way forward
// that reading the screen would show.
//
// The panel already owns the keyboard and already has a text input for the API
// key. So it asks the same two questions here, where the keystrokes certainly
// arrive, and the member never leaves the thing they just opened.
func (m *model) startLogin() tea.Cmd {
	m.wizard = wizardEmail
	m.loginStage = loginAskEmail
	m.loginMsg = ""
	m.loginInput.SetValue("")
	m.loginInput.Placeholder = "you@example.com"
	m.loginInput.Prompt = "Email: "
	return m.loginInput.Focus()
}

// startKeyEdit opens the API key field, moving to the tab that shows it.
func (m *model) startKeyEdit() tea.Cmd {
	if m.wizard != wizardOff {
		m.wizard = wizardKey
	}
	if m.activeID() != tabSetup {
		m.active = tabIndex(tabSetup)
		m.scrollY = 0
	}
	m.editingKey = true
	m.keyInput.SetValue("")
	m.setupMsg = ""
	return m.keyInput.Focus()
}

// resumeSetup asks for the first thing that is missing, and for nothing when
// nothing is.
//
// It runs when the panel opens and again after each step finishes, which is
// what turns three separate keys into one sequence: a fresh machine is asked
// for an account, then for a key, then left on the tab that configures the
// tools. Before this, all three were things the member had to know to press.
//
// It is a nudge and not a gate. Esc leaves any of them, and a member who came
// to look at their usage is not held hostage by a setup they did not ask for -
// the Setup tab has always worked with no session at all, and that stays true.
func (m *model) resumeSetup() tea.Cmd {
	switch {
	case m.sess.Token == "":
		return m.startLogin()
	case m.sess.APIKey == "":
		return m.startKeyEdit()
	}
	return nil
}

func (m *model) cancelLogin() {
	m.wizard = wizardOff
	m.loginStage = loginOff
	m.loginBusy = false
	m.loginInput.Blur()
	m.loginInput.SetValue("")
	m.loginMsg = ""
}

func sendLink(email string) tea.Cmd {
	return func() tea.Msg { return linkSentMsg{auth.RequestSignInLink(email)} }
}

func signIn(pasted string) tea.Cmd {
	return func() tea.Msg {
		token, err := auth.TokenFromLink(strings.TrimSpace(pasted))
		if err != nil {
			return signedInMsg{err}
		}
		sessionToken, err := auth.ExchangeToken(token)
		if err != nil {
			return signedInMsg{err}
		}
		current, err := session.Load()
		if err != nil {
			current = &session.Session{}
		}
		current.Token = sessionToken
		if err := session.Save(current); err != nil {
			return signedInMsg{err}
		}
		return signedInMsg{nil}
	}
}

func (m model) fetchKeyStatus() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		status, err := client.GetKeyStatus()
		if err != nil {
			// Silent on purpose: this is a hint, and a member with no
			// connection still has a Setup tab that works.
			return keyStatusMsg{nil}
		}
		return keyStatusMsg{status}
	}
}

// The one check that catches a mistyped key before it is copied into every
// tool on the machine. /v1/models is the endpoint the key itself opens, so
// a 401 here is exactly the 401 the tools would hit later.
func checkKey(apiKey string) tea.Cmd {
	return func() tea.Msg {
		ids, err := api.ListModels(apiKey)
		if err != nil {
			return keyCheckedMsg{err: err}
		}
		return keyCheckedMsg{models: len(ids)}
	}
}

// Whether this tab has nothing to ask for until the member logs in.
//
// Every tab below Home asks the platform for something, and the platform
// answers a request with no session `unauthorized`. That word reached the
// screen verbatim: a fresh install drew "unauthorized" over three of its six
// tabs, which says what the server decided and not one word about what to do
// about it. Reported from a real first run on Windows.
//
// The Models tab is the exception. An API key opens /v1/models on its own, so
// a member who pasted a key into Setup and never logged in still gets that
// one - which is the point of Setup working without a session at all.
func (m model) needsLogin(id tabID) bool {
	if m.sess.Token != "" {
		return false
	}
	if id == tabModels && m.sess.APIKey != "" {
		return false
	}
	return id != tabHome && id != tabAbout && id != tabSetup
}

func (m model) fetchTab(id tabID) tea.Cmd {
	client := m.client
	apiKey := m.sess.APIKey
	needsLogin := m.needsLogin(id)
	return func() tea.Msg {
		if needsLogin {
			return fetchErrMsg{errNotSignedIn}
		}
		switch id {
		case tabProfile:
			data, err := client.GetMe()
			if err != nil {
				return fetchErrMsg{err}
			}
			return fetchedMsg{tab: tabProfile, data: data}
		case tabUsage:
			data, err := client.GetMetricsUsage()
			if err != nil {
				return fetchErrMsg{err}
			}
			return fetchedMsg{tab: tabUsage, data: data}
		case tabModels:
			// The ids a member can put in a request come from the inference
			// API, and it wants the API key rather than the session. Without
			// one there is still the platform's list, which answers with
			// deployment names: routing aliases and models on their way out.
			if apiKey != "" {
				ids, err := api.ListModels(apiKey)
				if err != nil {
					return fetchErrMsg{err}
				}
				return fetchedMsg{tab: tabModels, data: ids}
			}
			data, err := client.GetAgentsModels()
			if err != nil {
				return fetchErrMsg{err}
			}
			return fetchedMsg{tab: tabModels, data: data}
		}
		return nil
	}
}

// ── view ──────────────────────────────────────────────────────────────────────

const minWidth = 36

func (m model) View() string {
	l := m.lay

	if l.w < minWidth {
		return lipgloss.NewStyle().Foreground(cGray).
			Render(fmt.Sprintf("Terminal too narrow (%d cols, need %d+)", l.w, minWidth))
	}

	if m.showHelp {
		return lipgloss.Place(l.w, l.h, lipgloss.Center, lipgloss.Center, renderHelp())
	}

	var b strings.Builder
	if m.wizard == wizardOff {
		b.WriteString(renderTabBar(m.active, l) + "\n\n")
	} else {
		b.WriteString("\n")
	}

	// Content area height: total minus tab-bar, blank, blank-before-footer, footer
	contentH := l.h - 4
	if contentH < 1 {
		contentH = 1
	}

	// The guided setup owns the screen: no tab bar to wander off into, and the
	// banner at the top so the thing you just opened says what it is.
	if m.wizard != wizardOff {
		hint := "enter to continue   esc to leave setup"
		switch m.wizard {
		case wizardTools:
			hint = "↑/↓ pick   space toggle   c configure   esc to leave setup"
		case wizardDone:
			hint = "esc or enter to open the panel"
		}
		body := strings.Split(strings.TrimRight(m.renderWizard(l), "\n"), "\n")
		for len(body) < contentH {
			body = append(body, "")
		}
		if len(body) > contentH {
			body = body[:contentH]
		}
		b.WriteString(strings.Join(body, "\n"))
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(cGray).Render(l.indent+hint))
		return b.String()
	}

	var content string
	if m.loading {
		content = l.indent + m.spin.View() + " Loading...\n"
	} else if m.err != nil {
		content = lipgloss.NewStyle().Foreground(cRed).Render(l.indent+"✗  "+m.err.Error()) + "\n"
	} else {
		id := m.activeID()
		switch id {
		case tabCosts:
			if usageData, ok := m.cache[tabUsage]; ok {
				content = renderCosts(usageData.(map[string]any), l)
			}
		case tabHome:
			content = renderHome(l, m.sess.Token != "", m.sess.APIKey != "", m.mood())
		case tabAbout:
			content = renderAbout(l)
		case tabSetup:
			content = m.renderSetup(l)
		default:
			if data, ok := m.cache[id]; ok {
				switch id {
				case tabProfile:
					content = renderProfile(data.(map[string]any), l)
				case tabUsage:
					content = renderUsage(data.(map[string]any), l)
				case tabModels:
					usageData, _ := m.cache[tabUsage]
					content = renderModels(data, usageData, l)
				}
			}
		}
	}

	// Scroll viewport
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	maxScroll := len(lines) - contentH
	if maxScroll < 0 {
		maxScroll = 0
	}
	scrollY := m.scrollY
	if scrollY > maxScroll {
		scrollY = maxScroll
	}
	end := scrollY + contentH
	if end > len(lines) {
		end = len(lines)
	}
	visible := append([]string{}, lines[scrollY:end]...)
	for len(visible) < contentH {
		visible = append(visible, "")
	}
	b.WriteString(strings.Join(visible, "\n"))

	b.WriteString("\n")
	hint := "←/→ tabs   ↑/↓ scroll   r refresh   ? help   q quit"
	if l.w < 55 {
		hint = "←/→  ↑/↓  r  ?  q"
	} else if l.w < 72 {
		hint = "←/→ tabs  ↑/↓ scroll  r  ? help  q"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(cGray).Render(l.indent + hint))
	return b.String()
}

func renderTabBar(active int, l layout) string {
	narrow := l.w < 55
	var parts []string
	for i, td := range tabDefs {
		name := td.name
		if narrow {
			name = name[:3] // "Pro" "Usa" "Mod"
		}
		var s lipgloss.Style
		switch {
		case i == active:
			s = lipgloss.NewStyle().Bold(true).Foreground(cWhite).Background(cBlueDim).Padding(0, 1)
		case i == 0:
			s = lipgloss.NewStyle().Foreground(cCyan).Padding(0, 1)
		default:
			s = lipgloss.NewStyle().Foreground(cGray).Padding(0, 1)
		}
		parts = append(parts, s.Render(name))
	}
	sep := "  "
	if narrow {
		sep = " "
	}
	return strings.Join(parts, sep)
}

// ── profile renderer ──────────────────────────────────────────────────────────

// renderProfile also carries the sign-out, because the account screen is where
// somebody goes looking for it. It was only behind `?`, which is a place you
// find something in if you already suspect it is there.
func renderProfile(data map[string]any, l layout) string {
	var b strings.Builder
	kStyle := lipgloss.NewStyle().Foreground(cGray).Width(l.keyW)
	vStyle := lipgloss.NewStyle().Foreground(cText)

	for _, k := range sortedKeys(data) {
		v := data[k]
		if _, ok := v.(map[string]any); ok {
			continue
		}
		val := censor(k, fmt.Sprintf("%v", v))
		b.WriteString(l.indent + kStyle.Render(humanKey(k)+":") + vStyle.Render(val) + "\n")
	}

	b.WriteString("\n" + l.indent +
		lipgloss.NewStyle().Foreground(cCyan).Bold(true).Render("o") +
		lipgloss.NewStyle().Foreground(cGray).Render("  sign out of this account, twice to confirm") + "\n")
	return b.String()
}

// ── usage renderer ────────────────────────────────────────────────────────────

type modelStat struct {
	name         string
	inputTokens  float64
	outputTokens float64
}

type period struct {
	label       string
	totalTokens float64
	models      []modelStat
}

func renderUsage(data map[string]any, l layout) string {
	periods := []period{
		parsePeriod("Last 24 hours", data["last24h"]),
		parsePeriod("Last 30 days", data["last30d"]),
		parsePeriod("All time", data["allTime"]),
	}

	var b strings.Builder
	divider := lipgloss.NewStyle().Foreground(cDimGray).
		Render(l.indent + strings.Repeat("─", l.w-len(l.indent)*2))
	sTitle := lipgloss.NewStyle().Bold(true).Foreground(cWhite)
	sMuted := lipgloss.NewStyle().Foreground(cGray)

	first := true
	for _, p := range periods {
		if p.totalTokens == 0 {
			continue
		}
		if !first {
			b.WriteString(divider + "\n\n")
		}
		first = false

		b.WriteString(l.indent + sTitle.Render(p.label) +
			"   " + sMuted.Render(fmtTokens(p.totalTokens)+" total tokens") + "\n\n")

		for ci, ms := range p.models {
			pct := (ms.inputTokens + ms.outputTokens) / p.totalTokens * 100
			color := modelColors[ci%len(modelColors)]

			nameStyle := lipgloss.NewStyle().Foreground(cGray).Width(l.nameW)
			pctStyle := lipgloss.NewStyle().Foreground(color).Width(7)

			bar := renderBar(pct, l.barW, color)
			b.WriteString(l.indent + nameStyle.Render(truncate(ms.name, l.nameW)) +
				bar + " " + pctStyle.Render(fmt.Sprintf("%.1f%%", pct)) + "\n")

			if l.w >= 50 {
				sub := lipgloss.NewStyle().Foreground(cDimGray).
					Render(fmtTokens(ms.inputTokens) + " in  " + fmtTokens(ms.outputTokens) + " out")
				b.WriteString(l.indent + strings.Repeat(" ", l.nameW) + sub + "\n")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func parsePeriod(label string, raw any) period {
	obj, ok := raw.(map[string]any)
	if !ok {
		return period{label: label}
	}
	p := period{label: label, totalTokens: toFloat(obj["totalTokens"])}
	if list, ok := obj["byModel"].([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			p.models = append(p.models, modelStat{
				name:         fmt.Sprintf("%v", m["model"]),
				inputTokens:  toFloat(m["inputTokens"]),
				outputTokens: toFloat(m["outputTokens"]),
			})
		}
	}
	return p
}

func renderBar(pct float64, width int, color lipgloss.Color) string {
	filled := int(pct / 100.0 * float64(width))
	filled = clamp(filled, 0, width)
	fill := lipgloss.NewStyle().Background(color).Foreground(color).Render(strings.Repeat("█", filled))
	empty := lipgloss.NewStyle().Foreground(cDimGray).Render(strings.Repeat("░", width-filled))
	return fill + empty
}

// ── models renderer ───────────────────────────────────────────────────────────

type modelInfo struct {
	name      string
	mode      string
	tokens30d float64
	tokens24h float64
}

var modeLabel = map[string]string{
	"":                    "text generation",
	"audio_speech":        "audio speech",
	"audio_transcription": "audio transcription",
	"embedding":           "embedding",
}

var modeColor = map[string]lipgloss.TerminalColor{
	"":                    lipgloss.Color("#3B82F6"),
	"audio_speech":        lipgloss.Color("#8B5CF6"),
	"audio_transcription": lipgloss.Color("#F59E0B"),
	"embedding":           lipgloss.Color("#10B981"),
	// The kinds the catalogue uses, for the ids that come from /v1/models.
	"chat":           lipgloss.Color("#3B82F6"),
	"chat · premium": lipgloss.Color("#A78BFA"),
	"rerank":         lipgloss.Color("#10B981"),
	"text to speech": lipgloss.Color("#8B5CF6"),
	"speech to text": lipgloss.Color("#F59E0B"),
	"image":          lipgloss.Color("#EC4899"),
	// An id the cluster serves and this catalogue has never heard of. Worth
	// showing rather than hiding: that is how a model nobody documented gets
	// noticed.
	"unknown": lipgloss.Color("#71717A"),
}

// A colour for a mode this build does not know, so an id it has never seen
// still renders.
func badgeColor(mode string) lipgloss.TerminalColor {
	if c, ok := modeColor[mode]; ok {
		return c
	}
	return cGray
}

func renderModels(data any, usageData any, l layout) string {
	var models []modelInfo

	// []string is the id list from GET /v1/models: what goes in the `model`
	// field of a request, which is what a member came to this tab to copy.
	if ids, ok := data.([]string); ok {
		for _, id := range ids {
			mi := modelInfo{name: id, mode: "unknown"}
			if m, found := catalog.Get(id); found {
				mi.mode = string(m.Kind)
				if m.Premium {
					mi.mode += " · premium"
				}
			}
			models = append(models, mi)
		}
	} else {
		// The platform's list, when there is no API key to ask the other one.
		var raw []any
		switch v := data.(type) {
		case map[string]any:
			if list, ok := v["models"].([]any); ok {
				raw = list
			}
		case []any:
			raw = v
		}
		if raw == nil {
			return l.indent + lipgloss.NewStyle().Foreground(cGray).Render("No models found.") + "\n"
		}
		for _, item := range raw {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			mi := modelInfo{
				name: fmt.Sprintf("%v", obj["name"]),
				mode: fmt.Sprintf("%v", obj["mode"]),
			}
			if mi.mode == "<nil>" {
				mi.mode = ""
			}
			models = append(models, mi)
		}
	}
	if len(models) == 0 {
		return l.indent + lipgloss.NewStyle().Foreground(cGray).Render("No models found.") + "\n"
	}

	// Cross-reference with usage cache
	if usageRaw, ok := usageData.(map[string]any); ok {
		usageByModel := extractTokensByModel(usageRaw)
		for i := range models {
			models[i].tokens30d = usageByModel["30d"][models[i].name]
			models[i].tokens24h = usageByModel["24h"][models[i].name]
		}
	}

	// Sort: most used first, then alphabetical
	sort.Slice(models, func(i, j int) bool {
		if models[i].tokens30d != models[j].tokens30d {
			return models[i].tokens30d > models[j].tokens30d
		}
		return models[i].name < models[j].name
	})

	var b strings.Builder
	nameW := clamp(l.w/3, 12, 22)
	divider := lipgloss.NewStyle().Foreground(cDimGray).
		Render(l.indent + strings.Repeat("─", clamp(l.w-6, 10, 50)))

	for i, mi := range models {
		if i > 0 {
			b.WriteString(divider + "\n")
		}

		color := badgeColor(mi.mode)
		label, ok := modeLabel[mi.mode]
		if !ok {
			label = mi.mode
		}

		nameStyle := lipgloss.NewStyle().Bold(true).Foreground(cCyan).Width(nameW)
		badgeStyle := lipgloss.NewStyle().Foreground(color)
		dimStyle := lipgloss.NewStyle().Foreground(cDimGray)
		usageStyle := lipgloss.NewStyle().Foreground(cGray)

		// Row 1: name + mode badge
		b.WriteString(l.indent + nameStyle.Render(truncate(mi.name, nameW)) +
			"  " + badgeStyle.Render(label) + "\n")

		// Row 2: usage stats or "no recent usage"
		if mi.tokens30d > 0 {
			parts := []string{fmtTokens(mi.tokens30d) + " tokens (30d)"}
			if mi.tokens24h > 0 {
				parts = append(parts, fmtTokens(mi.tokens24h)+" (24h)")
			}
			b.WriteString(l.indent + strings.Repeat(" ", nameW) +
				"  " + usageStyle.Render(strings.Join(parts, "  ·  ")) + "\n")
		} else {
			b.WriteString(l.indent + strings.Repeat(" ", nameW) +
				"  " + dimStyle.Render("no recent usage") + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// extractTokensByModel builds maps of total tokens per model for each period.
func extractTokensByModel(usage map[string]any) map[string]map[string]float64 {
	out := map[string]map[string]float64{
		"24h": {},
		"30d": {},
	}
	periods := map[string]string{"last24h": "24h", "last30d": "30d"}
	for key, label := range periods {
		if p, ok := usage[key].(map[string]any); ok {
			if list, ok := p["byModel"].([]any); ok {
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						name := fmt.Sprintf("%v", m["model"])
						out[label][name] = toFloat(m["inputTokens"]) + toFloat(m["outputTokens"])
					}
				}
			}
		}
	}
	return out
}

// ── cost comparison renderer ──────────────────────────────────────────────────

type providerPricing struct {
	model    string
	provider string
	inPer1M  float64 // $ per 1M input tokens
	outPer1M float64 // $ per 1M output tokens
}

// Per 1M tokens, read off each vendor's own pricing page on 2026-09-14.
//
// Ten rows rather than the six this started as, and the reason is the spread
// rather than the count. The tab multiplies a member's NaN token usage by each
// of these, so a table that carried only mid-range models answered only the
// mid-range question. From Luna at $0.20 in to Astra and Fable at $10, a
// reader can find the row that matches what they would actually have reached
// for instead of taking ours as the comparison.
//
// The six this replaced were five-sixths wrong, and all five in the same
// direction - the output price too low. GPT-5.5 was published at $20 against a
// real $30, Gemini 3.1 Pro at $8 against $12, Gemini 2.5 Flash at $0.35/$1.05
// against $0.30/$2.50, and "GPT-5.4 Mini" was not a model anyone sells. A tab
// whose whole claim is "this is what you would have paid elsewhere"
// understating every competitor is the one direction it must not be wrong in.
//
// Two rows carry a condition the table cannot express, so each is taken at its
// lowest published rate and the comparison stays conservative: Gemini 3.1 Pro
// costs $4/$18 above a 200k-token prompt, and Gemini 3.8 Flash is on a
// promotional rate that doubles on 2027-01-01. TestGeminiFlashPromoHasNotExpired
// fails on that date so the number is changed rather than forgotten.
var pricingTable = []providerPricing{
	{"Claude Fable 5.1", "Anthropic", 10.00, 50.00},
	{"Claude Opus 5", "Anthropic", 5.00, 25.00},
	{"Claude Sonnet 5", "Anthropic", 2.00, 10.00},
	{"Claude Haiku 4.5", "Anthropic", 1.00, 5.00},
	{"GPT-6 Astra", "OpenAI", 10.00, 50.00},
	{"GPT-5.6 Sol", "OpenAI", 4.00, 20.00},
	{"GPT-5.6 Terra", "OpenAI", 2.00, 12.00},
	{"GPT-5.6 Luna", "OpenAI", 0.20, 1.20},
	{"Gemini 3.1 Pro", "Google", 2.00, 12.00},
	{"Gemini 3.8 Flash", "Google", 0.75, 3.75},
}

// The day Gemini 3.8 Flash stops being half price.
var geminiFlashPromoEnds = time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)

var providerColor = map[string]lipgloss.TerminalColor{
	"Anthropic": lipgloss.Color("#F97316"),
	"OpenAI":    lipgloss.Color("#10B981"),
	"Google":    lipgloss.Color("#3B82F6"),
}

type periodTokens struct {
	input  float64
	output float64
}

func sumTokens(usage map[string]any, key string) periodTokens {
	p, ok := usage[key].(map[string]any)
	if !ok {
		return periodTokens{}
	}
	list, _ := p["byModel"].([]any)
	var pt periodTokens
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			pt.input += toFloat(m["inputTokens"])
			pt.output += toFloat(m["outputTokens"])
		}
	}
	return pt
}

func calcCost(pt periodTokens, p providerPricing) float64 {
	return pt.input/1_000_000*p.inPer1M + pt.output/1_000_000*p.outPer1M
}

func fmtCost(v float64) string {
	// Go has no `%,` verb: that format printed `$%!,(float64=1234.5).2f` on
	// every figure over a thousand, which on the Costs tab is most of them.
	if v >= 1000 {
		return fmtCostAligned(v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// fmtCostAligned formats a dollar amount with comma separators.
func fmtCostAligned(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	// insert thousands separators
	dotIdx := len(s) - 3
	intPart := s[:dotIdx]
	dec := s[dotIdx:]
	var out []byte
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return "$" + string(out) + dec
}

func renderCosts(usage map[string]any, l layout) string {
	t24 := sumTokens(usage, "last24h")
	t30 := sumTokens(usage, "last30d")
	tall := sumTokens(usage, "allTime")
	sameAsTotal := tall.input == t30.input && tall.output == t30.output
	showAll := l.w >= 72 && !sameAsTotal

	// ── pre-compute all cell strings (plain text, for width measurement) ──
	type row struct {
		plainLabel  string // "Claude Sonnet 4.6  Anthropic" — no ANSI
		styledLabel string // same text but colored
		c24         string // "$38.21"
		c30         string // "$876.11"
		call        string // "$876.11"
	}

	// Below this the provider word is dropped: it is the widest part of the
	// label and the least load-bearing, because each provider already has its
	// own colour and its own block of rows. Keeping it is what ran the table
	// off the side of a split pane.
	showProvider := l.w >= 72

	rows := make([]row, len(pricingTable))
	for i, p := range pricingTable {
		pColor, ok := providerColor[p.provider]
		if !ok {
			pColor = cGray
		}
		plain := p.model
		styled := lipgloss.NewStyle().Foreground(pColor).Bold(true).Render(p.model)
		if showProvider {
			plain = p.model + "  " + p.provider
			styled = lipgloss.NewStyle().Foreground(cWhite).Bold(true).Render(p.model) +
				"  " + lipgloss.NewStyle().Foreground(pColor).Render(p.provider)
		}
		rows[i] = row{
			plainLabel:  plain,
			styledLabel: styled,
			c24:         fmtCostAligned(calcCost(t24, p)),
			c30:         fmtCostAligned(calcCost(t30, p)),
			call:        fmtCostAligned(calcCost(tall, p)),
		}
	}

	// ── measure column widths from plain text ──
	nameW := len("PROVIDER")
	c24W := len("24H")
	c30W := len("30 DAYS")
	callW := len("ALL TIME")
	for _, r := range rows {
		if len(r.plainLabel) > nameW {
			nameW = len(r.plainLabel)
		}
		if len(r.c24) > c24W {
			c24W = len(r.c24)
		}
		if len(r.c30) > c30W {
			c30W = len(r.c30)
		}
		if len(r.call) > callW {
			callW = len(r.call)
		}
	}
	nameW += 2 // right margin before first cost column

	// ── styles ──
	hStyle := lipgloss.NewStyle().Foreground(cGray).Bold(true)
	numStyle := lipgloss.NewStyle().Foreground(cGray)
	totalStyle := lipgloss.NewStyle().Foreground(cWhite).Bold(true)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(cWhite)
	subStyle := lipgloss.NewStyle().Foreground(cGray)

	rpad := func(s string, w int) string {
		p := w - len(s)
		if p < 0 {
			p = 0
		}
		return strings.Repeat(" ", p) + s
	}
	lpad := func(s string, w int) string {
		p := w - len(s)
		if p < 0 {
			p = 0
		}
		return s + strings.Repeat(" ", p)
	}

	var b strings.Builder

	// Header
	b.WriteString(l.indent + titleStyle.Render("COST COMPARISON") + "\n")
	// Measured rather than guessed: the long line is 75 columns and was
	// switched on from 72 up, so the width that turned it on was also the
	// width it ran off. Longest first, and the first one that fits wins.
	subtitle := ""
	for _, candidate := range []string{
		"Estimated cost based on your actual input/output tokens on other providers.",
		"Estimated cost of your usage on other providers.",
		"Estimated cost elsewhere.",
	} {
		subtitle = candidate
		if lipgloss.Width(candidate)+lipgloss.Width(l.indent) <= l.w {
			break
		}
	}
	b.WriteString(l.indent + subStyle.Render(subtitle) + "\n\n")

	// Header row
	header := hStyle.Render(lpad("PROVIDER", nameW))
	header += "  " + hStyle.Render(rpad("24H", c24W))
	header += "  " + hStyle.Render(rpad("30 DAYS", c30W))
	if showAll {
		header += "  " + hStyle.Render(rpad("ALL TIME", callW))
	}
	b.WriteString(l.indent + header + "\n")

	divW := nameW + 2 + c24W + 2 + c30W
	if showAll {
		divW += 2 + callW
	}
	b.WriteString(l.indent + lipgloss.NewStyle().Foreground(cDimGray).
		Render(strings.Repeat("─", divW)) + "\n\n")

	// Data rows — pad label using plain-text length, then right-align costs.
	//
	// One line each, with a blank line only where the provider changes. This
	// used to put a blank line between every row, which read fine over six and
	// stopped being a table at ten: two screens of alternating text and gap,
	// impossible to run an eye down. The gaps that are left do some work -
	// they group the rows by who charges them.
	for i, r := range rows {
		if i > 0 && pricingTable[i].provider != pricingTable[i-1].provider {
			b.WriteString("\n")
		}
		labelPad := nameW - len(r.plainLabel)
		if labelPad < 0 {
			labelPad = 0
		}
		line := r.styledLabel + strings.Repeat(" ", labelPad)
		line += "  " + numStyle.Render(rpad(r.c24, c24W))
		if showAll {
			line += "  " + numStyle.Render(rpad(r.c30, c30W))
			line += "  " + totalStyle.Render(rpad(r.call, callW))
		} else {
			line += "  " + totalStyle.Render(rpad(r.c30, c30W))
		}
		b.WriteString(l.indent + line + "\n")
	}
	b.WriteString("\n")

	// Footer — measure plain text width first to avoid border miscalculation
	const notePlain = "NaN — Your usage is included in your membership. No per-token charges."
	nanStyle := lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	note := nanStyle.Render("NaN") +
		lipgloss.NewStyle().Foreground(cGray).Render(" — Your usage is included in your membership. No per-token charges.")
	// Not len(): the em dash is one column and three bytes, so counting bytes
	// drew the box two columns wider than its own text. And not the text width
	// alone either: at 70 columns of content plus a border and the indent, the
	// box ran off the side of any terminal narrower than about 74, which is
	// where this tab gets read on half a laptop screen. The text wraps instead.
	noteW := lipgloss.Width(notePlain)
	if fits := l.w - lipgloss.Width(l.indent) - 4; noteW > fits {
		noteW = fits
	}
	if noteW < 8 {
		noteW = 8
	}
	noteStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlueDim).
		Padding(0, 1).
		Width(noteW)
	b.WriteString(indentBlock(noteStyle.Render(note), l.indent) + "\n")

	return b.String()
}

// ── censoring ─────────────────────────────────────────────────────────────────

var sensitivePatterns = []string{
	"id", "token", "secret", "key", "password",
	"email", "mail", "session", "auth", "hash",
	"discord", "guild", "user",
}

func isSensitive(key string) bool {
	lower := strings.ToLower(key)
	for _, p := range sensitivePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func censor(key, value string) string {
	if !isSensitive(key) {
		return value
	}
	const keep = 6
	runes := []rune(value)
	if len(runes) <= keep {
		return "..."
	}
	return string(runes[:keep]) + "..."
}

// ── helpers ───────────────────────────────────────────────────────────────────

func fmtTokens(n float64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", n/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", n/1_000)
	default:
		return fmt.Sprintf("%.0f", n)
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if max < 4 || len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func humanKey(s string) string {
	runes := []rune(s)
	var out []rune
	for i, r := range runes {
		// A space before every capital turns userUUID into "User U U I D",
		// which is the first line of the first tab. A run of capitals is one
		// word, and it ends where a lowercase letter starts it a new one.
		if i > 0 && isUpper(r) {
			startsWord := !isUpper(runes[i-1])
			endsRun := i+1 < len(runes) && !isUpper(runes[i+1])
			if startsWord || endsRun {
				out = append(out, ' ')
			}
		}
		out = append(out, r)
	}
	s = string(out)
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ── setup renderer ───────────────────────────────────────────────────────────

type toolInfo struct {
	name        string
	binary      string
	configPath  string
	installPath string // optional dir to check for installation (overrides configPath check)
	installed   bool
	configured  bool
}

func detectTools() []toolInfo {
	home, _ := os.UserHomeDir()
	candidates := []toolInfo{
		{
			name:       "Factory AI",
			binary:     "droid",
			configPath: filepath.Join(home, ".factory", "settings.json"),
		},
		{
			name:       "OpenCode",
			binary:     "opencode",
			configPath: filepath.Join(home, ".config", "opencode", "opencode.json"),
		},
		{
			name:        "Pi",
			binary:      "pi",
			configPath:  filepath.Join(home, ".pi", "agent", "models.json"),
			installPath: filepath.Join(home, ".pi"),
		},
		{
			name:       "Codex",
			binary:     "codex",
			configPath: filepath.Join(home, ".codex", "config.toml"),
		},
		{
			// Not a coding agent like the rest: it lives in the member's
			// messaging channels. It is here because connecting it is the same
			// two values, and because it is the one tool that tells us where
			// its config is instead of making us guess.
			name:        "Hermes",
			binary:      "hermes",
			configPath:  hermesConfigPath(hermesHome()),
			installPath: hermesHome(),
		},
	}
	for i := range candidates {
		_, binErr := exec.LookPath(candidates[i].binary)
		checkPath := candidates[i].configPath
		if candidates[i].installPath != "" {
			checkPath = candidates[i].installPath
		}
		_, pathErr := os.Stat(checkPath)
		candidates[i].installed = binErr == nil || pathErr == nil
		if candidates[i].installed {
			candidates[i].configured = isNaNConfigured(candidates[i].name, candidates[i].configPath)
		}
	}
	return candidates
}

func isNaNConfigured(toolName, cfgPath string) bool {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	switch toolName {
	case "Factory AI":
		var cfg struct {
			CustomModels []map[string]any `json:"customModels"`
		}
		if json.Unmarshal(data, &cfg) != nil {
			return false
		}
		for _, m := range cfg.CustomModels {
			if base, _ := m["baseUrl"].(string); strings.Contains(base, "api.nan.builders") {
				return true
			}
		}
		return false
	case "OpenCode":
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) != nil {
			return false
		}
		providers, _ := cfg["provider"].(map[string]any)
		nan, _ := providers["nan"].(map[string]any)
		opts, _ := nan["options"].(map[string]any)
		base, _ := opts["baseURL"].(string)
		return strings.Contains(base, "api.nan.builders")
	case "Pi":
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) != nil {
			return false
		}
		providers, _ := cfg["providers"].(map[string]any)
		nan, _ := providers["nan"].(map[string]any)
		base, _ := nan["baseUrl"].(string)
		return strings.Contains(base, "api.nan.builders")
	case "Codex":
		return strings.Contains(string(data), "api.nan.builders")
	}
	return strings.Contains(string(data), "api.nan.builders")
}

func (m model) toolEnabled(name string) bool {
	if m.sess.EnabledTools == nil {
		return true
	}
	if v, ok := m.sess.EnabledTools[name]; ok {
		return v
	}
	return true
}

// Writes the configs, and says which tools it wrote so the tab can tell a
// member how to use each one.
//
// This runs off the event loop. It used to be called straight out of the key
// handler, which meant the panel sat frozen for as long as it took - and with
// Hermes in the list that is four processes, several seconds, with no repaint
// and no key accepted. Reported as "se ha quedado paralizado y no sabia que
// pasaba", which is the only thing it could look like.
// How many of the tools it knows about are on this machine.
func installedTools() int {
	n := 0
	for _, t := range detectTools() {
		if t.installed {
			n++
		}
	}
	return n
}

// What went wrong for one tool, kept apart from the others so a failure in one
// does not read as a failure in all of them.
type toolFailure struct {
	name   string
	reason string
}

// firstLine of a reason, for a summary. A tool that is configured by running
// it - Hermes - hands back whatever it printed, which can be a paragraph.
func (f toolFailure) firstLine() string {
	if i := strings.IndexAny(f.reason, "\r\n"); i >= 0 {
		return strings.TrimSpace(f.reason[:i])
	}
	return strings.TrimSpace(f.reason)
}

func configureTools(apiKey string, enabledTools map[string]bool) (string, []string, []toolFailure) {
	isEnabled := func(name string) bool {
		if enabledTools == nil {
			return true
		}
		if v, ok := enabledTools[name]; ok {
			return v
		}
		return true
	}
	tools := detectTools()
	var written []string
	var failed []toolFailure
	added, removed := 0, 0
	for _, t := range tools {
		if !t.installed {
			continue
		}
		if isEnabled(t.name) {
			var err error
			switch t.name {
			case "Factory AI":
				err = writeFactoryConfig(t.configPath, apiKey)
			case "OpenCode":
				err = writeOpencodeConfig(t.configPath, apiKey)
			case "Pi":
				err = writePiConfig(t.configPath, apiKey)
			case "Codex":
				err = writeCodexConfig(t.configPath, apiKey)
			case "Hermes":
				err = writeHermesConfig(filepath.Dir(t.configPath), apiKey)
			}
			if err != nil {
				failed = append(failed, toolFailure{t.name, err.Error()})
			} else {
				added++
				written = append(written, t.name)
			}
		} else if t.configured {
			var err error
			switch t.name {
			case "Factory AI":
				err = removeFactoryConfig(t.configPath)
			case "OpenCode":
				err = removeOpencodeConfig(t.configPath)
			case "Pi":
				err = removePiConfig(t.configPath)
			case "Codex":
				err = removeCodexConfig(t.configPath)
			case "Hermes":
				err = removeHermesConfig(filepath.Dir(t.configPath))
			}
			if err != nil {
				failed = append(failed, toolFailure{t.name, err.Error()})
			} else {
				removed++
			}
		}
	}
	// One tool failing used to throw away everything that worked: the message
	// became "error: <whatever the last one said>" and the four configs that
	// had just been written went unmentioned. A member with a broken Hermes
	// was told the whole step had failed, and left on it with nothing to do
	// but escape.
	parts := []string{}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", removed))
	}
	for _, f := range failed {
		parts = append(parts, f.name+" failed")
	}
	if len(parts) == 0 {
		return "nothing to sync", written, nil
	}
	return strings.Join(parts, "  ·  "), written, failed
}

func factoryCustomID(displayName string, index int) string {
	return fmt.Sprintf("custom:%s-%d", strings.ReplaceAll(displayName, " ", "-"), index)
}

func writeFactoryConfig(cfgPath, apiKey string) error {
	// Use map to preserve unknown top-level fields (logoAnimation, etc.)
	var cfg map[string]any
	if data, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	// Extract existing customModels
	var models []map[string]any
	if raw, ok := cfg["customModels"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				models = append(models, m)
			}
		}
	}

	// Index NaN model IDs already present
	existingIDs := map[string]bool{}
	for _, m := range models {
		if base, _ := m["baseUrl"].(string); strings.Contains(base, "api.nan.builders") {
			if id, _ := m["model"].(string); id != "" {
				existingIDs[id] = true
			}
		}
	}

	// Append only missing models
	added := false
	for _, nm := range catalog.ChatModels() {
		if !existingIDs[nm.ID] {
			idx := len(models)
			display := nm.Name + " (NaN)"
			models = append(models, map[string]any{
				"model":          nm.ID,
				"id":             factoryCustomID(display, idx),
				"index":          idx,
				"baseUrl":        "https://api.nan.builders/v1",
				"apiKey":         apiKey,
				"displayName":    display,
				"noImageSupport": !nm.Accepts(catalog.InputImage),
				"provider":       "openai",
			})
			added = true
		}
	}
	if !added {
		return nil
	}
	cfg["customModels"] = models

	// Leave it pointing at the model the quickstart starts everyone with. It
	// used to be qwen3.6, which the docs now call the previous generation.
	if _, ok := cfg["sessionDefaultSettings"]; !ok {
		for _, m := range models {
			if id, _ := m["model"].(string); id == catalog.Default {
				cfg["sessionDefaultSettings"] = map[string]any{
					"model":           m["id"],
					"reasoningEffort": "none",
					"autonomyMode":    "normal",
				}
				break
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0o600)
}

func writeOpencodeConfig(cfgPath, apiKey string) error {
	var cfg map[string]any
	if data, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	// `limit` is not decoration: without it opencode falls back to its own
	// guess at the window and compacts a 1M-token session as if it were a
	// small one. An unknown key (this used to be written as `contextWindow`)
	// raises nothing anyone sees, which is why nan.builders/docs/opencode
	// spells the field out and why this writes it.
	nanModels := map[string]any{}
	for _, m := range catalog.ChatModels() {
		nanModels[m.ID] = map[string]any{
			"name": m.Name,
			"limit": map[string]any{
				"context": m.Context,
				"output":  m.Output,
			},
			"modalities": map[string]any{
				"input":  m.Inputs,
				"output": []string{"text"},
			},
		}
	}

	if nan, ok := providers["nan"].(map[string]any); ok {
		if opts, ok := nan["options"].(map[string]any); ok {
			if base, _ := opts["baseURL"].(string); strings.Contains(base, "api.nan.builders") {
				// Already configured — merge any missing models
				existing, _ := nan["models"].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				changed := false
				for id, m := range nanModels {
					found, ok := existing[id].(map[string]any)
					if !ok {
						existing[id] = m
						changed = true
						continue
					}
					// An entry written by an older version of this CLI has no
					// `limit` at all. Filling it in is the only way those
					// configs ever stop compacting early; anything the member
					// set themselves is left exactly as it is.
					for _, field := range []string{"limit", "modalities"} {
						if _, present := found[field]; !present {
							found[field] = m.(map[string]any)[field]
							changed = true
						}
					}
				}
				if !changed {
					return nil
				}
				nan["models"] = existing
				providers["nan"] = nan
				cfg["provider"] = providers
				data, err := json.MarshalIndent(cfg, "", "  ")
				if err != nil {
					return err
				}
				return os.WriteFile(cfgPath, data, 0o600)
			}
		}
	}

	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	providers["nan"] = map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "NaN",
		"options": map[string]any{
			"baseURL": "https://api.nan.builders/v1",
			"apiKey":  apiKey,
		},
		"models": nanModels,
	}
	cfg["provider"] = providers

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0o600)
}

// Pi takes a provider two ways: a models.json, which is data, or an extension
// that calls pi.registerProvider, which is code. Both are current in 0.85.1.
// This used to write the extension - a TypeScript file Pi loads and runs - and
// now writes the data, which is what nan.builders/docs/pi publishes: Pi
// validates models.json against its own schema and names the field that is
// wrong, nothing this CLI writes has to execute on a member's machine, and the
// two ways of setting Pi up stop being two things to keep in step.
func writePiConfig(cfgPath, apiKey string) error {
	// Unlike the extension file, models.json is shared: other providers live in
	// it and none of them are ours to touch.
	var cfg map[string]any
	if data, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	providers, _ := cfg["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	models := make([]map[string]any, 0, len(catalog.ChatModels()))
	for _, m := range catalog.ChatModels() {
		models = append(models, map[string]any{
			"id":            m.ID,
			"name":          m.Name,
			"input":         piInputs(m),
			"reasoning":     m.Reasoning,
			"contextWindow": m.Context,
			"maxTokens":     m.Output,
		})
	}

	providers["nan"] = map[string]any{
		"name":    "NaN",
		"baseUrl": "https://api.nan.builders/v1",
		"apiKey":  apiKey,
		"api":     "openai-completions",
		"compat":  map[string]any{"supportsDeveloperRole": true},
		"models":  models,
	}
	cfg["providers"] = providers

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return err
	}
	return writePiDefaults(piSettingsPath(cfgPath))
}

// Pi reads the provider it calls from a second file, and until this existed
// the CLI wrote only the first one. nan.builders/docs/pi marks this step "not
// optional" for a reason: with models.json alone Pi goes on calling its
// factory provider, and what the member sees is a 401 that names neither file.
func writePiDefaults(settingsPath string) error {
	var settings map[string]any
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}

	// A member who already picked a default picked it, and ours is one more
	// provider they can switch to from inside Pi. This only fills the gap that
	// leaves a fresh install calling nothing.
	if _, chosen := settings["defaultProvider"]; chosen {
		return nil
	}
	settings["defaultProvider"] = "nan"
	settings["defaultModel"] = catalog.Coding

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o600)
}

// settings.json sits next to models.json in Pi's agent directory.
func piSettingsPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "settings.json")
}

// Pi's schema takes "text" and "image" and nothing else, so mimo-v2.5 goes in
// without its audio: a third value fails validation, and Pi then refuses the
// whole file with every other provider in it.
func piInputs(m catalog.Model) []string {
	out := make([]string, 0, len(m.Inputs))
	for _, in := range m.Inputs {
		if in == catalog.InputText || in == catalog.InputImage {
			out = append(out, in)
		}
	}
	return out
}

// Deleting the file is no longer an option: models.json is where every Pi
// provider lives, and the extension it replaced was a file of ours alone.
func removePiConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	providers, _ := cfg["providers"].(map[string]any)
	if _, ours := providers["nan"]; !ours {
		return nil
	}
	delete(providers, "nan")

	// A default pointing at a provider that is no longer in models.json is
	// worse than no default: Pi starts and fails on the first message.
	if err := removePiDefaults(piSettingsPath(cfgPath)); err != nil {
		return err
	}

	// A models.json with nothing left in it is not a config Pi needs to read.
	if len(providers) == 0 && len(cfg) == 1 {
		return os.Remove(cfgPath)
	}
	cfg["providers"] = providers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0o600)
}

// Only the default we wrote, and only while it still points at us: anything
// the member set themselves is theirs.
func removePiDefaults(settingsPath string) error {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}
	var settings map[string]any
	if json.Unmarshal(data, &settings) != nil {
		return nil
	}
	if settings["defaultProvider"] != "nan" {
		return nil
	}
	delete(settings, "defaultProvider")
	delete(settings, "defaultModel")

	// The file existed only to hold our default, so it goes with it.
	if len(settings) == 0 {
		return os.Remove(settingsPath)
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o600)
}

func writeCodexConfig(cfgPath, apiKey string) error {
	data, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data), "api.nan.builders") {
		return nil
	}

	// wire_api = "chat", not "responses": the cluster's /responses endpoint
	// emits a single terminal event, so with "responses" the whole answer
	// appears at once at the end instead of streaming.
	codexModel, _ := catalog.Get(catalog.Coding)

	// If no existing config, write a complete starter config.
	if len(data) == 0 {
		content := fmt.Sprintf(`model = %q
model_provider = "nan"
model_context_window = %d

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "chat"
`, codexModel.ID, codexModel.Context, apiKey)
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			return err
		}
		return os.WriteFile(cfgPath, []byte(content), 0o600)
	}

	// Existing config: only append the provider section; preserve user's model/provider choices.
	// model_context_window suppresses the "metadata not found" warning for nan models.
	section := fmt.Sprintf(`
model_context_window = %d

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "chat"
`, codexModel.Context, apiKey)
	content := strings.TrimRight(string(data), "\n") + "\n" + section
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, []byte(content), 0o600)
}

// ── Hermes ───────────────────────────────────────────────────────────────────
//
// Every other tool here is configured by writing its file. Hermes is not,
// because its config.yaml is a long commented document the member also edits
// by hand, and because it ships the command to do it: `hermes config set`
// writes a key without flattening the comments around it and rejects a key it
// does not know. Reproducing that schema in Go would buy nothing and would
// start drifting the day Hermes moves a field.
//
// Two things the tool knows and the docs page does not say. The path in
// nan.builders/docs/hermes is the Unix one; on Windows Hermes keeps its home
// under LOCALAPPDATA. And with the `custom` provider Hermes asks the endpoint
// what it serves, so there is no model list to write here and none to keep in
// step with the cluster - only which model to open with.

// Swapped in tests, so nothing here spawns a process or goes near the
// member's own Hermes. HERMES_HOME is passed on every call rather than
// inherited: the writer configures the install it detected, not whichever one
// the environment happens to point at.
var runHermesConfig = func(home string, args ...string) error {
	cmd := exec.Command("hermes", append([]string{"config"}, args...)...)
	cmd.Env = append(os.Environ(), "HERMES_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hermes config %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// The resolution Hermes itself uses: HERMES_HOME, then the platform default.
func hermesHome() string {
	if home := strings.TrimSpace(os.Getenv("HERMES_HOME")); home != "" {
		return home
	}
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Join(local, "hermes")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Local", "hermes")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hermes")
}

func hermesConfigPath(home string) string {
	return filepath.Join(home, "config.yaml")
}

func writeHermesConfig(home, apiKey string) error {
	settings := [][2]string{
		{"model.provider", "custom"},
		{"model.base_url", "https://api.nan.builders/v1"},
		{"model.api_key", apiKey},
		{"model.default", catalog.Coding},
	}
	for _, s := range settings {
		if err := runHermesConfig(home, "set", s[0], s[1]); err != nil {
			return err
		}
	}
	return nil
}

// The same four keys and nothing else: a member's channels, skills and
// persona live in this file too.
func removeHermesConfig(home string) error {
	for _, key := range []string{"model.api_key", "model.base_url", "model.default", "model.provider"} {
		if err := runHermesConfig(home, "unset", key); err != nil {
			return err
		}
	}
	return nil
}

func removeCodexConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	inNanSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[model_providers.nan]" {
			inNanSection = true
			continue
		}
		if inNanSection {
			// End of the nan section when a new section header appears.
			if strings.HasPrefix(trimmed, "[") {
				inNanSection = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	if result == "\n" {
		return os.Remove(cfgPath)
	}
	return os.WriteFile(cfgPath, []byte(result), 0o600)
}

func removeFactoryConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	raw, _ := cfg["customModels"].([]any)
	var filtered []map[string]any
	var removedIDs []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if base, _ := m["baseUrl"].(string); strings.Contains(base, "api.nan.builders") {
			if id, _ := m["id"].(string); id != "" {
				removedIDs = append(removedIDs, id)
			}
		} else {
			filtered = append(filtered, m)
		}
	}
	if len(removedIDs) == 0 {
		return nil
	}
	cfg["customModels"] = filtered

	// Clear sessionDefaultSettings.model if it pointed to a removed NaN model
	if sds, ok := cfg["sessionDefaultSettings"].(map[string]any); ok {
		if model, _ := sds["model"].(string); model != "" {
			for _, rid := range removedIDs {
				if model == rid {
					delete(sds, "model")
					cfg["sessionDefaultSettings"] = sds
					break
				}
			}
		}
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0o600)
}

func removeOpencodeConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		return nil
	}
	if _, ok := providers["nan"]; !ok {
		return nil // nothing to remove
	}
	delete(providers, "nan")
	cfg["provider"] = providers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0o600)
}

// The sign-in questions, drawn where the tab content would be.
// The guided setup: the banner, the four steps with the one you are on marked,
// and whatever that step needs underneath.
//
// It replaced a sign-in screen that showed "Step 1 of 2" and then handed you
// back to the panel to find the other two yourself. Four numbered lines cost
// almost nothing and answer "how much is left", which is the question someone
// halfway through a setup is actually holding.
func (m model) renderWizard(l layout) string {
	title := lipgloss.NewStyle().Bold(true).Foreground(cWhite)
	dim := lipgloss.NewStyle().Foreground(cDimGray)
	done := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	now := lipgloss.NewStyle().Foreground(cCyan).Bold(true)

	var b strings.Builder
	if l.w >= BannerWidthPlain+4 {
		b.WriteString(Banner(l.indent, m.wizard.mood(),
			l.w >= BannerWidth+4 && l.h >= BannerRoom) + "\n")
	}

	b.WriteString(l.indent + title.Render("Setting up") + "\n\n")

	at := m.wizard.index()
	for i, step := range wizardSteps {
		marker, style := dim.Render("  "), dim
		switch {
		case i < at || m.wizard == wizardDone:
			marker, style = done.Render("✓ "), dim
		case i == at:
			marker, style = now.Render("▶ "), now
		}
		b.WriteString(l.indent + marker +
			style.Render(fmt.Sprintf("%d. %s", step.n, step.title)) + "\n")
	}
	b.WriteString("\n")

	switch m.wizard {
	case wizardEmail, wizardLink:
		b.WriteString(l.indent + m.loginInput.View() + "\n")
		if m.loginMsg != "" {
			b.WriteString("\n" + m.wrapped(l, m.loginMsg) + "\n")
		}
	case wizardKey:
		if m.editingKey {
			b.WriteString(l.indent + m.keyInput.View() + "\n")
		}
		if m.keyCheck != "" {
			b.WriteString("\n" + m.wrapped(l, m.keyCheck) + "\n")
		}
	case wizardTools, wizardDone:
		if m.keyCheck != "" {
			b.WriteString(l.indent + m.wrapped(l, m.keyCheck) + "\n\n")
		}
		tools := detectTools()
		cursor := m.setupCursor
		if len(tools) > 0 && cursor >= len(tools) {
			cursor = len(tools) - 1
		}
		b.WriteString(m.renderToolList(l, tools, cursor))
		if m.configuring {
			b.WriteString("\n" + l.indent + m.spin.View() +
				dim.Render(" writing the configs - a few seconds") + "\n")
		} else if m.setupMsg != "" {
			b.WriteString("\n" + m.wrapped(l, m.setupMsg) + "\n")
		}
		b.WriteString(m.renderFailures(l))
	}
	return b.String()
}

// What would not configure, and what to do about it.
//
// One tool failing used to read as the whole step failing, and left a member
// on the last screen of a setup with nothing to do but press escape. The rest
// of the tools were configured and nothing said so.
func (m model) renderFailures(l layout) string {
	if len(m.failures) == 0 {
		return ""
	}
	bad := lipgloss.NewStyle().Foreground(cRed)
	dim := lipgloss.NewStyle().Foreground(cDimGray)

	var b strings.Builder
	b.WriteString("\n")
	for _, f := range m.failures {
		b.WriteString(m.wrapped(l, "error: "+f.name+" — "+f.firstLine()) + "\n")
	}
	_ = bad
	b.WriteString("\n" + l.indent + dim.Render(
		"The rest went in. Fix that one and press c again, or carry on without it - "+
			"its page on nan.builders has the manual steps.") + "\n")
	return b.String()
}

// A message wrapped to the panel, coloured by whether it is one. A sign-in
// link is longer than any terminal.
func (m model) wrapped(l layout, msg string) string {
	style := lipgloss.NewStyle().Foreground(cCyan)
	if strings.HasPrefix(msg, "error") {
		style = lipgloss.NewStyle().Foreground(cRed)
	}
	return indentBlock(lipgloss.NewStyle().
		Width(l.w-lipgloss.Width(l.indent)-1).
		Render(style.Render(msg)), l.indent)
}

func (m model) renderLogin(l layout) string {
	title := lipgloss.NewStyle().Bold(true).Foreground(cWhite)
	dim := lipgloss.NewStyle().Foreground(cGray)
	errStyle := lipgloss.NewStyle().Foreground(cRed)
	ok := lipgloss.NewStyle().Foreground(cCyan)

	var b strings.Builder
	b.WriteString(l.indent + title.Render("Sign in") + "\n\n")

	step := "Step 1 of 2 — where should the link go?"
	if m.loginStage == loginAskLink {
		step = "Step 2 of 2 — the link from the email"
	}
	b.WriteString(l.indent + dim.Render(step) + "\n\n")

	b.WriteString(l.indent + m.loginInput.View() + "\n")

	if m.loginMsg != "" {
		style := ok
		if strings.HasPrefix(m.loginMsg, "error") {
			style = errStyle
		}
		// A sign-in link is longer than any terminal, so this wraps rather
		// than running off the side and taking the rest of the line with it.
		wrapped := lipgloss.NewStyle().Width(l.w - lipgloss.Width(l.indent) - 1).
			Render(style.Render(m.loginMsg))
		b.WriteString("\n" + indentBlock(wrapped, l.indent) + "\n")
	}

	if m.loginStage == loginAskLink {
		b.WriteString("\n" + l.indent + dim.Render("Nothing arrived? esc, then s to start again.") + "\n")
	}
	return b.String()
}

// How to actually use each tool once its config is written. The panel said
// "4 added" and stopped there, which answers what it did and not the question
// a member is left holding: and now what. These are the steps each tool page
// on nan.builders publishes under "check that it works".
var nextStepFor = map[string][2]string{
	"OpenCode":   {"opencode", "then /models inside it, and pick a NaN one"},
	"Codex":      {"codex", "already aimed at the cluster; codex --model <id> to switch"},
	"Pi":         {"pi", "NaN is already its default provider"},
	"Factory AI": {"droid", "pick a model with (NaN) in its name"},
	"Hermes":     {"hermes doctor", "says whether the provider answers, then talk to it"},
}

// Pads to a column width. renderCosts keeps its own; this is package-level
// because a second renderer now needs the same thing.
func lpadTo(v string, w int) string {
	if len(v) >= w {
		return v
	}
	return v + strings.Repeat(" ", w-len(v))
}

// renderToolList draws the tools and their state. Pulled out of the Setup
// tab because the guided setup shows the same list as its last step, and two
// copies of a list with a cursor in it would drift the first time either moved.
func (m model) renderToolList(l layout, tools []toolInfo, cursor int) string {
	dimStyle := lipgloss.NewStyle().Foreground(cDimGray)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	var b strings.Builder
	nameW := 14
	for _, t := range tools {
		if len(t.name) > nameW {
			nameW = len(t.name)
		}
	}

	checkStyle := lipgloss.NewStyle().Foreground(cCyan)
	cursorStyle := lipgloss.NewStyle().Foreground(cCyan).Bold(true)

	for i, t := range tools {
		isCursor := i == cursor
		enabled := m.toolEnabled(t.name)

		cur := "  "
		if isCursor {
			cur = cursorStyle.Render("▶") + " "
		}

		var check string
		if !t.installed {
			check = dimStyle.Render("[ ]")
		} else if enabled {
			check = checkStyle.Render("[✓]")
		} else {
			check = dimStyle.Render("[ ]")
		}

		nameStyle := lipgloss.NewStyle().Foreground(cText).Width(nameW)
		if isCursor {
			nameStyle = nameStyle.Foreground(cWhite)
		}

		var status string
		if !t.installed {
			status = dimStyle.Render("not installed")
		} else if t.configured {
			status = okStyle.Render("✓ configured with NaN")
		} else {
			status = warnStyle.Render("○ not configured")
		}

		b.WriteString(l.indent + cur + check + " " + nameStyle.Render(t.name) + "  " + status + "\n")
	}
	return b.String()
}

func (m model) renderSetup(l layout) string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(cWhite)
	labelStyle := lipgloss.NewStyle().Foreground(cGray).Width(l.keyW)
	dimStyle := lipgloss.NewStyle().Foreground(cDimGray)
	accentStyle := lipgloss.NewStyle().Foreground(cCyan)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	errStyle := lipgloss.NewStyle().Foreground(cRed)

	// ── API Key ──────────────────────────────────────────────────────────────
	b.WriteString(l.indent + titleStyle.Render("API Key") + "\n\n")

	if m.editingKey {
		b.WriteString(l.indent + m.keyInput.View() + "\n")
		b.WriteString(l.indent + dimStyle.Render("Enter to save  ·  Esc to cancel") + "\n")
	} else if m.sess.APIKey != "" {
		b.WriteString(l.indent + labelStyle.Render("Key:") +
			accentStyle.Render(strings.Repeat("•", 24)) +
			"  " + dimStyle.Render("e to edit") + "\n")
	} else {
		b.WriteString(l.indent + warnStyle.Render("No API key set") +
			"  " + dimStyle.Render("e to set") + "\n")
	}

	// What the platform says about the account, once it has answered. The key
	// itself never comes back from there, so the most this can do is say
	// whether there is one to copy and where from - which still beats leaving
	// a member staring at an empty field wondering what to paste.
	if m.keyStatus != nil && m.sess.APIKey == "" {
		hint := "this account has no key yet - create one at cloud.nan.builders"
		if m.keyStatus.Exists {
			name := m.keyStatus.Alias
			if name == "" {
				name = m.keyStatus.Name
			}
			hint = "your account has a key"
			if name != "" {
				hint += " (" + name + ")"
			}
			hint += " - copy it from cloud.nan.builders"
		}
		b.WriteString(l.indent + dimStyle.Render(hint) + "\n")
	}
	if m.keyCheck != "" {
		style := okStyle
		if strings.HasPrefix(m.keyCheck, "error") {
			style = errStyle
		}
		b.WriteString(l.indent + style.Render(m.keyCheck) + "\n")
	}

	// ── Tools ────────────────────────────────────────────────────────────────
	b.WriteString("\n" + l.indent + titleStyle.Render("Tools") + "\n")
	if m.configuring {
		// Several seconds, because Hermes is configured by running Hermes.
		// Saying so beats a panel that looks hung.
		b.WriteString(l.indent + m.spin.View() +
			dimStyle.Render(" writing the configs - a few seconds, Hermes is asked rather than written") + "\n")
	}
	if m.setupMsg != "" {
		style := okStyle
		if strings.HasPrefix(m.setupMsg, "error") {
			style = errStyle
		}
		b.WriteString(l.indent + style.Render(m.setupMsg) + "\n")
	}

	b.WriteString(m.renderFailures(l))

	// And now what. The answer used to be nowhere in the panel.
	if len(m.configured) > 0 {
		b.WriteString("\n" + l.indent + titleStyle.Render("Now open them") + "\n\n")
		width := 0
		for _, name := range m.configured {
			if step, ok := nextStepFor[name]; ok && len(step[0]) > width {
				width = len(step[0])
			}
		}
		for _, name := range m.configured {
			step, ok := nextStepFor[name]
			if !ok {
				continue
			}
			b.WriteString(l.indent + accentStyle.Render(lpadTo(step[0], width+2)) +
				dimStyle.Render(step[1]) + "\n")
		}
	}
	b.WriteString("\n")

	tools := detectTools()
	cursor := m.setupCursor
	if len(tools) > 0 && cursor >= len(tools) {
		cursor = len(tools) - 1
	}

	b.WriteString(m.renderToolList(l, tools, cursor))

	b.WriteString("\n")
	if m.sess.APIKey == "" {
		b.WriteString(l.indent + dimStyle.Render("Set an API key first (e).") + "\n")
	} else {
		keys := "↑/↓ navigate   space toggle   c configure selected"
		if m.sess.Token != "" {
			keys += "   o sign out"
		}
		b.WriteString(l.indent + dimStyle.Render(keys) + "\n")
	}

	return b.String()
}

// ── about renderer ───────────────────────────────────────────────────────────

const Version = "0.1.18"

func renderAbout(l layout) string {
	var b strings.Builder

	logoStyle := lipgloss.NewStyle().Bold(true).Foreground(cCyan)
	dimStyle := lipgloss.NewStyle().Foreground(cDimGray)
	labelStyle := lipgloss.NewStyle().Foreground(cGray).Width(l.keyW)
	linkStyle := lipgloss.NewStyle().Foreground(cBlue)
	accentStyle := lipgloss.NewStyle().Foreground(cCyan)
	sectionStyle := lipgloss.NewStyle().Foreground(cGray).Bold(true)

	// The banner needs room for the art with the text beside it; narrower than
	// that it would wrap into nonsense, so the plain line stays.
	banner := l.w >= BannerWidthPlain+4
	if banner {
		b.WriteString(Banner(l.indent, moodNormal, l.w >= BannerWidth+4 && l.h >= BannerRoom) + "\n")
	} else {
		b.WriteString(l.indent + logoStyle.Render("nan") +
			"  " + dimStyle.Render("v"+Version) + "\n")
		b.WriteString(l.indent + dimStyle.Render("nan.builders cloud CLI") + "\n\n")
	}

	b.WriteString(l.indent + sectionStyle.Render("Links") + "\n\n")
	b.WriteString(l.indent + labelStyle.Render("Platform:") +
		linkStyle.Render("https://nan.builders") + "\n")
	b.WriteString(l.indent + labelStyle.Render("Cloud:") +
		linkStyle.Render("https://cloud.nan.builders") + "\n\n")

	// The banner already says who made it and who keeps it; repeating it four
	// rows below is just the same line twice.
	if !banner {
		b.WriteString(l.indent + sectionStyle.Render("Maintainer") + "\n\n")
		b.WriteString(l.indent + labelStyle.Render("Author:") +
			accentStyle.Render("@Nxssie") + "\n\n")
	}

	b.WriteString(l.indent + sectionStyle.Render("Session") + "\n\n")
	b.WriteString(l.indent + labelStyle.Render("Config:") +
		dimStyle.Render(session.Path()) + "\n")

	return b.String()
}

// ── help overlay ─────────────────────────────────────────────────────────────

func renderHelp() string {
	shortcuts := []struct{ key, desc string }{
		{"←/→  h/l  Tab", "switch tabs"},
		{"↑/↓  k/j", "scroll"},
		{"r", "refresh current tab"},
		{"s", "sign in, when there is no session"},
		{"e", "edit API key (Setup tab)"},
		{"space", "tick or untick the tool under the cursor (Setup tab)"},
		{"c", "configure the ticked tools (Setup tab)"},
		{"o", "sign out, twice to confirm"},
		{"?", "toggle this help"},
		{"q / Esc", "quit"},
	}

	titleStyle := lipgloss.NewStyle().Foreground(cWhite).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(cGray)

	keyW := 0
	for _, s := range shortcuts {
		if w := lipgloss.Width(s.key); w > keyW {
			keyW = w
		}
	}
	keyW += 3

	rows := []string{titleStyle.Render("Keyboard Shortcuts"), ""}
	for _, s := range shortcuts {
		keyCol := lipgloss.NewStyle().Foreground(cCyan).Bold(true).Width(keyW).Render(s.key)
		rows = append(rows, keyCol+descStyle.Render(s.desc))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlueDim).
		Padding(1, 2).
		Render(strings.Join(rows, "\n"))
}

// ── entry point ───────────────────────────────────────────────────────────────

func Run() error {
	// A machine that has never logged in still gets the TUI: the Setup tab,
	// which is the one that configures the tools, needs the API key and nothing
	// else. Returning ErrNotLoggedIn here meant a fresh install could not open
	// the dashboard at all, and the only way in was `nan auth login --token`
	// with any string whatsoever.
	sess, err := session.Load()
	if err != nil {
		if !errors.Is(err, session.ErrNotLoggedIn) {
			return err
		}
		sess = &session.Session{}
	}
	client := api.New(sess.Token)
	m := newModel(client, sess)
	// Home asks the API for nothing, so there is nothing to wait for: starting
	// on `loading` would spin forever over a tab that is already drawn. The
	// data tabs set it themselves in maybeLoad when you walk into them.
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

// indentBlock indents EVERY line. `indent + Render(...)` only moves the first
// one, which on a bordered box leaves the top edge two columns right of the
// sides.
func indentBlock(block, indent string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

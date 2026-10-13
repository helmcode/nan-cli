// Package pulse is the dashboard for a nan.builders member.
//
// Seven areas down the left - Home, Profile, Usage, Models, Costs,
// Harnesses, About - always on screen and numbered, so what exists and where
// it lives is never a question. Home says what needs attention and offers
// what people come here to do; the other areas are lists, and enter on a row
// opens a menu of exactly what that row allows.
//
// Built from what the TUIs people keep open have in common - lazygit's
// always-visible, numbered places and stable verbs, lazydocker's action menu,
// soft-serve's copy over OSC 52 - and the same structure as the Helmcode
// CLI's dashboard, so the two are maintained the same way.
package pulse

import (
	"context"
	"math"
	"os"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/harmonica"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/flow"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/nxssie/nan-cli/internal/tools"
	"github.com/nxssie/nan-cli/internal/ui"
	"github.com/nxssie/nan-cli/internal/update"
)

// guide is how a first run ends here.
var guide = [][2]string{
	{"1-7  tab", "the areas: Home, Profile, Usage, Models, Costs, Harnesses, About"},
	{"↑/↓  enter", "pick a row, and see what you can do with it"},
	{"e", "the API key and the tools that use it, from anywhere"},
	{"/", "find anything by name"},
}

type model struct {
	ctx     context.Context
	client  *api.Client
	session *session.Session

	w, h int

	keys   keyMap
	help   help.Model
	loader ui.Loader

	loading bool
	loaded  bool
	snap    snapshot
	sigs    []signal

	area   area
	cursor [areaCount]int
	// picked is the id under each area's cursor, so a reload puts the
	// cursor back on the same thing rather than on whatever now sits at the
	// same index.
	picked [areaCount]string

	// stack is the cards open over the current area; links the cursor on
	// each card's links.
	stack []target
	links []int

	modal modal
	pal   palette

	// flash is the result of the last action, for a few seconds.
	flash    string
	flashBad bool
	flashID  int

	signIn flow.SignIn
	setup  flow.Setup

	// confirmSignOut is the first o: signing out takes the key back out of
	// every tool, so it takes a second press. signingOut is the platform
	// being asked to end the session.
	confirmSignOut bool
	signingOut     bool
	// expiredNotice is the platform having refused the session: said on
	// Home until the member signs in again.
	expiredNotice bool

	spring    harmonica.Spring
	reveal    float64
	velocity  float64
	animating bool
	// frame counts animation ticks, to shimmer the decoding noise.
	frame int

	// tour is the guided tour's step, from 1, or 0 when it is not running.
	tour int

	// upd is what is remembered between runs: the models known, the latest
	// version (internal/update). newModels is what appeared since the last
	// run, kept for the whole visit.
	upd       update.State
	newModels []string

	quitting bool
}

type (
	animTickMsg  struct{}
	flashDoneMsg struct{ id int }
	// signedOutMsg is the platform's answer to ending the session; err is
	// nil when it confirmed.
	signedOutMsg struct{ err error }
)

const (
	animFPS   = 60
	flashFor  = 6 * time.Second
	cardWidth = 88
)

func newModel(ctx context.Context, client *api.Client, sess *session.Session) model {
	h := help.New()
	h.Styles = ui.HelpStyles()
	return model{
		ctx:     ctx,
		client:  client,
		session: sess,
		keys:    newKeyMap(),
		help:    h,
		loader:  ui.NewLoader("Reading your account"),
		pal:     newPalette(),
		spring:  harmonica.NewSpring(harmonica.FPS(animFPS), 7.0, 0.8),
		reveal:  1,
		upd:     update.Load(),
	}
}

// Run opens the dashboard.
func Run(ctx context.Context, client *api.Client, sess *session.Session) error {
	// The palette is chosen by asking the terminal for its background, which
	// only works before Bubble Tea takes the input over.
	_ = ui.Current()
	m := newModel(ctx, client, sess)
	// Started here, not in Init, which gets a copy of the model.
	opening := m.opening()
	p := tea.NewProgram(m)
	go func() { p.Send(opening()) }()
	_, err := p.Run()
	return err
}

// opening is what the first frame starts: the load, and whatever is
// missing before the dashboard is any use - the sign-in, then the key.
func (m *model) opening() tea.Cmd {
	cmds := []tea.Cmd{m.startLoad()}
	switch {
	case !m.session.SignedIn():
		cmds = append(cmds, m.signIn.Start(m.notice()))
	case m.session.APIKey == "":
		cmds = append(cmds, m.setup.Onboard("", flow.Guide{Keys: guide, Tour: true}))
	}
	return tea.Batch(cmds...)
}

// notice is what the sign-in says above the address, on a machine that has
// a credential the dashboard cannot use.
func (m model) notice() string {
	if m.session.PlatformToken != "" || os.Getenv(session.TokenEnvVar) != "" {
		return flow.TokenOnlyNotice
	}
	return ""
}

func (m model) Init() tea.Cmd { return m.loader.Tick() }

func (m *model) startLoad() tea.Cmd {
	m.loading = true
	ctx, c, key := m.ctx, m.client, m.session.APIKey
	// The latest version at most once a day: most runs ask nothing extra.
	ask := m.upd.DueForCheck(time.Now())
	return tea.Batch(m.loader.Tick(), func() tea.Msg { return loadedMsg{snap: load(ctx, c, key, ask)} })
}

func (m *model) animate() tea.Cmd {
	m.reveal, m.velocity = 0, 0
	if m.animating {
		return nil
	}
	m.animating = true
	return animTick()
}

func animTick() tea.Cmd {
	return tea.Tick(time.Second/animFPS, func(time.Time) tea.Msg { return animTickMsg{} })
}

// say puts a result on the flash line, and clears it after a while.
func (m *model) say(text string, bad bool) tea.Cmd {
	m.flash, m.flashBad = text, bad
	m.flashID++
	id := m.flashID
	return tea.Tick(flashFor, func(time.Time) tea.Msg { return flashDoneMsg{id: id} })
}

func (m model) inCard() bool { return len(m.stack) > 0 }

// inFlow is whether a sign-in or a Setup has the screen.
func (m model) inFlow() bool { return m.signIn.Active() || m.setup.Active() }

// selectedRow is the row under the current area's cursor.
func (m model) selectedRow() *row {
	rows := m.rows(m.area)
	i := m.cursor[m.area]
	if i < 0 || i >= len(rows) {
		return nil
	}
	return &rows[i]
}

// Update is the entry point: the real update, then the reveal restarted if
// what is on screen changed - another area, a card opened or closed, a flow
// closing. In one place, so no path that changes the screen can forget it.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before, depth, flow := m.area, len(m.stack), m.inFlow()
	next, cmd := m.update(msg)
	nm, ok := next.(model)
	if ok {
		nm.advanceTour()
		next = nm
	}
	closed := flow && !nm.inFlow()
	if !ok || !nm.loaded || nm.area == before && len(nm.stack) == depth && !closed {
		return next, cmd
	}
	anim := nm.animate()
	return nm, tea.Batch(cmd, anim)
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.help.SetWidth(msg.Width - 4)
		m.pal.input.SetWidth(max(msg.Width-10, 10))
		m.signIn.Resize(m.w, m.h-3)
		m.setup.Resize(m.w, m.h-3)
		return m, nil

	case ui.LoaderTickMsg:
		if !m.loading && !m.signingOut && !m.inFlow() {
			return m, nil
		}
		var cmd tea.Cmd
		m.loader, cmd = m.loader.Update(msg)
		return m, cmd

	case animTickMsg:
		m.frame++
		m.reveal, m.velocity = m.spring.Update(m.reveal, m.velocity, 1)
		if math.Abs(1-m.reveal) < 0.002 && math.Abs(m.velocity) < 0.002 {
			m.reveal, m.velocity, m.animating = 1, 0, false
			return m, nil
		}
		return m, animTick()

	case flashDoneMsg:
		if msg.id == m.flashID {
			m.flash = ""
		}
		return m, nil

	case loadedMsg:
		return m.takeLoad(msg.snap)

	case signedOutMsg:
		return m.finishSignOut(msg.err)
	}

	// ctrl+c leaves from anywhere, a form included: every other key may be
	// somebody typing, this one never is.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}

	if m.signIn.Active() {
		next, cmd := m.signIn.Update(m.ctx, msg, m.client)
		m.signIn = next
		if m.signIn.Done() {
			return m.afterSignIn()
		}
		return m, cmd
	}
	if m.setup.Active() {
		next, cmd := m.setup.Update(m.ctx, msg, m.session.EnabledTools)
		m.setup = next
		if !m.setup.Active() {
			return m.afterSetup(cmd)
		}
		return m, cmd
	}

	if k, ok := msg.(tea.KeyPressMsg); ok {
		// The tour's own keys first. The closing line goes on the next key,
		// which then does its usual job; x skips, except while typing a
		// search, where it is a letter like any other.
		if m.touring() {
			switch {
			case m.tour >= tourFinished:
				m.stopTour()
			case k.String() == "x" && !m.pal.visible:
				m.stopTour()
				return m, nil
			}
		}
		// Any key but o lets go of a sign-out that was waiting for its
		// second press.
		if m.confirmSignOut && !key.Matches(k, m.keys.SignOut) {
			m.confirmSignOut = false
			m.flash = ""
		}
		switch {
		case m.modal.kind != modalNone:
			return m.onModalKey(k)
		case m.pal.visible:
			return m.onPaletteKey(k)
		}
		return m.onKey(k)
	}
	if m.pal.visible {
		var cmd tea.Cmd
		m.pal.input, cmd = m.pal.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// takeLoad takes a snapshot in.
func (m model) takeLoad(s snapshot) (tea.Model, tea.Cmd) {
	// A refusal of a session that is no longer the one on this machine - an
	// old load arriving after a new sign-in - says nothing about the new one.
	if s.token != m.session.Token {
		s.expired = false
	}
	first := !m.loaded
	m.loading, m.loaded = false, true
	m.snap = s
	m.takeNote(s)

	var cmds []tea.Cmd
	if s.expired {
		// The session is gone on the platform; the key and the ticked tools
		// are still the member's, so only the token goes.
		m.session.Token = ""
		_ = session.Save(m.session)
		m.client = api.New("")
		m.expiredNotice = true
		switch {
		case first && !m.inFlow():
			// On a machine opened on an expired session, the sign-in is the
			// first thing anybody needs, so it opens on its own.
			cmds = append(cmds, m.signIn.Start(m.notice()))
		case !m.inFlow():
			// Later, it is said, and the keyboard is left alone: whoever is
			// mid-way through something keeps doing it.
			cmds = append(cmds, m.say(api.ErrSessionExpired.Error()+" - press s to sign in again", true))
		}
	}
	m.sigs = m.signals()
	m.restoreCursors()
	if first {
		cmds = append(cmds, m.animate())
	}
	return m, tea.Batch(cmds...)
}

// restoreCursors puts every area's cursor back on the thing it was on.
func (m *model) restoreCursors() {
	for a := area(0); a < areaCount; a++ {
		rows := m.rows(a)
		if id := m.picked[a]; id != "" {
			for i, r := range rows {
				if r.id == id {
					m.cursor[a] = i
					break
				}
			}
		}
		if m.cursor[a] >= len(rows) {
			m.cursor[a] = max(len(rows)-1, 0)
		}
	}
}

func (m *model) remember() {
	if r := m.selectedRow(); r != nil {
		m.picked[m.area] = r.id
	}
}

func (m model) afterSignIn() (tea.Model, tea.Cmd) {
	m.session.Token = m.signIn.Token
	_ = session.Save(m.session)
	// A client still holding the token it started with sends no session,
	// and the platform answers `unauthorized`: exactly what a failed
	// sign-in looks like.
	m.client = api.New(m.signIn.Token)
	m.signIn = flow.NewSignIn()
	m.expiredNotice = false
	cmds := []tea.Cmd{m.startLoad()}
	if m.session.APIKey == "" {
		cmds = append(cmds, m.setup.Onboard(flow.KeyHint(m.snap.keyStatus), flow.Guide{Keys: guide, Tour: true}))
	}
	return m, tea.Batch(cmds...)
}

func (m model) afterSetup(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.setup.TourRequested {
		m.startTour()
	}
	if m.setup.Key != "" {
		m.session.APIKey = m.setup.Key
		if m.setup.Tools != nil {
			m.session.EnabledTools = m.setup.Tools
		}
		_ = session.Save(m.session)
	}
	m.setup = flow.NewSetup()
	// What Setup just wrote is what Home and Harnesses should now say.
	return m, tea.Batch(cmd, m.startLoad())
}

// startSetup opens Setup over the Harnesses area.
func (m *model) startSetup() tea.Cmd {
	m.goTo(areaHarness)
	return m.setup.Start(m.session.APIKey, flow.KeyHint(m.snap.keyStatus))
}

// goTo switches area.
func (m *model) goTo(a area) {
	m.area = a
	m.stack, m.links = nil, nil
	if a == areaModels {
		// Opening the list is seeing what is new in it.
		m.seeNewModels()
	}
}

// signOut is o: the first press asks, the second does it.
func (m model) signOut() (tea.Model, tea.Cmd) {
	if !m.session.SignedIn() || m.signingOut {
		return m, nil
	}
	if !m.confirmSignOut {
		m.confirmSignOut = true
		return m, m.say("press o again to sign out: it also takes your key back out of the tools. Any other key keeps the session.", false)
	}
	m.confirmSignOut, m.signingOut = false, true
	m.flash = ""
	ctx, c := m.ctx, m.client
	// The platform first, while the token is still here: deleting the file
	// alone would leave any copy of the token working until it expires.
	return m, tea.Batch(m.loader.Tick(), func() tea.Msg { return signedOutMsg{err: c.Logout(ctx)} })
}

// finishSignOut is the local half, whatever the platform said: the session
// file goes, and the key comes back out of every tool it was written into.
func (m model) finishSignOut(revokeErr error) (tea.Model, tea.Cmd) {
	if !m.signingOut {
		return m, nil // a stray answer, with no sign-out in flight
	}
	m.signingOut = false
	_ = session.Delete()
	removed, failed := tools.RemoveAll()

	m.session = &session.Session{}
	m.client = api.New("")
	m.snap, m.sigs, m.newModels = snapshot{}, nil, nil
	m.expiredNotice = false
	m.goTo(areaHome)

	msg, bad := "signed out", false
	switch {
	case len(failed) > 0:
		names := make([]string, len(failed))
		for i, f := range failed {
			names[i] = f.Tool + " (" + f.FirstLine() + ")"
		}
		msg, bad = "signed out, but the key is still in "+joinNames(names), true
	case len(removed) > 0:
		msg = "signed out  ·  key removed from " + joinNames(removed)
	}
	if revokeErr != nil {
		msg += "  ·  the platform did not confirm the sign-out (" + revokeErr.Error() + "): the session stays valid until it expires"
		bad = true
	}
	// Signed out is a fresh machine again, and a fresh machine is asked to
	// sign in.
	return m, tea.Batch(m.say(msg, bad), m.signIn.Start(""), m.startLoad())
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func (m model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		return m, nil
	case key.Matches(msg, m.keys.Tour) && m.loaded:
		m.startTour()
		return m, nil
	case key.Matches(msg, m.keys.Find):
		m.pal.open(m.entries())
		return m, nil
	case key.Matches(msg, m.keys.Reload):
		if !m.loading {
			return m, m.startLoad()
		}
		return m, nil
	case key.Matches(msg, m.keys.SignIn) && !m.session.SignedIn() && !m.signingOut:
		return m, m.signIn.Start(m.notice())
	case key.Matches(msg, m.keys.SignOut):
		return m.signOut()
	case key.Matches(msg, m.keys.Setup):
		return m, m.startSetup()
	case key.Matches(msg, m.keys.Area):
		m.goTo(area(msg.String()[0] - '1'))
		return m, nil
	case key.Matches(msg, m.keys.NextArea):
		m.goTo((m.area + 1) % areaCount)
		return m, nil
	case key.Matches(msg, m.keys.PrevArea):
		m.goTo((m.area + areaCount - 1) % areaCount)
		return m, nil
	}

	if m.inCard() {
		return m.onCardKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		m.move(-1)
	case key.Matches(msg, m.keys.Down):
		m.move(1)
	case key.Matches(msg, m.keys.Back):
		if m.area != areaHome {
			m.goTo(areaHome)
		}
	case key.Matches(msg, m.keys.Enter):
		return m.onEnter()
	default:
		if v := m.keys.verbFor(msg); v != verbNone && m.has(v) {
			return m.do(v)
		}
	}
	return m, nil
}

func (m *model) move(delta int) {
	n := len(m.rows(m.area))
	if n == 0 {
		return
	}
	m.cursor[m.area] = (m.cursor[m.area] + delta + n) % n
	m.remember()
}

// onEnter is what enter does where you are: follow a signal, run a Home
// shortcut, set up the machine, or open the row's menu.
func (m model) onEnter() (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	switch {
	case m.area == areaHarness:
		return m, m.startSetup()
	case r != nil && r.sig != nil:
		return m.follow(r.sig.goTo)
	case r != nil && r.newModel != "":
		m.land(target{kind: cardModel, id: r.newModel})
		return m, nil
	case r != nil && r.quick != nil:
		return m.act(r.quick.do, r.quick.in)
	}
	m.openMenu()
	return m, nil
}

// follow goes where a signal points.
func (m model) follow(t target) (tea.Model, tea.Cmd) {
	switch t.kind {
	case cardSignIn:
		if !m.session.SignedIn() {
			return m, m.signIn.Start(m.notice())
		}
	case cardHarness:
		m.goTo(areaHarness)
	case cardUpdate:
		m.updateModal()
	default:
		m.land(t)
	}
	return m, nil
}

// act runs a shortcut: something to do, or somewhere to go.
func (m model) act(a action, in area) (tea.Model, tea.Cmd) {
	switch a {
	case actSetup:
		return m, m.startSetup()
	case actSignIn:
		if !m.session.SignedIn() {
			return m, m.signIn.Start(m.notice())
		}
		return m, nil
	case actSignOut:
		return m.signOut()
	case actReload:
		return m, m.startLoad()
	case actQuit:
		m.quitting = true
		return m, tea.Quit
	}
	m.goTo(in)
	return m, nil
}

// land goes to where a target lives and puts the cursor on it.
func (m *model) land(t target) {
	m.goTo(areaOf(t))
	for i, r := range m.rows(m.area) {
		if r.id == t.id {
			m.cursor[m.area] = i
			m.picked[m.area] = t.id
		}
	}
}

// do performs a verb on the current area and row.
func (m model) do(v verb) (tea.Model, tea.Cmd) {
	r := m.selectedRow()
	m.modal = modal{}
	switch v {
	case verbDetails:
		if r != nil && r.model != "" {
			kind := cardModel
			if m.area == areaUsage {
				kind = cardUsage
			}
			m.stack, m.links = []target{{kind: kind, id: r.model}}, []int{0}
			return m, m.animate()
		}
	case verbEdit:
		return m, m.startSetup()
	case verbCopy:
		if r != nil && r.model != "" {
			return m, tea.Batch(tea.SetClipboard(r.model), m.say("Copied "+r.model+".", false))
		}
	}
	return m, nil
}

func (m model) onModalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	md := &m.modal
	switch md.kind {
	case modalMenu:
		switch {
		case key.Matches(msg, menuKeys.up):
			md.cursor = (md.cursor + len(md.items) - 1) % len(md.items)
		case key.Matches(msg, menuKeys.down):
			md.cursor = (md.cursor + 1) % len(md.items)
		case key.Matches(msg, menuKeys.close):
			m.modal = modal{}
		case key.Matches(msg, menuKeys.pick):
			return m.do(md.items[md.cursor].verb)
		default:
			// The letters shown in the menu work in it too.
			if v := m.keys.verbFor(msg); v != verbNone {
				for _, it := range md.items {
					if it.verb == v {
						return m.do(v)
					}
				}
			}
			if key.Matches(msg, m.keys.Setup) {
				for _, it := range md.items {
					if it.verb == verbEdit {
						return m.do(verbEdit)
					}
				}
			}
		}
		return m, nil

	case modalLink:
		switch msg.String() {
		case "y":
			md.copied = true
			return m, tea.SetClipboard(md.url)
		case "enter", "esc", "q":
			m.modal = modal{}
		}
		return m, nil
	}
	return m, nil
}

// onCardKey moves through a card's links, follows them, and walks back.
func (m model) onCardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := m.currentCard()
	i := len(m.links) - 1
	switch {
	case key.Matches(msg, m.keys.Up):
		if n := len(c.links); n > 0 {
			m.links[i] = (m.links[i] + n - 1) % n
		}
	case key.Matches(msg, m.keys.Down):
		if n := len(c.links); n > 0 {
			m.links[i] = (m.links[i] + 1) % n
		}
	case key.Matches(msg, m.keys.Enter):
		if m.links[i] < len(c.links) {
			m.stack = append(m.stack, c.links[m.links[i]].to)
			m.links = append(m.links, 0)
			return m, m.animate()
		}
	case key.Matches(msg, m.keys.Back):
		m.stack, m.links = m.stack[:len(m.stack)-1], m.links[:len(m.links)-1]
	case key.Matches(msg, m.keys.Copy):
		id := m.stack[len(m.stack)-1].id
		return m, tea.Batch(tea.SetClipboard(id), m.say("Copied "+id+".", false))
	}
	return m, nil
}

func (m model) currentCard() card {
	return buildCard(m.snap, m.stack[len(m.stack)-1], m.contentWidth(), m.reveal)
}

func (m model) onPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.pal.close()
		return m, nil
	case "up", "ctrl+p":
		m.pal.move(-1)
		return m, nil
	case "down", "ctrl+n", "tab":
		m.pal.move(1)
		return m, nil
	case "enter":
		e, ok := m.pal.selected()
		m.pal.close()
		if !ok {
			return m, nil
		}
		if e.do != actNone {
			return m.act(e.do, e.in)
		}
		if e.open.kind != 0 {
			// A search lands on the thing in its area, where its actions
			// are, rather than on a card with none.
			m.land(e.open)
			return m, nil
		}
		m.goTo(e.in)
		return m, nil
	}
	var cmd tea.Cmd
	m.pal.input, cmd = m.pal.input.Update(msg)
	m.pal.filter()
	return m, cmd
}

// mood is the mascot's face, from what the dashboard is doing. It answers
// "what is going on" from across the room.
func (m model) mood() ui.Mood {
	switch {
	case m.loading || m.signingOut:
		return ui.MoodThinking
	case m.snap.err != nil || m.flashBad:
		return ui.MoodSad
	case m.signIn.Active():
		return ui.MoodSurprised
	case !m.session.SignedIn():
		return ui.MoodAsleep
	case m.session.APIKey != "":
		return ui.MoodHappy
	}
	return ui.MoodNormal
}

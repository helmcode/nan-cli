package pulse

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/helmcode/nan-cli/internal/api"
	"github.com/helmcode/nan-cli/internal/build"
	"github.com/helmcode/nan-cli/internal/flow"
	"github.com/helmcode/nan-cli/internal/session"
	"github.com/helmcode/nan-cli/internal/tools"
	"github.com/helmcode/nan-cli/internal/ui"
	"github.com/helmcode/nan-cli/internal/update"
)

const (
	margin     = 2
	minW       = 72
	minH       = 20
	maxResults = 10
)

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func pad() string { return strings.Repeat(" ", margin) }

// contentWidth is the terminal minus the margins: the areas are a row of
// tabs over the content, not a column beside it.
func (m model) contentWidth() int { return max(m.w-2*margin, 20) }

func (m model) render() string {
	if m.quitting || m.w == 0 {
		return ""
	}
	if m.w < minW || m.h < minH {
		// Every area is also a command, which fits any terminal.
		// Every key still works here; q is the one worth saying.
		return fmt.Sprintf("\n  This dashboard needs %dx%d; the terminal is %dx%d.\n  Make it bigger, or use the commands (nan --help lists them).\n  q quits.\n", minW, minH, m.w, m.h)
	}

	header := m.renderHeader()
	footer := m.renderFooter()
	// The flash line and the footer each start with a newline, which ends
	// the line before rather than adding one; the flash line is one row.
	room := m.h - lipgloss.Height(header) - 1 - (lipgloss.Height(footer) - 1)

	var body string
	switch {
	case m.signIn.Active():
		body = m.flowBanner(room) + m.signIn.View(pad(), m.loader.SetLabel(m.signIn.Label()).View(), m.client.APIHost())
	case m.setup.Active():
		body = m.flowBanner(room) + m.setup.View(pad(), m.loader.SetLabel(m.setup.Label()).View())
	case !m.loaded:
		body = "\n" + pad() + m.loader.View()
	default:
		// The tabs, then the area under them; every area opens on a blank
		// line, which is the gap between the two.
		room := room - 1
		content := fit(m.renderContent(room), room, m.contentWidth())
		body = m.renderTabs() + "\n" + indent(content)
	}
	return header + "\n" + fit(body, room, m.w) + m.renderFlash() + footer
}

// flowBanner is the banner over a sign-in or a Setup, when there is room
// for it under the form: the mascot goes first, then the wordmark.
func (m model) flowBanner(room int) string {
	switch {
	case room >= ui.BannerRoom+12 && m.w >= ui.BannerWidth(build.Version)+2*margin:
		return "\n" + ui.Banner(build.Version, pad(), m.mood(), true) + "\n"
	case room >= 6+14 && m.w >= ui.BannerWidthCompact(build.Version)+2*margin:
		return "\n" + ui.Banner(build.Version, pad(), m.mood(), false) + "\n"
	}
	return ""
}

// fit cuts or pads a block to exactly rows lines of exactly width columns.
func fit(block string, rows, width int) string {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) > rows {
		lines = lines[:max(rows, 0)]
	}
	for i := range lines {
		lines[i] = ui.Pad(ansi.Truncate(lines[i], width, "…"), width)
	}
	for len(lines) < rows {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

// ── chrome ───────────────────────────────────────────────────────────────

// renderHeader is what this is on the left and where the session stands on
// the right, with an update said first when there is one.
func (m model) renderHeader() string {
	t := ui.Current()
	left := lipgloss.NewStyle().Foreground(t.AccentText).Bold(true).Render("nan.builders") +
		"  " + ui.Faint("cloud CLI")

	right := ui.Faint("not signed in")
	if m.session.SignedIn() {
		right = ui.Faint("signed in")
	}
	if m.loading && m.loaded {
		right = ui.Faint("reloading…  ") + right
	}
	if v := m.upd.Available(); v != "" {
		right = lipgloss.NewStyle().Foreground(t.Yellow).Render("update "+v) + ui.Faint("  ") + right
	}
	gap := max(m.w-2*margin-lipgloss.Width(left)-lipgloss.Width(right), 1)
	line := ansi.Truncate(pad()+left+strings.Repeat(" ", gap)+right, m.w, "…")
	return line + "\n" + pad() + lipgloss.NewStyle().Foreground(t.Line).Render(strings.Repeat("─", m.w-2*margin))
}

// renderTabs is the areas in a row, the way nan has always shown them: the
// one you are on filled in, each with the number that jumps to it and a count
// where it has one. A terminal too narrow for all of that drops the counts
// first, then the numbers, so the row never wraps.
func (m model) renderTabs() string {
	t := ui.Current()
	room := m.w - 2*margin
	for _, detail := range []struct{ num, count bool }{{true, true}, {true, false}, {false, false}} {
		parts := make([]string, 0, areaCount)
		for a := area(0); a < areaCount; a++ {
			label := a.title()
			if detail.num {
				label = fmt.Sprintf("%d %s", a+1, label)
			}
			if c := m.count(a); detail.count && c != "" {
				label += " " + c
			}
			style := lipgloss.NewStyle().Padding(0, 1)
			if a == m.area {
				style = style.Foreground(lipgloss.Color("#FFFFFF")).Background(t.Accent).Bold(true)
			} else {
				style = style.Foreground(t.TextDim)
			}
			parts = append(parts, style.Render(label))
		}
		row := strings.Join(parts, " ")
		if lipgloss.Width(row) <= room || !detail.num {
			return pad() + ansi.Truncate(row, room, "…")
		}
	}
	return ""
}

// indent puts the margin in front of every line of a block.
func indent(block string) string {
	lines := strings.Split(block, "\n")
	for i := range lines {
		lines[i] = pad() + lines[i]
	}
	return strings.Join(lines, "\n")
}

// renderFlash is the result of the last action, or a blank line.
func (m model) renderFlash() string {
	width := m.w - 2*margin
	switch {
	case m.signingOut:
		return "\n" + pad() + m.loader.SetLabel("Signing out").View()
	case m.flash == "":
		return "\n"
	case m.flashBad:
		return "\n" + pad() + ansi.Truncate(ui.Error(fmt.Errorf("%s", m.flash)), width, "…")
	}
	return "\n" + pad() + ansi.Truncate(ui.Success(m.flash), width, "…")
}

// renderFooter says what the keys do HERE: the verbs this row allows, then
// the few that work everywhere. Read from the same list as the menu and the
// key handler, so an offered key is a working key.
func (m model) renderFooter() string {
	k := m.keys
	var binds []key.Binding
	switch {
	case m.setup.AsksAboutTour():
		binds = []key.Binding{hint("enter", "take the tour"), hint("s", "skip it"), hint("ctrl+c", "quit")}
	case m.setup.SkippableKey():
		binds = []key.Binding{hint("enter", "check the key"), hint("esc", "skip for now"), hint("ctrl+c", "quit")}
	case m.signIn.Active(), m.setup.Active():
		// A form owns the keyboard: none of the dashboard's keys work here,
		// so none are offered.
		binds = []key.Binding{hint("enter", "continue"), hint("esc", "back"), hint("ctrl+c", "quit")}
	case m.touring() && !m.help.ShowAll:
		// For as long as the tour runs, the tour is the help.
		return "\n" + pad() + ansi.Truncate(m.renderTour(), m.w-2*margin, "…")
	case m.pal.visible:
		binds = []key.Binding{hint("type", "to find"), hint("↑/↓", "pick"), hint("enter", "go"), hint("esc", "close")}
	case m.modal.kind == modalMenu:
		binds = []key.Binding{hint("↑/↓", "pick"), hint("enter", "do it"), hint("esc", "close")}
	case m.modal.kind == modalLink:
		binds = []key.Binding{hint("y", "copy it"), hint("enter", "close")}
	case m.inCard():
		binds = []key.Binding{hint("y", "copy the id"), k.Back}
	default:
		binds = append(binds, k.Up)
		verbs := m.verbs(m.area, m.selectedRow())
		switch {
		case m.area == areaHome:
			binds = append(binds, hint("enter", "go"))
		case m.area == areaHarness:
			binds = append(binds, hint("enter", "set up"))
		case len(verbs) > 0:
			binds = append(binds, k.Enter)
		}
		for _, it := range verbs {
			if it.key != "enter" && it.key != "e" {
				binds = append(binds, hint(it.key, strings.ToLower(it.label)))
			}
		}
		// The way to every other key, and out, come before the rest: on a
		// narrow terminal the line is cut from the right, and `?` and `q`
		// are the two that must never be what goes.
		binds = append(binds, k.Help, k.Quit, k.Find)
		if m.session.SignedIn() {
			binds = append(binds, k.SignOut)
		} else {
			binds = append(binds, k.SignIn)
		}
		if m.area != areaHarness {
			binds = append(binds, k.Setup)
		}
		binds = append(binds, k.NextArea, k.Area)
		if m.help.ShowAll {
			// Everything, on as many lines as it takes: `?` is the one
			// place a key cut off the single line can be read.
			binds = append(binds, k.Reload, k.Back, k.Tour)
			return "\n" + pad() + strings.Join(wrapHelp(m.help, binds, m.w-2*margin), "\n"+pad())
		}
	}
	return "\n" + pad() + ui.Truncate(m.help.ShortHelpView(binds), m.w-2*margin)
}

// wrapHelp lays the bindings out on lines no wider than width, breaking
// between bindings, never inside one.
func wrapHelp(h help.Model, binds []key.Binding, width int) []string {
	// help cuts its own line at its width with an ellipsis, which would
	// hide every break; this copy measures the whole line.
	h.SetWidth(0)
	var lines []string
	var line []key.Binding
	for _, b := range binds {
		if len(line) > 0 && ansi.StringWidth(h.ShortHelpView(append(line, b))) > width {
			lines = append(lines, h.ShortHelpView(line))
			line = nil
		}
		line = append(line, b)
	}
	if len(line) > 0 {
		lines = append(lines, ui.Truncate(h.ShortHelpView(line), width))
	}
	return lines
}

// hint is a footer entry that is not a binding of its own.
func hint(k, desc string) key.Binding { return key.NewBinding(key.WithKeys(k), key.WithHelp(k, desc)) }

// ── content ──────────────────────────────────────────────────────────────

func (m model) renderContent(room int) string {
	w := m.contentWidth()
	if m.pal.visible {
		return m.renderPalette(w)
	}
	var body string
	switch {
	case m.inCard():
		body = m.renderCard()
	case m.area.needsSession() && !m.session.SignedIn():
		body = m.heading(m.area.title(), "") +
			ui.Error(fmt.Errorf("not signed in - press s")) + "\n"
	case m.area == areaHome:
		body = m.renderHome(w, room)
	case m.area == areaProfile:
		body = m.renderProfile(w, room)
	case m.area == areaUsage:
		body = m.renderUsage(w, room)
	case m.area == areaModels:
		body = m.renderModels(w, room)
	case m.area == areaCosts:
		body = m.renderCosts(w)
	case m.area == areaHarness:
		body = m.renderHarness(w)
	case m.area == areaAbout:
		body = m.renderAbout(w, room)
	}
	if m.modal.kind != modalNone {
		// Over the top of the area, which stays visible under it.
		return overlay(body, m.renderModal(w))
	}
	return body
}

// overlay draws box over the top of block, a line down.
func overlay(block, box string) string {
	under := strings.Split(block, "\n")
	for i, l := range strings.Split(box, "\n") {
		if i+1 < len(under) {
			under[i+1] = l
		} else {
			under = append(under, l)
		}
	}
	return strings.Join(under, "\n")
}

func (m model) heading(title, summary string) string {
	out := "\n" + m.eyebrow(title) + "\n"
	if summary != "" {
		out += ui.Faint(summary) + "\n"
	}
	return out + "\n"
}

// eyebrow is a section heading, typed in with the rest of the screen.
func (m model) eyebrow(title string) string {
	return ui.EyebrowTyped(title, m.reveal, m.frame)
}

// selectable draws a line, with the cursor's bar and bold text when it is
// the selected one.
func selectable(on bool, text string) string {
	t := ui.Current()
	if on {
		return lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + " " +
			lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(ansi.Strip(text))
	}
	return "  " + text
}

// banner is the mascot and the wordmark at the top of a landing area, as
// much of it as the room allows: the mascot goes first, then the wordmark.
func (m model) banner(w, room int) string {
	switch {
	case w >= ui.BannerWidth(build.Version) && room >= ui.BannerRoom:
		return ui.Banner(build.Version, "", m.mood(), true)
	case w >= ui.BannerWidthCompact(build.Version):
		return ui.Banner(build.Version, "", m.mood(), false)
	}
	return ui.PlainBanner(build.Version, "")
}

func (m model) renderHome(w, room int) string {
	s := m.snap
	t := ui.Current()
	var b strings.Builder

	b.WriteString("\n" + m.banner(w, room) + "\n")

	// The pulse: the member's figures counting up as the area opens.
	if s.usage != nil {
		b.WriteString(figures(w,
			figure(ui.Tokens(float64(s.usage.Last24h.TotalTokens)*m.reveal), "tokens · 24 hours"),
			figure(ui.Tokens(float64(s.usage.Last30d.TotalTokens)*m.reveal), "tokens · 30 days"),
			figure(ui.Tokens(float64(s.usage.AllTime.TotalTokens)*m.reveal), "tokens · all time"),
		) + "\n")
	}
	rows := m.rows(areaHome)
	cur := m.cursor[areaHome]
	// A shape each, not only a colour: with NO_COLOR, or to a reader who
	// does not tell red from yellow, an alarm and a warning still differ.
	dot := map[severity]string{
		sevAlarm: lipgloss.NewStyle().Foreground(t.Red).Render(sevMark[sevAlarm]),
		sevWarn:  lipgloss.NewStyle().Foreground(t.Yellow).Render(sevMark[sevWarn]),
		sevInfo:  lipgloss.NewStyle().Foreground(t.TextFaint).Render(sevMark[sevInfo]),
	}
	arrive := func(i int, text string) string {
		if p := stagger(m.reveal, i); p < 1 {
			return decode(text, p, m.frame)
		}
		return text
	}

	b.WriteString("\n" + m.eyebrow("Needs you") + "\n\n")
	if len(m.sigs) == 0 {
		b.WriteString("  " + ui.Success("Nothing needs you right now.") + "\n")
	}
	i := 0
	for ; i < len(rows) && rows[i].sig != nil; i++ {
		sg := rows[i].sig
		where := ui.Faint("  → " + areaOf(sg.goTo).title())
		switch sg.goTo.kind {
		case cardUpdate:
			where = ui.Faint("  → enter: how to update")
		case cardSignIn:
			where = ui.Faint("  → enter: sign in")
		case cardReload:
			where = ui.Faint("  → enter or r: try again")
		}
		say := arrive(i, sg.say)
		if i == cur {
			say = lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(say)
			b.WriteString(ansi.Truncate(lipgloss.NewStyle().Foreground(t.Accent).Render("▌")+" "+dot[sg.sev]+" "+say+where, w, "…") + "\n")
		} else {
			b.WriteString(ansi.Truncate("  "+dot[sg.sev]+" "+say+where, w, "…") + "\n")
		}
	}

	if i < len(rows) && rows[i].newModel != "" {
		b.WriteString("\n" + m.eyebrow("What's new") + "\n\n")
		for ; i < len(rows) && rows[i].newModel != ""; i++ {
			line := ui.Pad("model", 9) + arrive(i, rows[i].newModel+" is available to your key")
			b.WriteString(ansi.Truncate(selectable(i == cur, line)+ui.Faint("  → Models, e adds it to your tools"), w, "…") + "\n")
		}
	}

	b.WriteString("\n" + m.eyebrow("What you can do") + "\n\n")
	for ; i < len(rows); i++ {
		q := rows[i].quick
		b.WriteString(selectable(i == cur, ui.Pad(arrive(i, q.label), 34)+ui.Faint(q.hint)) + "\n")
	}
	return b.String()
}

// motion is the reveal as a table sees it.
type motion struct {
	reveal float64
	frame  int
}

func (m model) motion() motion { return motion{reveal: m.reveal, frame: m.frame} }

// table draws rows under column headings with the cursor's row marked,
// scrolled so the cursor stays in view in the lines given.
func table(cols []column, rows []row, cursor, width, lines int, mo motion) string {
	t := ui.Current()
	fixed := 0
	for _, c := range cols {
		fixed += c.width + 2
	}
	flex := max(width-4-fixed, 12)
	widthOf := func(c column) int {
		if c.width == 0 {
			return flex
		}
		return c.width
	}
	cell := func(c column, s string) string {
		s = ansi.Truncate(s, widthOf(c), "…")
		if c.right {
			return ui.PadLeft(s, widthOf(c))
		}
		return ui.Pad(s, widthOf(c))
	}

	var b strings.Builder
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = cell(c, c.title)
	}
	b.WriteString("  " + ui.Faint(strings.Join(head, "  ")) + "\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(t.Line).Render(strings.Repeat("─", max(min(width-2, flex+fixed), 1))) + "\n")

	if len(rows) == 0 {
		return b.String() + "  " + ui.Faint("Nothing here yet.") + "\n"
	}
	// The header takes two lines, and the "1-3 of 9" line one more, but
	// only when there is something it does not show.
	show := max(lines-2, 1)
	if len(rows) > show {
		show = max(lines-3, 1)
	}
	first := 0
	if cursor >= show {
		first = cursor - show + 1
	}
	last := min(first+show, len(rows))
	for i := first; i < last; i++ {
		r := rows[i]
		cells := make([]string, len(cols))
		for j, c := range cols {
			v := ""
			if j < len(r.cells) {
				v = r.cells[j]
			}
			cells[j] = cell(c, v)
		}
		// The name decodes into place, row by row down the table.
		if p := stagger(mo.reveal, i-first); p < 1 && len(cells) > 0 {
			cells[0] = decode(ansi.Strip(cells[0]), p, mo.frame)
		}
		text := strings.Join(cells, "  ")
		switch {
		case i == cursor:
			// Bold cell by cell, so a coloured cell keeps its colour on
			// exactly the row somebody is looking at.
			bold := lipgloss.NewStyle().Foreground(t.Text).Bold(true)
			lit := make([]string, len(cells))
			for j, c := range cells {
				if ansi.Strip(c) == c {
					c = bold.Render(c)
				}
				lit[j] = c
			}
			b.WriteString(lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + " " + strings.Join(lit, "  ") + "\n")
		case r.dim:
			b.WriteString("  " + ui.Faint(ansi.Strip(text)) + "\n")
		default:
			b.WriteString("  " + text + "\n")
		}
	}
	if len(rows) > show {
		b.WriteString("  " + ui.Faint(fmt.Sprintf("%d-%d of %d", first+1, last, len(rows))) + "\n")
	}
	return b.String()
}

func (m model) renderProfile(w, room int) string {
	h := m.heading("Profile", "Your nan.builders account. Identifiers are shortened; o signs out.")
	if m.snap.err != nil && m.snap.me == nil {
		return h + ui.Error(loadProblem(m.snap.err)) + "\n"
	}
	return h + table(areaProfile.columns(), m.rows(areaProfile), m.cursor[areaProfile], w, room-lipgloss.Height(h), m.motion())
}

func (m model) renderUsage(w, room int) string {
	s := m.snap
	var b strings.Builder
	b.WriteString("\n" + m.eyebrow("Usage") + "\n\n")
	if s.usage == nil {
		if s.err != nil {
			b.WriteString(ui.Error(loadProblem(s.err)) + "\n")
		}
		return b.String()
	}
	cells := make([]string, 0, 3)
	for _, p := range s.periods() {
		in, out := p.Sum()
		cells = append(cells, figure(ui.Tokens(float64(p.TotalTokens)*m.reveal), "tokens · "+p.short+"\n"+
			ui.Tokens(in)+" in · "+ui.Tokens(out)+" out"))
	}
	b.WriteString(figures(w, cells...) + "\n\n")
	// No blank line between the credits and the chart: the chart's top row
	// is mostly empty and is the gap, and on an 80x24 terminal that line
	// is the difference between the chart fitting and not.
	b.WriteString(m.renderCredits(w - 2))
	// What is left goes, in order, to the table (its header and three
	// rows at least: the figures per model matter most), the chart, and
	// last the "By model" heading, which the table's own header already
	// says. So an 80x24 terminal gets the chart and loses only the heading.
	need := 2 + len(m.rows(areaUsage))
	if need > 5 {
		need = 2 + 3 + 1 // three rows and the "1-3 of n" line
	}
	// Lines, counted as newlines: every piece here ends with one, and
	// lipgloss.Height counts the empty line after it as one more.
	left := room - strings.Count(b.String(), "\n")
	if chart := m.renderChart(w); left-strings.Count(chart, "\n")-1 >= need {
		b.WriteString(chart)
		left -= strings.Count(chart, "\n")
	}
	if left-3 >= need {
		b.WriteString("\n" + m.eyebrow("By model") + "\n\n")
	} else {
		b.WriteString("\n")
	}
	head := b.String()
	return head + table(m.usageColumns(), m.rows(areaUsage), m.cursor[areaUsage], w, room-strings.Count(head, "\n"), m.motion())
}

// renderChart is the tokens of each of the last thirty days, and the
// busiest of them.
func (m model) renderChart(w int) string {
	dates, totals := m.snap.daily(time.Now())
	if len(totals) == 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range sparkline(totals, w-2, m.reveal) {
		b.WriteString(line + "\n")
	}
	label := "tokens per day, last " + fmt.Sprint(len(totals)) + " days"
	if i := peakDay(totals); i >= 0 && totals[i] > 0 {
		label += " · busiest " + shortDate(dates[i]) + ", " + ui.Tokens(totals[i])
	} else {
		label += " · none yet"
	}
	b.WriteString(ui.Faint(label) + "\n")
	return b.String()
}

// renderCredits is the member's credits this period, drawn as the web
// dashboard's card is: a heading with what is left on the right, a bar the
// width of the area, and under it what was used, when it resets and what
// the prompt cache saved. Then the credits bought on top, and a line when
// they cannot be spent at all. Nothing for a member not on credits.
func (m model) renderCredits(w int) string {
	c := m.snap.credits
	if c == nil || !c.Enabled {
		return ""
	}
	frac := 0.0
	if c.AllowanceCredits > 0 {
		frac = c.UsedCredits / c.AllowanceCredits
	}
	now := time.Now()
	var b strings.Builder

	left := ui.Value(credits(c.RemainingCredits)) + ui.Faint(" left")
	b.WriteString(spread(ui.Value("Credits this period"), left, w) + "\n")
	b.WriteString(creditsBar(frac*m.reveal, c.UsedCredits > 0, w) + "\n")

	used := ui.Value(credits(c.UsedCredits)) + ui.Faint(" / "+credits(c.AllowanceCredits)+" used")
	if c.PeriodEnd != "" {
		used += ui.Faint(" · resets " + soon(c.PeriodEnd, now))
	}
	saved := ""
	if c.CacheSavedCredits > 0 {
		saved = ui.Success("cache saved " + credits(c.CacheSavedCredits))
	}
	b.WriteString(spread(used, saved, w) + "\n")

	if c.PackCredits > 0 {
		pack := "+ " + credits(c.PackCredits) + " bought"
		if c.PackExpiresAt != nil {
			pack += ", until " + soon(*c.PackExpiresAt, now)
		}
		b.WriteString(ansi.Truncate(ui.Faint(pack), w, "…") + "\n")
	}
	switch {
	case c.Frozen:
		b.WriteString(ansi.Truncate(ui.Warn("Credits are on hold: requests that use them are refused until the membership is active again."), w, "…") + "\n")
	case c.FrontierPaused:
		b.WriteString(ansi.Truncate(ui.Faint("Frontier models are paused for now; the others answer as usual."), w, "…") + "\n")
	}
	return b.String()
}

// spread puts left at the start of a line of width w and right at its end,
// dropping right when the two do not fit.
func spread(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if right == "" || gap < 2 {
		return ansi.Truncate(left, w, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// partials are the left-aligned eighth blocks, so a bar's end lands on an
// eighth of a cell rather than a whole one.
var partials = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// creditsBar is fraction of width filled. The fill is the brand's colour,
// turning to the usage scale's yellow and red once three quarters are gone;
// the track is a lighter shade, so the two differ without colour too. Any
// use at all shows: 455 of 20,000 is a sliver, not nothing.
func creditsBar(fraction float64, used bool, width int) string {
	if width < 1 {
		return ""
	}
	fraction = min(max(fraction, 0), 1)
	eighths := int(math.Round(fraction * float64(width*8)))
	if used && eighths == 0 {
		eighths = 1
	}
	full, part := eighths/8, eighths%8
	t := ui.Current()
	fill := t.Accent
	if fraction >= 0.75 {
		fill = ui.Level(fraction)
	}
	bar := strings.Repeat("█", full) + partials[part]
	cells := full
	if part > 0 {
		cells++
	}
	return lipgloss.NewStyle().Foreground(fill).Render(bar) +
		lipgloss.NewStyle().Foreground(t.Line).Render(strings.Repeat("░", width-cells))
}

// credits is an amount of credits as the web dashboard writes it: to a
// tenth, without a trailing ".0", thousands separated (19,544.8).
func credits(n float64) string {
	s := strings.TrimSuffix(fmt.Sprintf("%.1f", n), ".0")
	whole, frac, _ := strings.Cut(s, ".")
	neg := strings.HasPrefix(whole, "-")
	whole = strings.TrimPrefix(whole, "-")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if neg {
		whole = "-" + whole
	}
	if frac != "" {
		return whole + "." + frac
	}
	return whole
}

// soon is an RFC 3339 time as a day, without the year when it is within the
// next eleven months, which is every renewal and most packs: "1 Dec".
func soon(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return day(ts)
	}
	if t.After(now.AddDate(0, 11, 0)) || t.Before(now.AddDate(0, -1, 0)) {
		return t.Local().Format("2 Jan 2006")
	}
	return t.Local().Format("2 Jan")
}

// shortDate is a YYYY-MM-DD day as "2 Nov", or the text as it came.
func shortDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("2 Jan")
	}
	return d
}

func (m model) renderModels(w, room int) string {
	s := m.snap
	summary := "What your key can call, and what you have spent on each."
	switch {
	case s.keyRefused:
		summary = "The cluster refused the API key on this machine: e to paste the current one."
	case m.session.APIKey == "" && m.session.SignedIn():
		summary = "The platform's list. With an API key (e) this is what your key can actually call."
	case m.session.APIKey == "" && !m.session.SignedIn():
		summary = "Sign in (s) or set an API key (e) to see the models."
	}
	h := m.heading("Models", summary)
	if len(s.models) == 0 && s.err != nil {
		return h + ui.Error(loadProblem(s.err)) + "\n"
	}
	return h + table(areaModels.columns(), m.rows(areaModels), m.cursor[areaModels], w, room-lipgloss.Height(h), m.motion())
}

func (m model) renderHarness(w int) string {
	var b strings.Builder
	b.WriteString("\n" + m.eyebrow("Harnesses") + "\n")
	for _, line := range strings.Split(ansi.Wrap("The coding tools on this machine that can talk to NaN, and the API key they use.", max(w-2, 20), ""), "\n") {
		b.WriteString(ui.Faint(line) + "\n")
	}
	b.WriteString("\n")

	// The key first, and the one thing to do right under it: on a short
	// terminal the explanation below is what gets cut, not the action.
	switch k := m.session.APIKey; {
	case k == "":
		b.WriteString(ui.Warn("No API key set: the tools here cannot reach NaN.") + "\n")
		if h := flow.KeyHint(m.snap.keyStatus); h != "" {
			b.WriteString(ui.Faint(h) + "\n")
		}
	case m.snap.keyRefused:
		b.WriteString(ui.Error(fmt.Errorf("the cluster refused the key on this machine - paste the current one")) + "\n")
	default:
		// Dots, never any of the key: not even its ends.
		b.WriteString(ui.Value(strings.Repeat("•", 24)) + "  " + ui.Faint("saved on this machine") + "\n")
		b.WriteString(ui.Faint("If anyone has seen it: replace it at cloud.nan.builders, paste the new one here.") + "\n")
	}
	b.WriteString("\n" + selectable(true, "Set up the tools") + "  " + ui.Faint("enter or e") + "\n")

	// Which tools here already use NaN comes before the explanation: it is
	// what a member opens this area to find out, and on a short terminal
	// the explanation is what gets cut.
	b.WriteString("\n" + m.eyebrow("On this machine") + "\n\n")
	installed := m.snap.installedTools()
	if len(installed) == 0 {
		b.WriteString("  " + ui.Faint("None of the supported tools is installed: "+tools.List()+".") + "\n")
	}
	for _, h := range installed {
		state := lipgloss.NewStyle().Foreground(ui.Current().Yellow).Render("○ not configured")
		switch {
		case h.Configured:
			state = ui.Success("configured with NaN")
		case !tools.Enabled(m.session.EnabledTools, h.Name):
			state = ui.Faint("unticked")
		}
		b.WriteString("  " + ui.Value(ui.Pad(h.Name, 12)) + ui.Pad(state, 22) + ui.Faint(ui.HomePath(h.ConfigPath)) + "\n")
	}
	// What setting it does, said as the steps it is, because "paste your
	// key" alone does not say that it will edit other programs' files.
	b.WriteString("\n" + m.eyebrow("What setting the key does") + "\n\n")
	steps := []string{
		"Checks the key against the cluster first, so a mistyped one is caught here.",
		"Writes it into each tool you tick, as a \"NaN\" provider pointed at " + api.InferenceURL() + ".",
		"Touches only that provider: the rest of each config, your other providers included, stays as it was.",
		"Keeps the key on this machine, readable by you alone: " + ui.HomePath(session.Path()) + ".",
	}
	hang := strings.Repeat(" ", 5)
	wrap := func(text string) string {
		return strings.ReplaceAll(ansi.Wrap(text, max(w-7, 20), ""), "\n", "\n"+hang)
	}
	for i, step := range steps {
		b.WriteString("  " + ui.Value(fmt.Sprintf("%d", i+1)) + "  " + wrap(step) + "\n")
	}
	for _, line := range strings.Split(ansi.Wrap("Untick a tool to take NaN back out of it; signing out (o) takes it out of all of them.", max(w-7, 20), ""), "\n") {
		b.WriteString(hang + ui.Faint(line) + "\n")
	}
	return b.String()
}

func (m model) renderAbout(w, room int) string {
	var b strings.Builder
	b.WriteString("\n" + m.banner(w, room) + "\n")
	rows := [][2]string{
		{"Version", ui.Value(build.Version)},
		{"Built with", ui.Faint(build.Detail())},
		{"Platform", ui.Accent("https://nan.builders")},
		{"Cloud", ui.Accent("https://cloud.nan.builders")},
		{"Source", ui.Accent(build.Repo)},
		{"Maintainer", ui.Value("@Nxssie") + ui.Faint(", and the Helmcode team")},
		// The real path, resolved: a member told to look somewhere finds
		// the file there, on Windows too.
		{"Session", ui.Mono(ui.HomePath(session.Path()))},
	}
	if v := m.upd.Available(); v != "" {
		rows = append(rows, [2]string{"Update", lipgloss.NewStyle().Foreground(ui.Current().Yellow).Render(v+" is out: ") + ui.Mono(update.Command())})
	}
	b.WriteString(ui.KVBlock(rows, "") + "\n")
	return b.String()
}

// ── a card ───────────────────────────────────────────────────────────────

func (m model) renderCard() string {
	t := ui.Current()
	c := m.currentCard()
	var b strings.Builder

	crumbs := []string{ui.Faint(m.area.title())}
	for i, st := range m.stack {
		title := buildCard(m.snap, st, 0, 1).title
		if i == len(m.stack)-1 {
			crumbs = append(crumbs, ui.Value(title))
		} else {
			crumbs = append(crumbs, ui.Faint(title))
		}
	}
	b.WriteString("\n" + strings.Join(crumbs, ui.Faint(" › ")) + "\n\n")
	top := m.stack[len(m.stack)-1]
	b.WriteString(m.eyebrow(kindLabel(top.kind)) + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(c.title) + "\n\n")
	b.WriteString(c.body + "\n")
	if len(c.links) > 0 {
		b.WriteString("\n" + m.eyebrow("Linked") + "\n\n")
		at := m.links[len(m.links)-1]
		for i, l := range c.links {
			b.WriteString(selectable(i == at, ui.Pad(ui.Truncate(l.label, 32), 32)+"  "+ui.Faint(l.hint)) + "\n")
		}
	}
	return b.String()
}

// ── modals ───────────────────────────────────────────────────────────────

func (m model) renderModal(w int) string {
	t := ui.Current()
	width := min(w, cardWidth)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(t.Accent).
		Padding(0, 2).Width(width)
	inner := width - 6

	switch m.modal.kind {
	case modalMenu:
		var b strings.Builder
		if r := m.selectedRow(); r != nil && len(r.cells) > 0 {
			b.WriteString(ui.Faint(ansi.Strip(r.cells[0])) + "\n\n")
		}
		for i, it := range m.modal.items {
			b.WriteString(selectable(i == m.modal.cursor, ui.Pad(it.label, 34)+ui.Faint(it.key)) + "\n")
		}
		return box.Render(strings.TrimRight(b.String(), "\n"))

	case modalLink:
		var b strings.Builder
		b.WriteString(m.eyebrow(m.modal.title) + "\n\n")
		b.WriteString(ui.Accent(ansi.Hardwrap(m.modal.url, inner, true)) + "\n\n")
		b.WriteString(ui.Faint(m.modal.note) + "\n")
		if m.modal.copied {
			b.WriteString("\n" + ui.Success("Copied to the clipboard.") + "\n")
		}
		return box.Render(strings.TrimRight(b.String(), "\n"))
	}
	return ""
}

// ── the palette ──────────────────────────────────────────────────────────

func (m model) renderPalette(w int) string {
	t := ui.Current()
	var b strings.Builder
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Accent).Padding(0, 1).Width(w)
	b.WriteString("\n" + box.Render(m.pal.input.View()) + "\n\n")
	if len(m.pal.shown) == 0 {
		return b.String() + "  " + ui.Faint("Nothing matches.") + "\n"
	}
	first := max(m.pal.cursor-maxResults+1, 0)
	last := min(first+maxResults, len(m.pal.shown))
	tag := lipgloss.NewStyle().Foreground(t.AccentText).Width(8)
	for i := first; i < last; i++ {
		e := m.pal.shown[i]
		b.WriteString(selectable(i == m.pal.cursor, tag.Render(e.kind)+ui.Pad(ui.Truncate(e.name, 40), 42)+ui.Faint(e.hint)) + "\n")
	}
	if n := len(m.pal.shown); n > maxResults {
		b.WriteString("  " + ui.Faint(fmt.Sprintf("%d of %d", last-first, n)) + "\n")
	}
	return b.String()
}

// loadProblem is a failed load said as what to do about it. A network that
// did not answer is not the member's mistake, and the Go error under it
// (`dial tcp: lookup nan.builders: no such host`) says nothing they can act
// on.
func loadProblem(err error) error {
	if errors.Is(err, api.ErrUnreachable) {
		return errors.New("cannot reach nan.builders: check the connection, r to try again")
	}
	return fmt.Errorf("%w - r to try again", err)
}

// sevMark is the shape a signal's severity is drawn with.
var sevMark = map[severity]string{sevAlarm: "✗", sevWarn: "!", sevInfo: "○"}

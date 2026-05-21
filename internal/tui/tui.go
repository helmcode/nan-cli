package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nxssie/nan-cli/internal/api"
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
	tabProfile tabID = iota
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

// ── model ─────────────────────────────────────────────────────────────────────

type model struct {
	client     *api.Client
	sess       *session.Session
	active     int
	loading    bool
	err        error
	spin       spinner.Model
	cache      map[tabID]any
	lay        layout
	scrollY    int
	showHelp   bool
	keyInput    textinput.Model
	editingKey  bool
	setupMsg    string
	setupCursor int
}

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

	return model{
		client:   client,
		sess:     sess,
		cache:    make(map[tabID]any),
		spin:     sp,
		lay:      newLayout(80, 24),
		keyInput: ti,
	}
}

func (m model) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(m.spin.Tick, m.fetchTab(tabDefs[0].id))
}

func (m model) activeID() tabID { return tabDefs[m.active].id }

// ── update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.lay = newLayout(msg.Width, msg.Height)

	case spinner.TickMsg:
		if m.loading {
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

	case tea.KeyMsg:
		// When the API key input is active, route all keys to it
		if m.editingKey {
			switch msg.String() {
			case "enter":
				val := strings.TrimSpace(m.keyInput.Value())
				if val != "" {
					m.sess.APIKey = val
					if err := session.Save(m.sess); err != nil {
						m.setupMsg = "error saving: " + err.Error()
					} else {
						m.setupMsg = "API key saved"
					}
				}
				m.editingKey = false
				m.keyInput.Blur()
			case "esc":
				m.editingKey = false
				m.keyInput.Blur()
				m.setupMsg = ""
			default:
				var cmd tea.Cmd
				m.keyInput, cmd = m.keyInput.Update(msg)
				return m, cmd
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			if m.showHelp {
				m.showHelp = false
			} else {
				return m, tea.Quit
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
				if m.activeID() == tabSetup {
					if m.setupCursor > 0 {
						m.setupCursor--
					}
				} else if m.scrollY > 0 {
					m.scrollY--
				}
			}
		case "down", "j":
			if !m.showHelp {
				if m.activeID() == tabSetup {
					m.setupCursor++
				} else {
					m.scrollY++
				}
			}
		case " ":
			if !m.showHelp && m.activeID() == tabSetup {
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
		case "e":
			if !m.showHelp && m.activeID() == tabSetup {
				m.editingKey = true
				m.keyInput.SetValue("")
				m.keyInput.Focus()
				m.setupMsg = ""
			}
		case "c":
			if !m.showHelp && m.activeID() == tabSetup && m.sess.APIKey != "" {
				msg := configureTools(m.sess.APIKey, m.sess.EnabledTools)
				m.setupMsg = msg
			}
		}
	}
	return m, nil
}

func (m *model) maybeLoad() tea.Cmd {
	id := m.activeID()
	if id == tabAbout || id == tabSetup {
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

func (m model) fetchTab(id tabID) tea.Cmd {
	client := m.client
	return func() tea.Msg {
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
	b.WriteString(renderTabBar(m.active, l) + "\n\n")

	// Content area height: total minus tab-bar, blank, blank-before-footer, footer
	contentH := l.h - 4
	if contentH < 1 {
		contentH = 1
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
	b.WriteString(lipgloss.NewStyle().Foreground(cGray).Render(l.indent+hint))
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
	name       string
	mode       string
	tokens30d  float64
	tokens24h  float64
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
}

func renderModels(data any, usageData any, l layout) string {
	// Extract models list (unwrap {"models": [...]})
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

	// Build model list
	models := make([]modelInfo, 0, len(raw))
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

		color := modeColor[mi.mode]
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
	model       string
	provider    string
	inPer1M     float64 // $ per 1M input tokens
	outPer1M    float64 // $ per 1M output tokens
}

// Prices as of mid-2026 (per 1M tokens).
var pricingTable = []providerPricing{
	{"Claude Sonnet 4.6", "Anthropic", 3.00, 15.00},
	{"Claude Haiku 4.5", "Anthropic", 1.00, 5.00},
	{"GPT-5.5", "OpenAI", 5.00, 20.00},
	{"GPT-5.4 Mini", "OpenAI", 0.40, 1.60},
	{"Gemini 3.1 Pro", "Google", 2.00, 8.00},
	{"Gemini 2.5 Flash", "Google", 0.35, 1.05},
}

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
	if v >= 1000 {
		return fmt.Sprintf("$%,.2f", v)
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

	rows := make([]row, len(pricingTable))
	for i, p := range pricingTable {
		pColor, ok := providerColor[p.provider]
		if !ok {
			pColor = cGray
		}
		plain := p.model + "  " + p.provider
		styled := lipgloss.NewStyle().Foreground(cWhite).Bold(true).Render(p.model) +
			"  " + lipgloss.NewStyle().Foreground(pColor).Render(p.provider)
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
	subtitle := "Estimated cost of your usage on other providers."
	if l.w >= 72 {
		subtitle = "Estimated cost based on your actual input/output tokens on other providers."
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

	// Data rows — pad label using plain-text length, then right-align costs
	for _, r := range rows {
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
		b.WriteString(l.indent + line + "\n\n")
	}

	// Footer — measure plain text width first to avoid border miscalculation
	const notePlain = "NaN — Your usage is included in your membership. No per-token charges."
	nanStyle := lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	note := nanStyle.Render("NaN") +
		lipgloss.NewStyle().Foreground(cGray).Render(" — Your usage is included in your membership. No per-token charges.")
	noteStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlueDim).
		Padding(0, 1).
		Width(len(notePlain))
	b.WriteString(l.indent + noteStyle.Render(note) + "\n")

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
	var out []rune
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out = append(out, ' ')
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
			configPath:  filepath.Join(home, ".pi", "agent", "extensions", "nan.ts"),
			installPath: filepath.Join(home, ".pi"),
		},
		{
			name:       "Codex",
			binary:     "codex",
			configPath: filepath.Join(home, ".codex", "config.toml"),
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
		return strings.Contains(string(data), "api.nan.builders")
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

func configureTools(apiKey string, enabledTools map[string]bool) string {
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
	var lastErr error
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
			}
			if err != nil {
				lastErr = err
			} else {
				added++
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
			}
			if err != nil {
				lastErr = err
			} else {
				removed++
			}
		}
	}
	if lastErr != nil {
		return "error: " + lastErr.Error()
	}
	parts := []string{}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", removed))
	}
	if len(parts) == 0 {
		return "nothing to sync"
	}
	return strings.Join(parts, "  ·  ")
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
	nanModels := []struct{ id, display string }{
		{"qwen3.6", "Qwen 3.6 35B A3B (NaN)"},
		{"gemma4", "Gemma 4 26B A4B (NaN)"},
		{"deepseek-v4-flash", "DeepSeek V4 Flash 284B A13B (NaN)"},
	}
	added := false
	for _, nm := range nanModels {
		if !existingIDs[nm.id] {
			idx := len(models)
			models = append(models, map[string]any{
				"model":          nm.id,
				"id":             factoryCustomID(nm.display, idx),
				"index":          idx,
				"baseUrl":        "https://api.nan.builders/v1",
				"apiKey":         apiKey,
				"displayName":    nm.display,
				"noImageSupport": false,
				"provider":       "openai",
			})
			added = true
		}
	}
	if !added {
		return nil
	}
	cfg["customModels"] = models

	// Set qwen3.6 as default model if sessionDefaultSettings not present
	if _, ok := cfg["sessionDefaultSettings"]; !ok {
		for _, m := range models {
			if id, _ := m["model"].(string); id == "qwen3.6" {
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

	nanModels := map[string]any{
		"qwen3.6":           map[string]any{"name": "Qwen 3.6 35B A3B"},
		"gemma4":            map[string]any{"name": "Gemma 4 26B A4B"},
		"deepseek-v4-flash": map[string]any{"name": "DeepSeek V4 Flash 284B A13B"},
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
					if _, found := existing[id]; !found {
						existing[id] = m
						changed = true
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

func writePiConfig(cfgPath, apiKey string) error {
	if data, err := os.ReadFile(cfgPath); err == nil {
		if strings.Contains(string(data), "api.nan.builders") {
			return nil // already configured
		}
	}
	const tmpl = `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
  pi.registerProvider("nan", {
    name: "NaN",
    baseUrl: "https://api.nan.builders/v1",
    apiKey: %q,
    api: "openai-completions",
    models: [
      {
        id: "qwen3.6",
        name: "Qwen 3.6 35B A3B",
        reasoning: false,
        input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 128000,
        maxTokens: 8192,
      },
      {
        id: "gemma4",
        name: "Gemma 4 26B A4B",
        reasoning: false,
        input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 128000,
        maxTokens: 8192,
      },
      {
        id: "deepseek-v4-flash",
        name: "DeepSeek V4 Flash 284B A13B",
        reasoning: false,
        input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 128000,
        maxTokens: 8192,
      },
    ],
  });
}
`
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, []byte(fmt.Sprintf(tmpl, apiKey)), 0o600)
}

func removePiConfig(cfgPath string) error {
	err := os.Remove(cfgPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func writeCodexConfig(cfgPath, apiKey string) error {
	data, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data), "api.nan.builders") {
		return nil
	}

	// If no existing config, write a complete starter config.
	if len(data) == 0 {
		content := fmt.Sprintf(`model = "gemma4"
model_provider = "nan"
model_context_window = 131072

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "responses"
`, apiKey)
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			return err
		}
		return os.WriteFile(cfgPath, []byte(content), 0o600)
	}

	// Existing config: only append the provider section; preserve user's model/provider choices.
	// model_context_window suppresses the "metadata not found" warning for nan models.
	section := fmt.Sprintf(`
model_context_window = 131072

[model_providers.nan]
name = "NaN"
base_url = "https://api.nan.builders/v1"
experimental_bearer_token = %q
wire_api = "responses"
`, apiKey)
	content := strings.TrimRight(string(data), "\n") + "\n" + section
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, []byte(content), 0o600)
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

	// ── Tools ────────────────────────────────────────────────────────────────
	b.WriteString("\n" + l.indent + titleStyle.Render("Tools") + "\n")
	if m.setupMsg != "" {
		style := okStyle
		if strings.HasPrefix(m.setupMsg, "error") {
			style = errStyle
		}
		b.WriteString(l.indent + style.Render(m.setupMsg) + "\n")
	}
	b.WriteString("\n")

	tools := detectTools()
	cursor := m.setupCursor
	if len(tools) > 0 && cursor >= len(tools) {
		cursor = len(tools) - 1
	}

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

	b.WriteString("\n")
	if m.sess.APIKey == "" {
		b.WriteString(l.indent + dimStyle.Render("Set an API key first (e).") + "\n")
	} else {
		b.WriteString(l.indent + dimStyle.Render("↑/↓ navigate   space toggle   c configure selected") + "\n")
	}

	return b.String()
}

// ── about renderer ───────────────────────────────────────────────────────────

const Version = "0.1.1"

func renderAbout(l layout) string {
	var b strings.Builder

	logoStyle := lipgloss.NewStyle().Bold(true).Foreground(cCyan)
	dimStyle := lipgloss.NewStyle().Foreground(cDimGray)
	labelStyle := lipgloss.NewStyle().Foreground(cGray).Width(l.keyW)
	linkStyle := lipgloss.NewStyle().Foreground(cBlue)
	accentStyle := lipgloss.NewStyle().Foreground(cCyan)
	sectionStyle := lipgloss.NewStyle().Foreground(cGray).Bold(true)

	b.WriteString(l.indent + logoStyle.Render("nan") +
		"  " + dimStyle.Render("v"+Version) + "\n")
	b.WriteString(l.indent + dimStyle.Render("nan.builders cloud CLI") + "\n\n")

	b.WriteString(l.indent + sectionStyle.Render("Links") + "\n\n")
	b.WriteString(l.indent + labelStyle.Render("Platform:") +
		linkStyle.Render("https://nan.builders") + "\n")
	b.WriteString(l.indent + labelStyle.Render("Cloud:") +
		linkStyle.Render("https://cloud.nan.builders") + "\n\n")

	b.WriteString(l.indent + sectionStyle.Render("Maintainer") + "\n\n")
	b.WriteString(l.indent + labelStyle.Render("Author:") +
		accentStyle.Render("@Nxssie") + "\n\n")

	b.WriteString(l.indent + sectionStyle.Render("Session") + "\n\n")
	b.WriteString(l.indent + labelStyle.Render("Config:") +
		dimStyle.Render("~/.config/nan/session.json") + "\n")

	return b.String()
}

// ── help overlay ─────────────────────────────────────────────────────────────

func renderHelp() string {
	shortcuts := []struct{ key, desc string }{
		{"←/→  h/l  Tab", "switch tabs"},
		{"↑/↓  k/j", "scroll"},
		{"r", "refresh current tab"},
		{"e", "edit API key (Setup tab)"},
		{"c", "configure tools (Setup tab)"},
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
	sess, err := session.Load()
	if err != nil {
		return err
	}
	client := api.New(sess.Token)
	m := newModel(client, sess)
	m.loading = true
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

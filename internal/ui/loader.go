package ui

import (
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The loading indicator: a band of light sweeping across a word.
//
// A one-character spinner says "something is happening" and nothing else, and
// at the size of a dashboard it is easy to miss entirely. A word that moves
// says the same thing at the size of the sentence it is in, reads at a glance
// from across a desk, and — the part that matters here — is made of the
// brand's own two indigos rather than of whatever a spinner character
// inherits.
//
// It is a sweep rather than a pulse because a pulse and a frozen program look
// the same at the moment you glance at them. Motion along an axis does not.

// LoaderFPS is how often the band moves. Slow enough not to strobe, fast
// enough to read as motion rather than as a redraw.
const LoaderFPS = time.Second / 16

// Loader is an animated label. Embed it, forward its messages, call View.
type Loader struct {
	label string
	phase int
	ramp  []color.Color
}

// LoaderTickMsg advances the animation.
type LoaderTickMsg struct{}

// NewLoader builds one. The ramp is computed once: blending colours is the
// expensive part, and it does not change between frames.
func NewLoader(label string) Loader {
	t := Current()
	return Loader{
		label: label,
		// Dim at the edges, BRIGHTEST at the peak. The order matters and is
		// easy to get backwards: with the fill indigo last, the travelling
		// band comes out darker than the characters beside it, which reads
		// as a hole moving along the word rather than a light.
		ramp: lipgloss.Blend1D(rampSteps, t.TextFaint, t.Accent, t.AccentText),
	}
}

const rampSteps = 12

// SetLabel changes the word without restarting the animation, so a loader
// that goes from "Loading" to "Checking the key" does not stutter.
func (l Loader) SetLabel(label string) Loader {
	l.label = label
	return l
}

// Tick starts (or continues) the animation.
func (l Loader) Tick() tea.Cmd {
	return tea.Tick(LoaderFPS, func(time.Time) tea.Msg { return LoaderTickMsg{} })
}

// Update advances one frame.
func (l Loader) Update(msg tea.Msg) (Loader, tea.Cmd) {
	if _, ok := msg.(LoaderTickMsg); !ok {
		return l, nil
	}
	l.phase++
	return l, l.Tick()
}

// View renders the label with the band where it currently is.
func (l Loader) View() string {
	runes := []rune(l.label)
	if len(runes) == 0 {
		return ""
	}

	// The band travels a little past each end, so it leaves and re-enters
	// rather than bouncing off the last character.
	const overshoot = 6
	span := len(runes) + overshoot
	head := l.phase % span

	var b strings.Builder
	for i, r := range runes {
		distance := i - head
		if distance < 0 {
			distance = -distance
		}
		// Far from the band is the dim end of the ramp; on it is the bright
		// end. Anything past the ramp's length is simply the dimmest.
		step := rampSteps - 1 - distance*2
		if step < 0 {
			step = 0
		}
		b.WriteString(lipgloss.NewStyle().Foreground(l.ramp[step]).Render(string(r)))
	}
	return b.String()
}

// Width is what the loader occupies, for laying out around it.
func (l Loader) Width() int { return lipgloss.Width(l.label) }

package ui

import (
	"os"
	"strconv"

	"github.com/charmbracelet/x/term"
)

// How wide the terminal is, asked once and in one place.
//
// Two things need this and they used to disagree: the printer, which sizes
// tables, and the forms, which have to be given a width or huh leaves their
// text input at zero — and a text input with no width draws only the first
// character of its placeholder, so "you@example.com" arrives as a lone "y"
// under the cursor and the prompt stops saying what it wants.

const (
	// pipeWidth is what something that is not a terminal gets. Wide enough
	// for the tables here, narrow enough to survive being pasted into an
	// issue.
	pipeWidth = 100
	// minWidth is the floor. Below this nothing useful fits, and a table
	// squeezed into 20 columns is worse than one that overflows a little.
	minWidth = 40
	// formWidth is as wide as a form is ever drawn. Below it a form takes
	// the terminal, margins aside; above it, the lines of a description
	// would be longer than anybody reads comfortably in one sweep of the
	// eye, and a prompt spanning an ultrawide screen reads as the program
	// having taken it over.
	formWidth = 100
	// pipeHeight is the height assumed when there is no terminal to ask.
	pipeHeight = 40
)

// TerminalWidth is the width output should be drawn to.
//
// COLUMNS first: it is the standard way to say how wide the output should
// be, it is what `COLUMNS=60 helmcode keys` means, and it is the only handle
// anybody has when the destination is not a terminal at all.
func TerminalWidth() int {
	if v := os.Getenv("COLUMNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return clampWidth(n)
		}
	}
	if term.IsTerminal(os.Stdout.Fd()) {
		if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
			return clampWidth(w)
		}
	}
	return pipeWidth
}

// TerminalSize is the terminal's width, as TerminalWidth has it, and its
// height.
func TerminalSize() (width, height int) {
	width, height = TerminalWidth(), pipeHeight
	if term.IsTerminal(os.Stdout.Fd()) {
		if _, h, err := term.GetSize(os.Stdout.Fd()); err == nil && h > 0 {
			height = h
		}
	}
	return width, height
}

// FormWidth is how wide to draw a form in this terminal.
func FormWidth() int { return FormWidthFor(TerminalWidth()) }

// FormWidthFor is how wide to draw a form in a terminal w columns wide: the
// terminal with a margin down each side, up to formWidth.
func FormWidthFor(w int) int {
	if w -= 4; w < formWidth {
		return max(w, minWidth-4)
	}
	return formWidth
}

func clampWidth(w int) int {
	if w < minWidth {
		return minWidth
	}
	return w
}

// Package build carries what the binary knows about itself.
package build

import "runtime"

// Version is the release this binary was cut from.
//
// Bumping it is the act of publishing: once a commit that changes it passes
// CI on main, .github/workflows/tag.yml tags it and the release follows. The
// workflow reads it from this line, so it stays a plain string literal.
var Version = "0.2.0"

// Name is the binary as it is invoked, used wherever a message has to spell
// out a command for someone to type.
const Name = "nan"

// Repo is where this comes from, shown in About and named by the installer
// and the release workflow. One constant because the three of them going out
// of step is the kind of thing nobody notices until somebody follows the
// wrong one.
const Repo = "https://github.com/helmcode/nan-cli"

// Line is what `--version` prints: one line, parseable, the version in the
// second field.
//
// A framed banner is the wrong answer here however good it looks. `--version`
// is read by scripts, pasted into issues and diffed between machines, and all
// three of those want `nan 0.2.0`, not a box. The banner still exists - it
// is what the dashboard's Home and About draw - but it is not what a version
// flag is for.
func Line() string {
	return Name + " " + Version
}

// Detail is the second line: the toolchain and platform this binary was built
// for. It costs nothing and it is the first thing anybody asks for in a bug
// report.
func Detail() string {
	return runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
}

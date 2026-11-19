// Package contexts holds the agent-context domain of devops AGENT-CONTEXT.md:
// path validators, content typing, the Office and secret checks, the strict
// SKILL.md decoder and the quota rules. Everything in this file set is pure
// (no database, no R2): the API, the manifest builder, nan-run and the CLI
// share it.
package contexts

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Reason is the per-file refusal code the UI and CLI show next to a file
// (AGENT-CONTEXT.md §1.5). The wire values are part of the API contract.
type Reason string

const (
	ReasonDotfile         Reason = "dotfile"
	ReasonBadCharacter    Reason = "bad_character"
	ReasonNotNFC          Reason = "not_nfc"
	ReasonTooLong         Reason = "too_long"
	ReasonTooDeep         Reason = "too_deep"
	ReasonReservedName    Reason = "reserved_name"
	ReasonCaseCollision   Reason = "case_collision"
	ReasonBadTopLevel     Reason = "bad_top_level"
	ReasonNotSupportedYet Reason = "not_supported_yet" // mcp/ (reserved for v2)
)

// Stored-path limits (§1.5).
const (
	MaxStoredPathBytes  = 512
	MaxStoredPathDepth  = 8
	MaxComponentBytes   = 128
	InstructionsPath    = "AGENTS.md"
	SkillFileName       = "SKILL.md"
	topSkills           = "skills"
	topFiles            = "files"
	topMCP              = "mcp"
	contextNameMaxBytes = 63
)

// PathError is one refused path with its reason.
type PathError struct {
	Path   string
	Reason Reason
}

func (e *PathError) Error() string { return fmt.Sprintf("contexts: invalid path (%s)", e.Reason) }

// ErrInvalidPath matches every *PathError with errors.Is.
var ErrInvalidPath = errors.New("contexts: invalid path")

// Is makes errors.Is(err, ErrInvalidPath) hold for a *PathError.
func (e *PathError) Is(target error) bool { return target == ErrInvalidPath }

func pathErr(p string, r Reason) *PathError { return &PathError{Path: p, Reason: r} }

var (
	reContextName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reSkillName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// ValidContextName reports whether name is an acceptable context name: the
// contexts_name_shape CHECK of migration 126, and not a Windows reserved
// basename (`con`, `nul`, `com1`, ...). The second rule is not in the SQL
// CHECK: a context's name becomes a directory of the run's manifest
// (files/<name>/...), and the manifest validator refuses reserved names, so
// a context named `con` could never deliver its files.
//
// Two more names are refused because the API could never reach them by
// name: a UUID-shaped name (GET /v1/contexts/{x} always reads it as an id)
// and "blobs" (the /v1/contexts/blobs/... upload routes).
func ValidContextName(name string) bool {
	return reContextName.MatchString(name) && !isWindowsReserved(name) && name != reservedBlobs && !uuidShaped(name)
}

const reservedBlobs = "blobs"

// uuidShaped reports the 8-4-4-4-12 lowercase hex form.
func uuidShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// ValidSkillName reports whether name is an acceptable skill directory name.
func ValidSkillName(name string) bool { return reSkillName.MatchString(name) }

// ValidateStoredPath checks one path a member publishes (§1.5 "Stored
// paths"). It returns nil or a *PathError carrying the reason. Uniqueness
// (case_collision) is a property of the whole tree: see ValidateTree.
func ValidateStoredPath(p string) error {
	comps, err := checkPathShape(p, MaxStoredPathBytes, MaxStoredPathDepth)
	if err != nil {
		return err
	}
	switch top := comps[0]; {
	case len(comps) == 1 && top == InstructionsPath:
		return nil
	case top == topSkills:
		// skills/<name>/<at least one file>
		if len(comps) < 3 {
			return pathErr(p, ReasonBadTopLevel)
		}
		if !ValidSkillName(comps[1]) {
			return pathErr(p, ReasonBadCharacter)
		}
		return nil
	case top == topFiles:
		if len(comps) < 2 {
			return pathErr(p, ReasonBadTopLevel)
		}
		return nil
	case top == topMCP:
		return pathErr(p, ReasonNotSupportedYet)
	default:
		return pathErr(p, ReasonBadTopLevel)
	}
}

// checkPathShape applies the rules shared by stored and manifest paths:
// UTF-8, NFC, byte and depth limits, and the component rules.
func checkPathShape(p string, maxBytes, maxDepth int) ([]string, error) {
	if !utf8.ValidString(p) {
		return nil, pathErr(p, ReasonBadCharacter)
	}
	if !norm.NFC.IsNormalString(p) {
		return nil, pathErr(p, ReasonNotNFC)
	}
	if len(p) == 0 {
		return nil, pathErr(p, ReasonBadCharacter)
	}
	if len(p) > maxBytes {
		return nil, pathErr(p, ReasonTooLong)
	}
	comps := strings.Split(p, "/")
	if len(comps) > maxDepth {
		return nil, pathErr(p, ReasonTooDeep)
	}
	for _, c := range comps {
		if r := componentReason(c); r != "" {
			return nil, pathErr(p, r)
		}
	}
	return comps, nil
}

// componentReason returns the reason one path component is refused, or "".
func componentReason(c string) Reason {
	if c == "" {
		return ReasonBadCharacter // empty component: leading, trailing or doubled '/'
	}
	if len(c) > MaxComponentBytes {
		return ReasonTooLong
	}
	if c[0] == '.' {
		return ReasonDotfile // .git, .env.example, ., ..
	}
	if c[0] == ' ' || c[len(c)-1] == ' ' || c[len(c)-1] == '.' {
		return ReasonBadCharacter
	}
	for _, r := range c {
		if !allowedRune(r) {
			return ReasonBadCharacter
		}
	}
	if isWindowsReserved(c) {
		return ReasonReservedName
	}
	return ""
}

// allowedRune: Unicode letters (L*), combining marks (Mn, Mc), decimal
// digits (Nd), the ASCII space and `. _ - ( ) + , @`, minus every
// Default_Ignorable_Code_Point. Everything else (Cc, Cf, other separators,
// symbols, emoji, private use, unassigned, shell/Windows-hostile ASCII) is
// refused.
func allowedRune(r rune) bool {
	switch r {
	case ' ', '.', '_', '-', '(', ')', '+', ',', '@':
		return true
	}
	if !(unicode.IsLetter(r) || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd)) {
		return false
	}
	return !defaultIgnorable(r)
}

// defaultIgnorable covers Default_Ignorable_Code_Point among the categories
// allowedRune admits: the derived property is Other_Default_Ignorable_Code_Point
// + Cf + Variation_Selector (minus a few code points that are Cf, refused
// anyway). So Hangul fillers (Lo), U+034F and the Khmer inherent vowels (Mn)
// and every variation selector (Mn) are refused.
func defaultIgnorable(r rune) bool {
	return unicode.In(r, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector, unicode.Cf)
}

var windowsReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for i := 1; i <= 9; i++ {
		windowsReserved[fmt.Sprintf("COM%d", i)] = true
		windowsReserved[fmt.Sprintf("LPT%d", i)] = true
	}
}

// isWindowsReserved: the basename before the first '.' (any extension, any
// case) is CON, PRN, AUX, NUL, COM1..COM9 or LPT1..LPT9.
func isWindowsReserved(c string) bool {
	base := c
	if i := strings.IndexByte(c, '.'); i >= 0 {
		base = c[:i]
	}
	return windowsReserved[strings.ToUpper(base)]
}

var folder = cases.Fold()

// FoldKey is the case-insensitive identity of a path: Unicode full case
// folding of the NFC form, re-normalised (folding İ yields a combining dot).
// Two paths with the same key collide on a case-insensitive file system.
func FoldKey(p string) string {
	return norm.NFC.String(folder.String(norm.NFC.String(p)))
}

// ValidateTree validates every path of one version and their uniqueness: no
// two paths with the same FoldKey, and no path whose folded form is also a
// folder of another path (a file and a folder cannot share a name on any
// file system). It returns every refusal (sorted by path), not only the
// first, so the UI and CLI can mark each file.
func ValidateTree(paths []string) []PathError {
	var out []PathError
	seen := make(map[string]string, len(paths)) // fold key -> first path
	valid := make([]string, 0, len(paths))
	for _, p := range paths {
		if err := ValidateStoredPath(p); err != nil {
			var pe *PathError
			errors.As(err, &pe)
			out = append(out, *pe)
			continue
		}
		k := FoldKey(p)
		if _, dup := seen[k]; dup {
			out = append(out, PathError{Path: p, Reason: ReasonCaseCollision})
			continue
		}
		seen[k] = p
		valid = append(valid, p)
	}
	// File/folder clash: a valid path whose folded form is a folder prefix
	// of another valid path.
	dirs := make(map[string]bool)
	for _, p := range valid {
		k := FoldKey(p)
		for i := 0; i < len(k); i++ {
			if k[i] == '/' {
				dirs[k[:i]] = true
			}
		}
	}
	for _, p := range valid {
		if dirs[FoldKey(p)] {
			out = append(out, PathError{Path: p, Reason: ReasonCaseCollision})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

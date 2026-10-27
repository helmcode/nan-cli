package contexts

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Parity table: copied from cloud-api internal/contexts/path_test.go (the
// middleware name-shape test is left out: nan-cli has no route table). The
// copy must stay in step with the server; cloud-api's CI checks that it does.
//
// Stored paths (devops AGENT-CONTEXT.md section 1.5): every refusal carries its
// per-file reason; the table covers the Unicode rules and the exact maxima.

func reasonOf(err error) Reason {
	var pe *PathError
	if errors.As(err, &pe) {
		return pe.Reason
	}
	return ""
}

// rep builds a component of n bytes from an ASCII letter.
func rep(n int) string { return strings.Repeat("a", n) }

// pathOf builds a files/ path of exactly total bytes with depth components
// (files + depth-1 components), every component at most 128 bytes.
func pathOf(t *testing.T, total, depth int) string {
	t.Helper()
	comps := []string{"files"}
	left := total - len("files")
	n := depth - 1
	for i := 0; i < n; i++ {
		remainingComps := n - i
		// bytes for this component: its share of what is left after the separators
		avail := left - remainingComps // one '/' per remaining component
		share := avail / remainingComps
		if i == n-1 {
			share = avail
		}
		if share > MaxComponentBytes {
			share = MaxComponentBytes
		}
		comps = append(comps, rep(share))
		left -= share + 1
	}
	p := strings.Join(comps, "/")
	if len(p) != total || len(comps) != depth {
		t.Fatalf("pathOf(%d,%d) built %d bytes, %d comps", total, depth, len(p), len(comps))
	}
	return p
}

func TestStoredPathTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want Reason // "" = accepted
	}{
		// Layout.
		{"instructions", "AGENTS.md", ""},
		{"skill file", "skills/pr-review/SKILL.md", ""},
		{"skill aux", "skills/pr-review/scripts/run.sh", ""},
		{"file", "files/spec.pdf", ""},
		{"nested file", "files/docs/api/v1.md", ""},
		{"agents not top", "files/AGENTS.md", ""},
		{"agents lowercase", "agents.md", ReasonBadTopLevel},
		{"agents as dir", "AGENTS.md/x", ReasonBadTopLevel},
		{"unknown top", "notes/a.md", ReasonBadTopLevel},
		{"bare files", "files", ReasonBadTopLevel},
		{"skill without file", "skills/pr-review", ReasonBadTopLevel},
		{"file directly in skills", "skills/readme.md", ReasonBadTopLevel},
		{"skill uppercase", "skills/PR/SKILL.md", ReasonBadCharacter},
		{"skill accent", "skills/revisión/SKILL.md", ReasonBadCharacter},
		{"skill 64 bytes", "skills/" + rep(64) + "/SKILL.md", ""},
		{"skill 65 bytes", "skills/" + rep(65) + "/SKILL.md", ReasonBadCharacter},
		{"mcp reserved", "mcp/server.json", ReasonNotSupportedYet},
		// Spaces and Unicode letters are accepted (Lead decision 2026-10-21).
		{"spaces", "files/Meeting notes 2026.md", ""},
		{"accents", "files/especificación técnica.md", ""},
		{"cjk", "files/仕様書.md", ""},
		{"cyrillic", "files/документ.txt", ""},
		{"arabic digits", "files/ملف١٢.txt", ""},
		{"devanagari marks", "files/ह\u093Fन\u094Dद\u0940.txt", ""},
		{"punctuation set", "files/a_b-c(d)+e,f@g.txt", ""},
		{"dotless i", "files/ılık.md", ""},
		{"eszett", "files/Straße.md", ""},
		// NFC is required, never rewritten.
		{"decomposed e acute", "files/especificacio\u0301n.md", ReasonNotNFC},
		{"composed e acute", "files/especificación.md", ""},
		// Format and ignorable characters.
		{"rtl override", "files/invoice\u202Etxt.exe", ReasonBadCharacter},
		{"zero width joiner", "files/a\u200Db.md", ReasonBadCharacter},
		{"zero width space", "files/a\u200Bb.md", ReasonBadCharacter},
		{"bom", "files/\uFEFFa.md", ReasonBadCharacter},
		{"soft hyphen", "files/a\u00ADb.md", ReasonBadCharacter},
		{"nbsp", "files/a\u00A0b.md", ReasonBadCharacter},
		{"ideographic space", "files/a\u3000b.md", ReasonBadCharacter},
		{"tab", "files/a\tb.md", ReasonBadCharacter},
		{"newline", "files/a\nb.md", ReasonBadCharacter},
		{"nul", "files/a\x00b.md", ReasonBadCharacter},
		{"variation selector 16", "files/a\uFE0Fb.md", ReasonBadCharacter},
		{"variation selector supplement", "files/a\U000E0100b.md", ReasonBadCharacter},
		{"mongolian fvs", "files/a\u180Bb.md", ReasonBadCharacter},
		{"combining grapheme joiner", "files/a\u034Fb.md", ReasonBadCharacter},
		{"hangul filler", "files/a\u3164b.md", ReasonBadCharacter},
		{"hangul choseong filler", "files/\u115Fa.md", ReasonBadCharacter},
		{"hangul jungseong filler", "files/\u1160a.md", ReasonBadCharacter},
		{"halfwidth hangul filler", "files/a\uFFA0b.md", ReasonBadCharacter},
		{"khmer inherent vowel", "files/a\u17B4b.md", ReasonBadCharacter},
		{"emoji", "files/rocket\U0001F680.md", ReasonBadCharacter},
		{"symbol", "files/a\u00A9b.md", ReasonBadCharacter},
		{"private use", "files/a\uE000b.md", ReasonBadCharacter},
		{"unassigned", "files/a\U000E0080b.md", ReasonBadCharacter},
		{"invalid utf8", "files/a\xffb.md", ReasonBadCharacter},
		// Shell and Windows hostile ASCII.
		{"backslash", "files/a\\b.md", ReasonBadCharacter},
		{"colon", "files/a:b.md", ReasonBadCharacter},
		{"star", "files/a*.md", ReasonBadCharacter},
		{"question", "files/a?.md", ReasonBadCharacter},
		{"quote", "files/a\"b.md", ReasonBadCharacter},
		{"lt", "files/a<b.md", ReasonBadCharacter},
		{"gt", "files/a>b.md", ReasonBadCharacter},
		{"pipe", "files/a|b.md", ReasonBadCharacter},
		{"apostrophe", "files/don't.md", ReasonBadCharacter},
		{"backtick", "files/a`b.md", ReasonBadCharacter},
		{"dollar", "files/$HOME.md", ReasonBadCharacter},
		// Dotfiles, leading and trailing space or dot.
		{"dotfile", "files/.env", ReasonDotfile},
		{"dot env example", "files/.env.example", ReasonDotfile},
		{"git dir", "files/.git/config", ReasonDotfile},
		{"pi dir", "skills/x/.pi/a.md", ReasonDotfile},
		{"dot", "files/./a.md", ReasonDotfile},
		{"dotdot", "files/../AGENTS.md", ReasonDotfile},
		{"leading space", "files/ a.md", ReasonBadCharacter},
		{"trailing space", "files/a.md ", ReasonBadCharacter},
		{"trailing dot", "files/a.", ReasonBadCharacter},
		{"trailing dot dir", "files/dir./a.md", ReasonBadCharacter},
		{"inner dots ok", "files/v1.2.3.tar.md", ""},
		// Empty components and absolute paths.
		{"empty", "", ReasonBadCharacter},
		{"absolute", "/files/a.md", ReasonBadCharacter},
		{"double slash", "files//a.md", ReasonBadCharacter},
		{"trailing slash", "files/a/", ReasonBadCharacter},
		// Windows reserved basenames, any extension, any case.
		{"con", "files/CON", ReasonReservedName},
		{"con lower ext", "files/con.txt", ReasonReservedName},
		{"nul double ext", "files/Nul.tar.md", ReasonReservedName},
		{"com1", "files/com1.md", ReasonReservedName},
		{"lpt9 dir", "files/LPT9/a.md", ReasonReservedName},
		{"aux", "files/aux", ReasonReservedName},
		{"prn", "files/prn.md", ReasonReservedName},
		{"com0 ok", "files/com0.md", ""},
		{"console ok", "files/console.md", ""},
		{"con- ok", "files/con-1.md", ""},
		// Exact maxima (512 bytes, 8 components, 128 bytes per component).
		{"512 bytes", pathOf(t, 512, 6), ""},
		{"513 bytes", pathOf(t, 513, 6), ReasonTooLong},
		{"8 components", "files/a/b/c/d/e/f/g.md", ""},
		{"9 components", "files/a/b/c/d/e/f/g/h.md", ReasonTooDeep},
		{"component 128", "files/" + rep(128), ""},
		{"component 129", "files/" + rep(129), ReasonTooLong},
		{"component 128 multibyte", "files/" + strings.Repeat("é", 64), ""},
		{"component 129 multibyte", "files/" + strings.Repeat("é", 64) + "a", ReasonTooLong},
	} {
		err := ValidateStoredPath(tc.path)
		if got := reasonOf(err); got != tc.want {
			t.Errorf("%s: %q: reason %q, want %q (err %v)", tc.name, tc.path, got, tc.want, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: error does not match ErrInvalidPath", tc.name)
		}
	}
}

func TestValidContextName(t *testing.T) {
	for name, want := range map[string]bool{
		"acme": true, "a": true, "0": true, "pr-review": true, rep(63): true,
		rep(64): false, "": false, "-a": false, "Acme": false, "a_b": false, "a.b": false, "a b": false, "ñ": false,
		// Windows reserved: the name becomes files/<name>/ in the manifest.
		"con": false, "nul": false, "aux": false, "prn": false, "com1": false, "lpt9": false,
		"com0": true, "console": true, "con-1": true,
	} {
		if got := ValidContextName(name); got != want {
			t.Errorf("ValidContextName(%q) = %v", name, got)
		}
	}
}

// Uniqueness is case-insensitive with Unicode full case folding of the NFC
// form: ß folds to ss, final sigma to sigma; the Turkish dotless ı has no
// default folding (only the Turkic one), so ı and I do not collide, and İ
// folds to i + U+0307, so it collides with neither i nor I.
func TestValidateTreeCaseCollisions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		want  []PathError
	}{
		{"distinct", []string{"AGENTS.md", "files/a.md", "files/b.md", "skills/x/SKILL.md"}, nil},
		{"ascii case", []string{"files/Readme.md", "files/README.md"}, []PathError{{"files/README.md", ReasonCaseCollision}}},
		{"eszett", []string{"files/Straße.md", "files/STRASSE.md"}, []PathError{{"files/STRASSE.md", ReasonCaseCollision}}},
		{"capital eszett", []string{"files/ẞ.md", "files/ss.md"}, []PathError{{"files/ss.md", ReasonCaseCollision}}},
		{"sigma", []string{"files/ΟΔΟΣ.md", "files/οδος.md"}, []PathError{{"files/οδος.md", ReasonCaseCollision}}},
		{"final sigma", []string{"files/οδος.md", "files/οδοσ.md"}, []PathError{{"files/οδοσ.md", ReasonCaseCollision}}},
		{"accented case", []string{"files/Ésta.md", "files/ésta.md"}, []PathError{{"files/ésta.md", ReasonCaseCollision}}},
		{"dotless i is distinct", []string{"files/ı.md", "files/I.md", "files/i.md"}, []PathError{{"files/i.md", ReasonCaseCollision}}},
		{"dotted capital I is distinct", []string{"files/İ.md", "files/i.md"}, nil},
		{"folder case", []string{"files/Docs/a.md", "files/docs/b.md"}, nil},
		{"file vs folder", []string{"files/docs", "files/Docs/a.md"}, []PathError{{"files/docs", ReasonCaseCollision}}},
		{"invalid reported too", []string{"files/.env", "files/a.md", "files/A.md"},
			[]PathError{{"files/.env", ReasonDotfile}, {"files/A.md", ReasonCaseCollision}}},
	} {
		got := ValidateTree(tc.paths)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFoldKeyIsStable(t *testing.T) {
	// Folding İ yields a combining sequence; the key is re-normalised so a
	// second fold is a no-op.
	for _, p := range []string{"files/İstanbul.md", "files/Straße.md", "files/ΟΔΟΣ.md"} {
		k := FoldKey(p)
		if FoldKey(k) != k {
			t.Errorf("FoldKey(%q) not idempotent: %q vs %q", p, k, FoldKey(k))
		}
	}
}

// Names the API could never reach by name are refused: UUID-shaped names
// (always read as ids) and "blobs" (the upload routes).
func TestContextNameReachable(t *testing.T) {
	for _, n := range []string{"123e4567-e89b-12d3-a456-426614174000", "blobs"} {
		if ValidContextName(n) {
			t.Errorf("%q accepted", n)
		}
	}
	for _, n := range []string{"blobs-2", "my-blobs", "123e4567-e89b-12d3-a456-42661417400", "123e4567e89b12d3a456426614174000"} {
		if !ValidContextName(n) {
			t.Errorf("%q refused", n)
		}
	}
}

// File names never carry a quote, a backslash or a percent sign (they would
// need escaping in Content-Disposition).
func TestStoredPathRefusesHeaderCharacters(t *testing.T) {
	for _, p := range []string{`files/a"b.txt`, `files/a\b.txt`, "files/a%b.txt"} {
		if ValidateStoredPath(p) == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

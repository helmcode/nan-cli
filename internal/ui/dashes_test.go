package ui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// No em or en dashes in anything this program prints.
//
// They are a typographic nicety that a terminal is a bad place for: the
// character is not in every font a person might have set, it is not on
// anybody's keyboard when they want to grep the output for a line, and it
// survives a copy-paste into a shell far less well than a hyphen. This
// program had seventy-odd of them.
//
// The check is over string LITERALS only, parsed rather than grepped, so
// prose in the comments is left alone — it is not what anybody sees.
// Built rather than written out, so this file does not report itself.
var dashes = string([]rune{0x2014, 0x2013})

func TestNothingPrintedHasAnEmDash(t *testing.T) {
	root := filepath.Join("..", "..")

	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "assets" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			// A file that does not parse is the compiler's problem, not this
			// test's.
			return nil //nolint:nilerr // not this test's failure to report
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.ContainsAny(lit.Value, dashes) {
				found = append(found, fmt.Sprintf("%s:%d  %s",
					filepath.ToSlash(path), fset.Position(lit.Pos()).Line, lit.Value))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	if len(found) > 0 {
		t.Errorf("%d string literals carry an em or en dash; use a hyphen:\n  %s",
			len(found), strings.Join(found, "\n  "))
	}
}

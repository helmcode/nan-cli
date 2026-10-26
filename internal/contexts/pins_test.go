package contexts

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"unicode"
)

// The path rules read Unicode tables from two places: the standard library
// (unicode.IsLetter, the categories) and golang.org/x/text (NFC, case
// folding). cloud-api accepts a name only when its own tables say so, so
// this CLI has to read the same tables or `nan context pull` could refuse a
// name the server stored (devops AGENT-CONTEXT.md §1.5).
//
// These are the pins both repositories must share. x/text is v0.39.0, the
// first release without GO-2026-5970 (an infinite loop in unicode/norm on
// invalid UTF-8, reached by these rules); cloud-api has to move to it too.
// When either side moves, scripts/check-cloud-api-pins.sh fails in CI until
// this record and go.mod match cloud-api again.
const (
	cloudAPIXText   = "v0.39.0"
	cloudAPIUnicode = "15.0.0"
)

var reXText = regexp.MustCompile(`(?m)^\s*golang\.org/x/text\s+(v\S+)`)

func TestXTextPinnedToCloudAPI(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := reXText.FindSubmatch(b)
	if m == nil {
		t.Fatal("go.mod does not require golang.org/x/text")
	}
	if got := string(m[1]); got != cloudAPIXText {
		t.Errorf("go.mod pins golang.org/x/text %s, cloud-api builds with %s: the path rules would read other Unicode tables", got, cloudAPIXText)
	}
}

func TestUnicodeTablesMatchCloudAPI(t *testing.T) {
	if unicode.Version != cloudAPIUnicode {
		t.Errorf("this toolchain's unicode tables are %s, cloud-api's are %s", unicode.Version, cloudAPIUnicode)
	}
}

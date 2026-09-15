package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// `--token` and `--link` both take a secret, and a secret spelled out on a
// command line is in ~/.bash_history and in `ps` for as long as the command
// runs. "-" is the way out for a script or a CI step, so it has to actually
// read stdin - and has to say so rather than saving an empty token when
// nothing arrives.
func TestFlagValueReadsStdinForADash(t *testing.T) {
	for _, c := range []struct {
		name    string
		flag    string
		stdin   string
		want    string
		wantErr bool
	}{
		{name: "a value stays a value", flag: "a-token", want: "a-token"},
		{name: "trimmed", flag: "  a-token\n", want: "a-token"},
		{name: "a dash reads stdin", flag: "-", stdin: "from-stdin\n", want: "from-stdin"},
		{name: "an empty stdin is not a token", flag: "-", stdin: "   \n", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.flag == "-" {
				withStdin(t, c.stdin)
			}
			got, err := flagValue(c.flag)
			if c.wantErr {
				if err == nil {
					t.Fatalf("took %q as a credential", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("flagValue(%q) = %q, want %q", c.flag, got, c.want)
			}
		})
	}
}

func withStdin(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = original
		f.Close()
	})
}

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ── writing a file that carries a key ────────────────────────────────────────

// writeConfigFile writes a tool's config the way a file holding an API key has
// to be written, which is not what os.WriteFile does on its own.
//
// Its mode argument applies only where the file is CREATED, and the usual case
// here is the opposite one: ~/.codex/config.toml and the rest already exist,
// written by the tool itself, commonly 0644. The key went into them and the
// 0o600 in the call did nothing whatsoever - the file kept the mode it had,
// readable by every account on the machine.
//
// And a plain write truncates first: interrupted halfway it leaves a member's
// config - every provider in it, not only ours - cut in two. So the bytes go
// to a temp file in the same directory, which is created 0600, and that file
// is renamed over the destination. The rename happens once or not at all, and
// it carries the temp file's mode with it, which is where the 0600 sticks.
func writeConfigFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Managed dotfiles are symlinks into a repo. Renaming over the link would
	// replace it with a regular file and quietly detach the config from the
	// thing that manages it, so the write follows the link first.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".nan-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // does nothing once the rename has taken it

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// On Windows the mode bits are not the mechanism - the file inherits the
	// ACL of the profile directory it sits in - so a filesystem that will not
	// take the chmod is no reason to refuse to write the config at all.
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		// Windows will not replace a file another process is holding open,
		// where the plain write this replaced would have gone through. The
		// mode bits are not the mechanism there in any case, so falling back
		// to that write leaves nothing worse than what came before it. On
		// every other platform a rename that failed is a failure.
		if runtime.GOOS == "windows" {
			return os.WriteFile(path, data, 0o600) //nolint:gosec // G703: the path is a tool config this program chose
		}
		return err
	}
	return nil
}

// readJSONConfig reads a tool's config, and refuses to guess at one it cannot
// parse.
//
// These files are shared: opencode.json and Pi's models.json hold every
// provider a member has, other vendors' API keys included. The readers here
// used to drop the unmarshal error on the floor, which meant a file this CLI
// could not parse - a stray comma, a half-finished hand edit, a schema the
// tool has since moved on to - was read as nothing and then written again from
// scratch, taking every other provider in it along with it. Not being able to
// read the file is precisely the case where it must not be touched.
func readJSONConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a config file at a path this program chose
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s does not parse as JSON, so it was left exactly as it is: %w",
			filepath.Base(path), err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	catalog "github.com/nxssie/nan-cli/internal/models"
)

// Swapped in tests, so nothing here spawns a process or goes near the
// member's own Hermes. HERMES_HOME is passed on every call rather than
// inherited: the writer configures the install it detected, not whichever one
// the environment happens to point at.
var runHermesConfig = func(home string, args ...string) error {
	cmd := exec.Command("hermes", append([]string{"config"}, args...)...) //nolint:gosec // G204: the binary is hermes, found on PATH; the arguments are ours and carry no secret
	cmd.Env = append(os.Environ(), "HERMES_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return hermesConfigError(args, string(out))
	}
	return nil
}

// The verb and the key it was called with, and never the value.
//
// This used to join every argument into the message, and the message is
// rendered in the panel: one `set` that Hermes refused printed the member's
// API key onto their screen, into their scrollback, and into the issue they
// then pasted all of it into. Nothing past the key belongs in an error.
func hermesConfigError(args []string, out string) error {
	said := args
	if len(said) > 2 {
		said = said[:2]
	}
	return fmt.Errorf("hermes config %s: %s", strings.Join(said, " "), strings.TrimSpace(out))
}

// Reading a value back, for the one thing that has to be confirmed rather than
// assumed. Swapped in tests alongside the writer above.
var readHermesConfig = func(home string, args ...string) (string, error) {
	cmd := exec.Command("hermes", append([]string{"config"}, args...)...) //nolint:gosec // G204: the binary is hermes, found on PATH; the arguments are ours and carry no secret
	cmd.Env = append(os.Environ(), "HERMES_HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", hermesConfigError(args, string(exit.Stderr))
		}
		return "", hermesConfigError(args, err.Error())
	}
	return strings.TrimSpace(string(out)), nil
}

// The resolution Hermes itself uses: HERMES_HOME, then the platform default.
func hermesHome() string {
	if home := strings.TrimSpace(os.Getenv("HERMES_HOME")); home != "" {
		return home
	}
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Join(local, "hermes")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Local", "hermes")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hermes")
}

func hermesConfigPath(home string) string {
	return filepath.Join(home, "config.yaml")
}

// Hermes keeps its secrets in a .env beside its config - it has a `hermes
// config env-path` for exactly this - and expands ${VAR} in any config value
// against it when it loads. So the config gets the placeholder and the .env
// gets the key.
const (
	hermesEnvFile = ".env"
	hermesKeyVar  = "NAN_API_KEY"
	hermesKeyRef  = "${" + hermesKeyVar + "}"
	hermesKeyNote = "# Written by nan-cli. config.yaml points at it as " + hermesKeyRef + "."
)

func hermesEnvPath(home string) string {
	return filepath.Join(home, hermesEnvFile)
}

func writeHermesConfig(home, apiKey string) error {
	// The key goes into Hermes' .env, and the config points at it. Not because
	// the config is a worse place to keep it - same machine, same member, same
	// 0600 - but because of the way it would have to get there.
	//
	// Every value here is written by running `hermes config set <key> <value>`,
	// and an argument is not private: on Linux /proc/<pid>/cmdline is readable
	// by other accounts on the box, and `ps` hands it to anything running as
	// the member. A key passed that way is exposed for as long as the process
	// lives, to readers this CLI never meant to hand it to, and there is no
	// taking it back afterwards. The placeholder is not a secret and can go
	// through argv; the key is written straight to disk and appears in no
	// argument list at all.
	if err := writeHermesEnvKey(hermesEnvPath(home), apiKey); err != nil {
		return err
	}
	settings := [][2]string{
		{"model.provider", "custom"},
		{"model.base_url", "https://api.nan.builders/v1"},
		{"model.api_key", hermesKeyRef},
		{"model.default", catalog.Coding},
	}
	for _, s := range settings {
		if err := runHermesConfig(home, "set", s[0], s[1]); err != nil {
			return err
		}
	}
	return confirmHermesResolvesTheKey(home, apiKey)
}

// Everything above rests on Hermes expanding ${VAR} in a config value against
// its own .env, which it does today and which nothing here controls. Asking it
// what it ended up with is the only way to know it happened: a build that
// stopped expanding would leave the literal ${NAN_API_KEY} in place as the
// key, and what a member would see is a 401 from the cluster naming nothing in
// particular.
//
// The answer is the key itself, so it is compared and dropped on the spot. It
// does not go into a message, a log or an error.
func confirmHermesResolvesTheKey(home, apiKey string) error {
	resolved, err := readHermesConfig(home, "get", "model.api_key")
	if err != nil {
		// No answer is not a wrong answer. An older Hermes without this
		// subcommand, or one that could not be run twice, says nothing either
		// way about what it will do with the config - and failing the whole
		// step over a question we could not ask would be its own bug.
		return nil //nolint:nilerr // a file that cannot be read has nothing of ours in it
	}
	if resolved != apiKey {
		//nolint:staticcheck // ST1005: Hermes is a name, and starts the sentence
		return fmt.Errorf("Hermes did not resolve %s from its .env, so it has no usable key - "+
			"nan.builders/docs/hermes has the manual steps", hermesKeyRef)
	}
	return nil
}

// One variable set in the .env, and the rest of the file - a long commented
// document with the member's other providers in it - left as it was.
func writeHermesEnvKey(envPath, apiKey string) error {
	var lines []string
	if data, err := os.ReadFile(envPath); err == nil { //nolint:gosec // G304: a config file at a path this program chose
		if body := strings.TrimRight(string(data), "\n"); body != "" {
			lines = strings.Split(body, "\n")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	entry := hermesKeyVar + "=" + apiKey
	replaced := false
	for i, line := range lines {
		if isHermesKeyLine(line) {
			lines[i] = entry
			replaced = true
		}
	}
	if !replaced {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, hermesKeyNote, entry)
	}
	return writeConfigFile(envPath, []byte(strings.Join(lines, "\n")+"\n"))
}

// The assignment, and not a commented-out example of the same name: blanking
// one of those would leave the real one further down the file still winning.
func isHermesKeyLine(line string) bool {
	name, _, ok := strings.Cut(line, "=")
	return ok && strings.TrimSpace(name) == hermesKeyVar
}

// The same four keys and nothing else: a member's channels, skills and
// persona live in this file too.
func removeHermesConfig(home string) error {
	// Four processes, so only where there is something to unset. The .env
	// below is checked either way: it is written before the first of these
	// calls, so a run that failed part-way through leaves the key there and
	// nothing in config.yaml to say so.
	if cfg, err := os.ReadFile(hermesConfigPath(home)); err == nil && strings.Contains(string(cfg), "api.nan.builders") {
		for _, key := range []string{"model.api_key", "model.base_url", "model.default", "model.provider"} {
			if err := runHermesConfig(home, "unset", key); err != nil {
				return err
			}
		}
	}
	// And the key the config was pointing at. Unsetting model.api_key drops
	// the reference, which leaves the secret itself sitting in the .env.
	return removeHermesEnvKey(hermesEnvPath(home))
}

func removeHermesEnvKey(envPath string) error {
	data, err := os.ReadFile(envPath) //nolint:gosec // G304: a config file at a path this program chose
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var kept []string
	dropped := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if isHermesKeyLine(line) || line == hermesKeyNote {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	if !dropped {
		return nil
	}
	// A .env that held nothing but our two lines was ours to begin with.
	if len(kept) == 0 {
		return os.Remove(envPath)
	}
	return writeConfigFile(envPath, []byte(strings.Join(kept, "\n")+"\n"))
}

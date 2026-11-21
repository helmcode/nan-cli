#!/usr/bin/env bash
# Tests for the PATH handling in scripts/install.sh. No network and no real
# HOME: each case sources the installer in a clean bash, under a throwaway HOME,
# with the login shell and the OS faked, and calls one function.
#
#   bash scripts/install_test.sh
set -euo pipefail

installer="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install.sh"
tmp="$(mktemp -d)"
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
failures=0

fail() { printf 'FAIL: %s\n' "$1" >&2; failures=$((failures + 1)); }

expect_eq() { [ "$1" = "$2" ] || fail "$3: got '$1', want '$2'"; }

expect_has() {
  case "$1" in *"$2"*) ;; *) fail "$3: output does not contain '$2'"$'\n'"$1" ;; esac
}

# run <home> <login shell> <Linux|Darwin> <code>: source the installer and eval
# <code> with nothing inherited but a minimal PATH.
run() {
  mkdir -p "$1"
  env -i HOME="$1" SHELL="$2" FAKE_OS="$3" PATH=/usr/bin:/bin \
    bash -c 'source "$1"; uname() { echo "$FAKE_OS"; }; eval "$2"' _ "$installer" "$4" 2>&1
}

# --- shell_profile: the file a new terminal of each shell reads -------------
expect_eq "$(run "$tmp/p1" /bin/zsh Linux shell_profile)" "$tmp/p1/.zshrc" "zsh"
expect_eq "$(run "$tmp/p2" /bin/zsh Darwin 'ZDOTDIR=$HOME/zdot shell_profile')" "$tmp/p2/zdot/.zshrc" "zsh with ZDOTDIR"
mkdir -p "$tmp/p3"; touch "$tmp/p3/.bash_profile"
expect_eq "$(run "$tmp/p3" /bin/bash Linux shell_profile)" "$tmp/p3/.bashrc" "bash on Linux, even with a .bash_profile"
mkdir -p "$tmp/p4"; touch "$tmp/p4/.bashrc"
expect_eq "$(run "$tmp/p4" /bin/bash Darwin shell_profile)" "$tmp/p4/.bash_profile" "bash on macOS, even with a .bashrc"
expect_eq "$(run "$tmp/p5" /usr/bin/fish Linux shell_profile)" "$tmp/p5/.config/fish/config.fish" "fish"
expect_eq "$(run "$tmp/p6" /usr/bin/fish Linux 'XDG_CONFIG_HOME=$HOME/xdg shell_profile')" "$tmp/p6/xdg/fish/config.fish" "fish with XDG_CONFIG_HOME"
expect_eq "$(run "$tmp/p7" /bin/tcsh Linux shell_profile)" "$tmp/p7/.profile" "any other shell"

# --- check_path on the default directory ------------------------------------
# The exact lines the installer writes, unexpanded on purpose.
sh_line='export PATH="$HOME/.local/bin":"$PATH"'
fish_line='fish_add_path "$HOME/.local/bin"'

h="$tmp/zsh"; mkdir -p "$h"
# A comment that merely mentions the directory must not count as the line:
# the old `grep -qF "$dir"` stopped here and wrote nothing.
printf '# tools end up in %s/.local/bin\n' "$h" > "$h/.zshrc"
out="$(run "$h" /bin/zsh Darwin 'check_path "$HOME/.local/bin"')" || fail "zsh: check_path failed"
expect_eq "$(grep -cxF "$sh_line" "$h/.zshrc")" 1 "zsh: the export is appended despite a comment naming the dir"
expect_has "$out" "open a new terminal" "zsh: says what to do next"
out="$(run "$h" /bin/zsh Darwin 'check_path "$HOME/.local/bin"')" || fail "zsh rerun: check_path failed"
expect_eq "$(grep -cxF "$sh_line" "$h/.zshrc")" 1 "zsh rerun: still exactly one export"
expect_has "$out" "already adds" "zsh rerun: says the profile is fine"

# A home path full of shell metacharacters. The written line has to work, not
# only be there: a fresh bash that sources the profile finds nan in it.
h="$tmp/bash \$USER \`x\` 'q\""
run "$h" /bin/bash Linux 'check_path "$HOME/.local/bin"' >/dev/null || fail "bash: check_path failed"
expect_eq "$(grep -cxF "$sh_line" "$h/.bashrc")" 1 "bash: the export line"
mkdir -p "$h/.local/bin"; printf '#!/bin/sh\necho fake-nan\n' > "$h/.local/bin/nan"; chmod +x "$h/.local/bin/nan"
expect_eq "$(env -i HOME="$h" PATH=/usr/bin:/bin bash -c 'source "$HOME/.bashrc"; command -v nan')" "$h/.local/bin/nan" "bash: the written line puts nan on PATH"

h="$tmp/fish"
run "$h" /usr/bin/fish Linux 'check_path "$HOME/.local/bin"' >/dev/null || fail "fish: check_path failed"
expect_eq "$(grep -cxF "$fish_line" "$h/.config/fish/config.fish")" 1 "fish: fish syntax, not an export"

h="$tmp/inpath"
run "$h" /bin/zsh Linux 'PATH="$HOME/.local/bin:$PATH" check_path "$HOME/.local/bin"' >/dev/null || fail "in PATH: check_path failed"
[ ! -e "$h/.zshrc" ] || fail "in PATH: a profile was written anyway"

# A custom dir: no file is touched, and the printed line, run as printed, puts
# the literal directory first - metacharacters and all.
h="$tmp/custom"
custom="$h/odd \$HOME \`x\` it's"
out="$(run "$h" /bin/zsh Linux "check_path $(printf '%q' "$custom")")" || fail "custom dir: check_path failed"
[ -z "$(ls -A "$h")" ] || fail "custom dir: a file was written in HOME: $(ls -A "$h")"
line="$(printf '%s\n' "$out" | sed -n 's/^    //p')"
expect_eq "$(env -i PATH=/usr/bin:/bin bash -c "$line"'; printf %s "${PATH%%:*}"')" "$custom" "custom dir: the printed line is literal"

h="$tmp/customfish"
out="$(run "$h" /usr/bin/fish Linux 'check_path "$HOME/elsewhere"')" || fail "custom fish: check_path failed"
[ -z "$(ls -A "$h")" ] || fail "custom fish: a file was written in HOME"
expect_has "$out" "    fish_add_path '$h/elsewhere'" "custom fish: prints fish syntax"

# macOS bash: the profile a Terminal.app login shell reads.
h="$tmp/macbash"
run "$h" /bin/bash Darwin 'check_path "$HOME/.local/bin"' >/dev/null || fail "macOS bash: check_path failed"
expect_eq "$(grep -cxF "$sh_line" "$h/.bash_profile")" 1 "macOS bash: written to .bash_profile"
[ ! -e "$h/.bashrc" ] || fail "macOS bash: .bashrc written"

# A shell that does not read ~/.profile: the file is still written, but no
# success is claimed and the line is handed over.
h="$tmp/tcsh"
out="$(run "$h" /bin/tcsh Linux 'check_path "$HOME/.local/bin"')" || fail "tcsh: check_path failed"
expect_eq "$(grep -cxF "$sh_line" "$h/.profile")" 1 "tcsh: ~/.profile is still written"
expect_has "$out" "if your shell doesn't read ~/.profile, add this line yourself: $sh_line" "tcsh: hands over the line"
case "$out" in *"added "*) fail "tcsh: claimed success" ;; esac

# Root with no INSTALL_DIR: machine-wide default. Fake id on PATH.
fakebin="$tmp/fakebin"; mkdir -p "$fakebin"
printf '#!/bin/sh\n[ "$1" = -u ] && echo "${FAKE_UID:-1000}" || echo tester\n' > "$fakebin/id"
printf '#!/bin/sh\nexit 1\n' > "$fakebin/sudo"
chmod +x "$fakebin/id" "$fakebin/sudo"
dir_for() { env -i HOME="$tmp/roothome" FAKE_UID="$1" PATH="$fakebin:/usr/bin:/bin" $2 bash -c 'source "$1"; printf %s "$INSTALL_DIR"' _ "$installer"; }
expect_eq "$(dir_for 0 '')" /usr/local/bin "root: defaults to /usr/local/bin"
expect_eq "$(dir_for 0 INSTALL_DIR=/opt/x)" /opt/x "root: an explicit INSTALL_DIR wins"
expect_eq "$(dir_for 1000 '')" "$tmp/roothome/.local/bin" "user: defaults to ~/.local/bin"

# --- warn_if_shadowed --------------------------------------------------------
h="$tmp/shadow"; mkdir -p "$h/old" "$h/.local/bin"
expect_eq "$(run "$h" /bin/zsh Linux 'warn_if_shadowed "" "$HOME/.local/bin"')" "" "shadow: nothing found, nothing said"
expect_eq "$(run "$h" /bin/zsh Linux 'warn_if_shadowed "$HOME/.local/bin/nan" "$HOME/.local/bin"')" "" "shadow: the same dir is not another nan"
ln -s "$h/.local/bin" "$h/link"
expect_eq "$(run "$h" /bin/zsh Linux 'warn_if_shadowed "$HOME/link/nan" "$HOME/.local/bin"')" "" "shadow: a symlink to the same dir is not another nan"
out="$(run "$h" /bin/zsh Linux 'warn_if_shadowed "$HOME/old/nan" "$HOME/.local/bin"')"
expect_has "$out" "$h/old/nan" "shadow: names the old binary"
expect_has "$out" "remove it: rm '$h/old/nan'" "shadow: plain rm for a writable dir"

# Permission cases mean nothing as root, which can write anywhere.
if [ "$(id -u)" != 0 ]; then
  h="$tmp/ro"; mkdir -p "$h/old"; chmod 555 "$h/old"
  out="$(run "$h" /bin/zsh Linux 'warn_if_shadowed "$HOME/old/nan" "$HOME/.local/bin"')"
  expect_has "$out" "remove it: sudo rm '$h/old/nan'" "shadow: sudo rm for a root-owned dir"

  # A root-owned ~/.local gets a chown hint before the sudo fallback.
  h="$tmp/rootowned"; mkdir -p "$h/.local"; chmod 555 "$h/.local"
  out="$(env -i HOME="$h" SHELL=/bin/zsh PATH="$fakebin:/usr/bin:/bin" bash -c 'source "$1"; install_bin /bin/ls "$HOME/.local/bin"' _ "$installer" 2>&1)" || true
  expect_has "$out" "sudo chown -R \"\$(id -un)\" '$h/.local'" "unwritable ~/.local: chown hint"

  h="$tmp/locked"; mkdir -p "$h"; touch "$h/.zshrc"; chmod 444 "$h/.zshrc"
  out="$(run "$h" /bin/zsh Linux 'check_path "$HOME/.local/bin"')" || fail "unwritable profile: the installer aborted"
  expect_has "$out" "add this line to it yourself" "unwritable profile: says so"
  expect_has "$out" "$sh_line" "unwritable profile: prints the line"
fi

if [ "$failures" -ne 0 ]; then
  printf '%d failure(s)\n' "$failures" >&2
  exit 1
fi
echo "install.sh PATH logic: all cases pass"

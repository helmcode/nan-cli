#!/usr/bin/env bash
#
# Run the CLI against PRODUCTION as somebody installing it for the first
# time: no session, no tool configs.
#
#   scripts/fresh.sh              first run from scratch
#   scripts/fresh.sh --keep       reuse the last run's state (no re-login)
#   scripts/fresh.sh --show       print what Setup wrote, then exit
#   scripts/fresh.sh <args...>    pass a subcommand instead of the dashboard
#
# Why a throwaway HOME rather than just deleting ~/.config/nan: Setup writes
# into ~/.factory/settings.json, ~/.config/opencode/opencode.json,
# ~/.pi/agent/models.json, ~/.codex/config.toml and ~/.hermes - real files,
# holding real credentials for other services. Here it writes into a
# directory you can delete.
#
# Tool DETECTION still sees the real machine: it checks the PATH as well as
# the config directory, so installed tools are found and Setup offers them
# exactly as it would to a new member. Only the writing is redirected.
#
# This talks to the real nan.builders. Sign-in spends a real email, and runs,
# automations, approvals and secrets act on the real account. Only the tool
# configs are sandboxed.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
home="${FRESH_HOME:-${TMPDIR:-/tmp}/nan-fresh}"

keep=0
show=0
case "${1:-}" in
  --keep) keep=1; shift ;;
  --show) show=1; shift ;;
esac

if [ "$show" = 1 ]; then
  echo
  echo "  what Setup wrote under ${home}:"
  echo
  found=0
  for f in "$home/.factory/settings.json" \
           "$home/.config/opencode/opencode.json" \
           "$home/.pi/agent/models.json" \
           "$home/.codex/config.toml" \
           "$home"/.codex/nan-*.config.toml \
           "$home/.hermes/config.yaml" \
           "$home/.hermes/.env" \
           "$home/.config/nan/session.json"; do
    [ -f "$f" ] || continue
    found=1
    echo "  ── ${f#"$home"/}"
    # Every one of these holds a key; show the shape, never the key.
    sed -E \
      -e 's/(("(token|apiKey|api_key|platformToken)"|apiKey)[[:space:]]*:[[:space:]]*"?)[^",]*/\1…/g' \
      -e 's/^([A-Z_]*KEY[[:space:]]*=[[:space:]]*).*/\1…/' \
      -e 's/((api_key|experimental_bearer_token)[[:space:]]*=[[:space:]]*"?)[^"]*/\1…/' \
      "$f"
    echo
  done
  [ "$found" = 1 ] || echo "  (nothing yet - run it without --show first)"
  exit 0
fi

if [ "$keep" = 0 ]; then
  rm -rf "$home"
fi
mkdir -p "$home/.local/bin"

# Built from the working tree, so this exercises whatever was just changed,
# and installed into the throwaway HOME the way a member would have it.
echo "  building from $(git -C "$repo" rev-parse --short HEAD 2>/dev/null || echo 'working tree')…"
CGO_ENABLED=0 go build -C "$repo" -o "$home/.local/bin/nan" ./cmd/nan

echo
if [ "$keep" = 1 ]; then
  echo "  reusing ${home} (pass no --keep to start over)"
else
  echo "  fresh HOME: ${home}"
fi
echo "  platform: PRODUCTION - sign-in spends a real email, writes are real"
echo "  tool configs are sandboxed here; your own ~/.factory, ~/.codex and the rest are untouched"
echo

# Production means these unset, and a shell that exported one of them for a
# local stand-in would otherwise carry it in here.
unset NAN_API_URL NAN_APP_URL NAN_INFERENCE_URL NAN_TOKEN
# And the ones that point a tool's files somewhere other than HOME: left set,
# they would aim Setup at the real files this sandbox exists to spare.
unset XDG_CONFIG_HOME XDG_DATA_HOME CODEX_HOME HERMES_HOME
HOME="$home" PATH="$home/.local/bin:$PATH" exec nan "$@"

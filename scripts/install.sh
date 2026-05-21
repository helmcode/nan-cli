#!/usr/bin/env bash
set -euo pipefail

REPO="helmcode/nan-cli"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

if [ -t 1 ]; then
  BOLD="\033[1m"
  GREEN="\033[32m"
  CYAN="\033[36m"
  RESET="\033[0m"
else
  BOLD=""
  GREEN=""
  CYAN=""
  RESET=""
fi

log()  { printf "${GREEN}✓${RESET} %s\n" "$1"; }
info() { printf "${CYAN}‣${RESET} %s\n" "$1"; }

detect_arch() {
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "unsupported arch: $arch" >&2; exit 1 ;;
  esac
}

detect_os() {
  local os
  os="$(uname -s)"
  case "$os" in
    Linux)  echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)      echo "unsupported os: $os" >&2; exit 1 ;;
  esac
}

get_latest_version() {
  curl -sL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4
}

main() {
  local os arch version archive url

  os="$(detect_os)"
  arch="$(detect_arch)"

  if [ -z "$VERSION" ]; then
    info "fetching latest release..."
    version="$(get_latest_version)"
  else
    version="$VERSION"
  fi

  log "nan-cli ${BOLD}${version}${RESET} (${os}/${arch})"

  archive="nan-cli_${version}_${os}_${arch}.tar.gz"
  url="https://github.com/$REPO/releases/download/${version}/${archive}"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  info "downloading ${archive}..."
  curl -sL "$url" | tar xz -C "$tmpdir"

  info "installing to ${INSTALL_DIR}/nan..."
  install -d "$INSTALL_DIR"
  install -m 755 "$tmpdir/nan" "$INSTALL_DIR/nan"

  log "installed ${BOLD}${version}${RESET} to ${INSTALL_DIR}/nan"
}

main "$@"
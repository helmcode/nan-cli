#!/usr/bin/env bash
set -euo pipefail

REPO="helmcode/nan-cli"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

if [ -t 1 ]; then
  BOLD="\033[1m"
  GREEN="\033[32m"
  CYAN="\033[36m"
  YELLOW="\033[33m"
  RED="\033[31m"
  RESET="\033[0m"
else
  BOLD=""
  GREEN=""
  CYAN=""
  YELLOW=""
  RED=""
  RESET=""
fi

log()  { printf "${GREEN}✓${RESET} %s\n" "$1"; }
info() { printf "${CYAN}‣${RESET} %s\n" "$1"; }
warn() { printf "${YELLOW}⚠${RESET} %s\n" "$1"; }
err()  { printf "${RED}✗${RESET} %s\n" "$1" >&2; }

detect_arch() {
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) err "unsupported arch: $arch"; exit 1 ;;
  esac
}

detect_os() {
  local os
  os="$(uname -s)"
  case "$os" in
    Linux)  echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)      err "unsupported os: $os"; exit 1 ;;
  esac
}

get_latest_version() {
  curl -sL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4
}

verify_checksum() {
  local file="$1" expected="$2"
  local actual
  if command -v sha256sum &>/dev/null; then
    actual="$(sha256sum "$file" | cut -d' ' -f1)"
  elif command -v shasum &>/dev/null; then
    actual="$(shasum -a 256 "$file" | cut -d' ' -f1)"
  else
    warn "no sha256 tool found, skipping checksum verification"
    return 0
  fi
  if [ "$actual" != "$expected" ]; then
    err "checksum mismatch"
    err "  expected: $expected"
    err "  got:      $actual"
    exit 1
  fi
}

install_bin() {
  local src="$1" dest_dir="$2"
  if install -d "$dest_dir" && install -m 755 "$src" "$dest_dir/nan" 2>/dev/null; then
    return 0
  fi
  warn "no write access to $dest_dir, retrying with sudo..."
  sudo install -d "$dest_dir" && sudo install -m 755 "$src" "$dest_dir/nan"
}

check_path() {
  local dir="$1"
  case ":$PATH:" in
    *":$dir:"*) ;;
    *)
      warn "$dir is not in your PATH"
      warn "add this to your shell profile:"
      printf "    export PATH=\"%s:\$PATH\"\n" "$dir"
      ;;
  esac
}

main() {
  local os arch version archive url checksums_url expected_checksum

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
  checksums_url="https://github.com/$REPO/releases/download/${version}/checksums.txt"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  info "downloading ${archive}..."
  curl -fsSL "$url" -o "$tmpdir/$archive"

  info "verifying checksum..."
  expected_checksum="$(curl -fsSL "$checksums_url" | grep "$archive" | cut -d' ' -f1)"
  verify_checksum "$tmpdir/$archive" "$expected_checksum"

  tar xz -C "$tmpdir" -f "$tmpdir/$archive"

  info "installing to ${INSTALL_DIR}/nan..."
  install_bin "$tmpdir/nan" "$INSTALL_DIR"

  log "installed ${BOLD}${version}${RESET} to ${INSTALL_DIR}/nan"
  check_path "$INSTALL_DIR"
}

main "$@"

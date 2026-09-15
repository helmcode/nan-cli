#!/usr/bin/env bash
set -euo pipefail

# Overridable so a fork can install its own build, and so the failure paths
# below can be exercised against a repo that is not there.
REPO="${REPO:-helmcode/nan-cli}"
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
  # A token if one is around. The unauthenticated API allows 60 requests an
  # hour per IP, which anyone behind a shared address - an office, a CI runner,
  # a phone tether - can be on the wrong side of through no fault of their own.
  #
  # Two branches and no array. macOS ships bash 3.2.57, frozen in 2007 by its
  # licence, and there "${arr[@]}" on an EMPTY array under `set -u` is an
  # unbound variable - fixed in bash 4.4, which no Mac has. That is not an edge
  # case, it is every Mac: the script died on its first request and then blamed
  # the GitHub rate limit for it.
  #
  # `|| true` on the pipeline, because the caller decides what an empty answer
  # means. Without it `set -euo pipefail` kills the script where grep finds no
  # tag_name - which is the rate-limited case - and the message explaining it
  # never runs.
  local url="https://api.github.com/repos/$REPO/releases/latest"
  local token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  if [ -n "$token" ]; then
    curl -sL -H "Authorization: Bearer $token" "$url" | grep '"tag_name"' | cut -d'"' -f4 || true
  else
    curl -sL "$url" | grep '"tag_name"' | cut -d'"' -f4 || true
  fi
}

# An unauthenticated GitHub API is rate limited per IP, so this call can come
# back empty for reasons that have nothing to do with this repo. Without this
# check the empty version went straight into the archive name, and what the
# person saw was curl failing on a URL with a hole in it - which says nothing
# about what actually happened or what to do next.
require_version() {
  if [ -n "$1" ]; then
    return 0
  fi
  err "could not work out the latest version from the GitHub API"
  err "the usual cause is its rate limit on unauthenticated requests, which passes"
  err "wait a few minutes, or pick a version yourself:"
  printf "    VERSION=v0.1.19 curl -fsSL https://nan.builders/install | bash
" >&2
  err "the releases are at https://github.com/$REPO/releases"
  exit 1
}

verify_checksum() {
  local file="$1" expected="$2"
  local actual
  if command -v sha256sum &>/dev/null; then
    actual="$(sha256sum "$file" | cut -d' ' -f1)"
  elif command -v shasum &>/dev/null; then
    actual="$(shasum -a 256 "$file" | cut -d' ' -f1)"
  else
    # Not a warning. The checksum is the only thing standing between this
    # script and a binary that is not the one the release published, so
    # carrying on without it installs exactly what the check exists to catch.
    err "no sha256 tool found (sha256sum or shasum), so the download cannot be verified"
    err "install one of them, or take the release from"
    err "  https://github.com/$REPO/releases"
    exit 1
  fi
  if [ "$actual" != "$expected" ]; then
    err "checksum mismatch"
    err "  expected: $expected"
    err "  got:      $actual"
    exit 1
  fi
}

# The checksum above says the download arrived whole. It does not say who
# built it: the release that serves the archive serves checksums.txt too, so a
# release someone else published matches its own numbers perfectly.
#
# The build provenance is the part that answers that, and `gh` is what reads
# it. Not everyone has gh, and refusing to install without it would only teach
# people to skip the step - so this asks where it can, and is careful about the
# difference between "this binary is not what it claims to be" and "I could not
# reach GitHub to find out".
verify_provenance() {
  local file="$1" out

  if ! command -v gh &>/dev/null; then
    info "gh is not installed, so the build provenance was not checked"
    info "  to check it yourself later: gh attestation verify <file> --repo $REPO"
    return 0
  fi

  if out="$(gh attestation verify "$file" --repo "$REPO" 2>&1)"; then
    log "provenance verified: built by $REPO on GitHub Actions"
    return 0
  fi

  # Nothing recorded against these bytes, which is a 404 from the attestations
  # API. Two different things look identical from out here: a release from
  # before this repo signed anything, and an archive that is not the one it
  # signed - because a replaced archive has a digest nothing was ever signed
  # for. So this is a notice by default and not a guarantee, and it is fatal
  # for anyone who sets REQUIRE_PROVENANCE, which every release from now on
  # can satisfy.
  case "$out" in
    *"HTTP 404"*|*"no attestations found"*)
      if [ -n "${REQUIRE_PROVENANCE:-}" ]; then
        err "no build provenance is recorded for this archive"
        err "nothing was installed, because REQUIRE_PROVENANCE is set"
        exit 1
      fi
      warn "no build provenance is recorded for this archive"
      warn "releases published before this repo started signing carry none"
      warn "  REQUIRE_PROVENANCE=1 refuses to install those"
      return 0
      ;;
  esac

  # Something was recorded and it did not match, or gh could not ask. Asking
  # the API something trivial tells those apart: if it answers, gh works, and
  # the refusal above was about this archive.
  if gh api rate_limit >/dev/null 2>&1; then
    err "the build provenance of this archive does not check out"
    err "it is not what $REPO published, whatever its checksum says"
    err "nothing was installed"
    exit 1
  fi
  warn "could not reach GitHub to check the build provenance"
  warn "the checksum did match, so this is most likely the network"
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
    version="$(get_latest_version || true)"
    require_version "$version"
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

  info "checking who built it..."
  verify_provenance "$tmpdir/$archive"

  tar xz -C "$tmpdir" -f "$tmpdir/$archive"

  info "installing to ${INSTALL_DIR}/nan..."
  install_bin "$tmpdir/nan" "$INSTALL_DIR"

  log "installed ${BOLD}${version}${RESET} to ${INSTALL_DIR}/nan"
  check_path "$INSTALL_DIR"
}

main "$@"

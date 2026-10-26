#!/usr/bin/env bash
# Checks that `nan context` judges file names with the same rules and the
# same Unicode tables as cloud-api (helmcode/nan-devops AGENT-CONTEXT.md
# §1.5). The rules are a verbatim copy of cloud-api's
# internal/contexts/path.go, and they read Unicode from two places: the Go
# standard library (letters, marks, digits) and golang.org/x/text (NFC, case
# folding). If any of the three drifts, `nan context pull` can refuse a name
# the server stored, or push can send one the server refuses.
#
# Compared against cloud-api's default branch:
#   1. golang.org/x/text: the exact same version.
#   2. internal/contexts/path.go: byte-identical.
#   3. Go: the same minor as cloud-api's build image (Dockerfile) and go.mod;
#      when the minors differ, the two toolchains must at least carry the same
#      unicode.Version (the tables the rules read). A security patch can make
#      this CLI move first; it may not change the tables without cloud-api.
#   4. The record in internal/contexts/pins_test.go matches cloud-api, so the
#      test that runs everywhere (forks included) checks the right values.
#
# cloud-api is private: CLOUD_API_TOKEN must be a token that can read its
# contents (CI: the CLOUD_API_READ_TOKEN secret). Without it this fails;
# a check that passes because it could not look guards nothing.
set -euo pipefail

REPO="${CLOUD_API_REPO:-helmcode/nan-cloud-api}"
REF="${CLOUD_API_REF:-main}"
TOKEN="${CLOUD_API_TOKEN:-}"
root="$(cd "$(dirname "$0")/.." && pwd)"

fail() {
  echo "::error::$*" >&2
  exit 1
}

if [ -z "$TOKEN" ]; then
  fail "CLOUD_API_TOKEN is not set: it needs read access to $REPO (CI secret CLOUD_API_READ_TOKEN)"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fetch() {
  curl -fsSL \
    -H "Authorization: Bearer $TOKEN" \
    -H "Accept: application/vnd.github.raw+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/repos/$REPO/contents/$1?ref=$REF" -o "$2" ||
    fail "could not read $1 from $REPO@$REF"
}

fetch go.mod "$tmp/go.mod"
fetch Dockerfile "$tmp/Dockerfile"
fetch internal/contexts/path.go "$tmp/path.go"

# The version of golang.org/x/text a go.mod requires, in a require block or
# on a require line.
xtext_of() {
  awk '{ for (i = 1; i < NF; i++) if ($i == "golang.org/x/text") { print $(i + 1); exit } }' "$1"
}
go_directive_of() {
  awk '$1 == "go" { print $2; exit }' "$1"
}
minor_of() {
  echo "$1" | cut -d. -f1,2
}

# 1. golang.org/x/text
up_xtext="$(xtext_of "$tmp/go.mod")"
my_xtext="$(xtext_of "$root/go.mod")"
[ -n "$up_xtext" ] || fail "cloud-api's go.mod does not require golang.org/x/text"
[ -n "$my_xtext" ] || fail "go.mod does not require golang.org/x/text"
if [ "$up_xtext" != "$my_xtext" ]; then
  fail "golang.org/x/text: cloud-api builds with $up_xtext, nan-cli with $my_xtext. Both must use the same version: move the one that is behind (go get golang.org/x/text@VERSION) and update internal/contexts/pins_test.go"
fi
echo "golang.org/x/text $my_xtext on both sides"

# 2. path.go
if ! cmp -s "$tmp/path.go" "$root/internal/contexts/path.go"; then
  diff -u "$root/internal/contexts/path.go" "$tmp/path.go" >&2 || true
  fail "internal/contexts/path.go differs from cloud-api's: copy it again, with its test table (internal/contexts/path_test.go)"
fi
echo "internal/contexts/path.go is byte-identical to cloud-api's"

# 3. Go
up_image="$(sed -n 's/^FROM golang:\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' "$tmp/Dockerfile" | head -1)"
up_directive="$(go_directive_of "$tmp/go.mod")"
my_directive="$(go_directive_of "$root/go.mod")"
[ -n "$up_image" ] || fail "could not find the golang image in cloud-api's Dockerfile"
my_minor="$(minor_of "$my_directive")"

# unicode.Version of a toolchain, from a module of its own so this repo's
# go directive does not pick the toolchain.
unicode_of() {
  local dir="$tmp/uv-$1"
  mkdir -p "$dir"
  printf 'module uv\n\ngo 1.21\n' >"$dir/go.mod"
  printf 'package main\n\nimport (\n\t"fmt"\n\t"unicode"\n)\n\nfunc main() { fmt.Println(unicode.Version) }\n' >"$dir/main.go"
  (cd "$dir" && GOTOOLCHAIN="go$1" GOFLAGS=-mod=mod go run .)
}

up_unicode="$(unicode_of "$up_image.0")"
my_unicode="$(unicode_of "$my_directive")"
for up in "$up_image" "$(minor_of "$up_directive")"; do
  if [ "$up" != "$my_minor" ] && [ "$up_unicode" != "$my_unicode" ]; then
    fail "Go: cloud-api uses $up (Unicode $up_unicode), nan-cli $my_directive (Unicode $my_unicode): the path rules would read other tables"
  fi
done
if [ "$up_image" = "$my_minor" ]; then
  echo "Go $my_minor on both sides (Unicode $my_unicode)"
else
  echo "::notice::Go: cloud-api builds with $up_image, nan-cli with $my_directive; both carry Unicode $my_unicode"
fi

# 4. The record the unit test checks.
rec_xtext="$(sed -n 's/^[[:space:]]*cloudAPIXText[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' "$root/internal/contexts/pins_test.go")"
rec_unicode="$(sed -n 's/^[[:space:]]*cloudAPIUnicode[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' "$root/internal/contexts/pins_test.go")"
if [ "$rec_xtext" != "$up_xtext" ] || [ "$rec_unicode" != "$up_unicode" ]; then
  fail "internal/contexts/pins_test.go records x/text $rec_xtext and Unicode $rec_unicode; cloud-api has $up_xtext and $up_unicode"
fi
echo "internal/contexts/pins_test.go matches cloud-api"

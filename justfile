# Every command this repo has, in one place, for people and for agents.
# `just` on its own lists them.

default:
    @just --list

# Install the toolchain and the git hooks. Run once after cloning.
setup:
    mise install
    lefthook install

# Build the binary into ./nan.
build:
    CGO_ENABLED=0 go build -o nan ./cmd/nan

# Run the dashboard from source.
dev:
    go run ./cmd/nan

# Run against a local stand-in for nan.builders instead of production:
# NAN_API_URL, NAN_APP_URL and NAN_INFERENCE_URL point every request there.
#   just local
#   just local runs ls
local api="http://127.0.0.1:8080" *args="":
    NAN_API_URL={{api}} NAN_APP_URL={{api}} NAN_INFERENCE_URL={{api}}/v1 go run ./cmd/nan {{args}}

# Regenerate THIRD-PARTY-NOTICES.txt from what is actually linked into the
# binary. Run it after adding, removing or bumping a dependency: one of them
# is Apache-2.0, which requires the notice to travel with a redistribution.
notices:
    go run scripts/gen-notices.go

test:
    go test -race ./...

# What CI runs. `fmt --diff` exits non-zero on a difference; `go fmt` would
# rewrite the files and exit 0 either way, which is a check that cannot fail.
lint:
    golangci-lint run ./...
    golangci-lint fmt --diff

fmt:
    golangci-lint fmt

vuln:
    go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

# Build every target the release publishes, to catch a break that only shows
# on a platform this machine is not.
cross:
    #!/usr/bin/env bash
    set -euo pipefail
    for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
        echo "building ${target}"
        CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build -o /dev/null ./cmd/nan
    done

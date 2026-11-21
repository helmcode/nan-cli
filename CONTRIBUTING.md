# Contributing to nan-cli

Thanks for your interest in contributing. This document covers how to get the project running locally, the conventions to follow, and how to add support for new tools.

## Prerequisites

- **Go 1.26.11+**, the version in `go.mod`, which is where the standard
  library carries the current security fixes. The project uses [mise](https://mise.jdx.dev/) to pin it, with golangci-lint, lefthook and just: run `just setup` (or `mise install`) in the repo root.
- A [nan.builders](https://nan.builders) account with an API key (needed to try the dashboard at runtime).

## Getting started

```bash
git clone https://github.com/helmcode/nan-cli
cd nan-cli
just build      # or: go build -o nan ./cmd/nan
./nan
```

The binary opens the dashboard. Its **Harnesses** area (6, or `e` from anywhere) sets your API key and configures the tools; `nan setup` does the same from the command line.

To run against a local stand-in instead of production, point `NAN_API_URL`,
`NAN_APP_URL` and `NAN_INFERENCE_URL` at it, or use `just local`.

To install a released binary instead of building from source:

```bash
curl -fsSL https://nan.builders/install | bash
```

By default it installs to `/usr/local/bin`. Override with `INSTALL_DIR`:

```bash
INSTALL_DIR=~/.local/bin curl -fsSL https://nan.builders/install | bash
```

## Project structure

The same structure as the Helmcode CLI, on purpose: the two are maintained
the same way.

```
cmd/nan/main.go           Entry point: wiring and exit codes, nothing else
internal/
  build/                  Version, name and repository of this binary
  api/                    nan.builders: the session client (auth, me, usage,
                          keys, models) and the /v1/runs client with its SSE stream
  session/                Session persistence (~/.config/nan/session.json)
  tools/                  The coding tools: detection, writers and removers
  models/                 The member's model list (from the platform) and the
                          metadata this CLI knows about each model
  flow/                   The sign-in and the setup, as Bubble Tea models
  pulse/                  The dashboard (Bubble Tea v2)
  cli/                    The cobra commands, run through fang
  runs/                   Run events to terminal lines, guest text sanitised
  ui/                     The NaN palette, wordmark, mascot and components
  update/                 Whether a newer release is out, and new models
```

Every tool lives in `internal/tools`: one file per tool with its writer and
remover, and one entry in `Registry()` in `tools.go` that names its binary,
its config file, how to tell it is configured and what to do in it once it
is.

## Adding support for a new tool

1. **Add an entry to `Registry()`** in `internal/tools/tools.go` with the tool's binary, config path, `Open` and `NextStep`, and its `configured` check (most live in `detect.go`).

2. **Implement the writer** in a file of its own: write the NaN provider block into the tool's config format, through `writeConfigFile` so the file ends up 0600. Always check if NaN is already present before writing, and take the model list as a parameter (`cat []catalog.Model`, built by `catalog.Load` from the platform's `/api/agents/models` for this member) and write exactly those models, never ids of your own: the platform already leaves out what the member cannot call, so a model missing from the list is removed from the config, not kept. Leave a window or output limit out when it is unknown (`Context == 0`), and never lower one the config already has because a catalogue carried no figure.

3. **Implement the remover**: take out NaN's entries cleanly without touching the rest of the file.

4. **Test it** in `internal/tools/tools_test.go`, which writes each config into a temporary home and checks it against a catalogue.

## Adding a new NaN model

Nothing to do for the list: Setup configures whatever `/api/agents/models` returns for the member (falling back to the key's own `/v1/models` when the platform cannot answer), so a new model reaches every tool on the next `nan setup`.

The platform also reports `context_window`, `max_output_tokens`, `supports_vision` and `supports_reasoning`. Those win. `All` in `internal/models/models.go` is only metadata for older platforms that report none of them: add a model there to give it a name, a window and its input kinds when the platform does not. The windows are the ones the setup guides publish on nan.builders (/docs/opencode, /docs/pi), measured against the proxy, so copy them as they are rather than rounding: a window written short makes the tool compact a session that had room left, and one written long makes it fill the conversation until the model starts refusing requests.

The Codex config (`writeCodexConfig`) starts on `catalog.Pick(cat, catalog.Coding)`: the coding model when the member can call it, otherwise a model they can.

Then run `just test`. The tests write each config into a temp directory and check it against a catalogue, including a catalogue with no figures at all.

## Code style

- `just lint` must pass: golangci-lint with the repo's `.golangci.yml`, the same configuration as the Helmcode CLI. The git hooks (`just setup`) run it before each commit.
- No comments unless the *why* is non-obvious - well-named identifiers are preferred.
- Error messages are lowercase and don't end in punctuation (Go stdlib convention).
- No new dependencies without discussion - the dependency list is intentionally small.

## Commit messages

This project follows the [Conventional Commits](https://www.conventionalcommits.org/) specification.

```
<type>[optional scope]: <description>

[optional body]

[optional footer]
```

Common types:

| Type | When to use |
|---|---|
| `feat` | New feature or tool support |
| `fix` | Bug fix |
| `docs` | Changes to documentation only |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `chore` | Build process, dependencies, tooling |

Examples:

```
feat(setup): add support for Zed editor
fix(opencode): merge missing models on existing config
docs: update model list in CONTRIBUTING
chore: bump version to 0.1.2
```

Breaking changes must include a `!` after the type and a `BREAKING CHANGE:` footer:

```
feat!: remove legacy cookie-polling login flow

BREAKING CHANGE: nan auth login no longer reads browser cookies automatically.
```

## Submitting changes

1. Fork the repo and create a branch from `main`.
2. Make your changes and verify the dashboard builds and runs: `just build && ./nan`, and that `just test` and `just lint` pass.
3. Open a pull request - the title should follow the same Conventional Commits format as your commits.

## Releases

Releases publish themselves from the version constant. There is no separate
tagging step to remember:

1. Bump `Version` in `internal/build/build.go` in a `chore: <version>` commit.
2. Merge it to `main`.
3. Once CI passes on `main`, `.github/workflows/tag.yml` creates the matching
   `v<version>` tag and the release workflow builds, signs and publishes the
   archives for every platform.

The tag always matches the constant, in both directions: the tag is derived
from it, and the release workflow refuses to build a tag that disagrees with
it. `scripts/install.sh` asks GitHub for the latest release, so whatever this
publishes is what `curl -fsSL https://nan.builders/install | bash` serves
minutes later.

Merges that do not change the constant tag nothing - the version you bump to
is the release you get. To rebuild a release that failed halfway, run the
release workflow by hand from the Actions tab with an existing tag.

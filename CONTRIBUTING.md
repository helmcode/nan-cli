# Contributing to nan-cli

Thanks for your interest in contributing. This document covers how to get the project running locally, the conventions to follow, and how to add support for new tools.

## Prerequisites

- **Go 1.26+** — the project uses [mise](https://mise.jdx.dev/) to pin the version. Run `mise install` in the repo root and Go will be available automatically.
- A [nan.builders](https://nan.builders) account with an API key (needed to test the TUI at runtime).

## Getting started

```bash
git clone https://github.com/helmcode/nan-cli
cd nan-cli
go build -o nan .
./nan
```

The binary opens the Bubbletea TUI directly. Use the **Setup** tab to configure your API key and test tool auto-configuration.

To install a released binary instead of building from source:

```bash
curl -fsSL https://nan.builders/install | bash
```

By default it installs to `/usr/local/bin`. Override with `INSTALL_DIR`:

```bash
INSTALL_DIR=~/.local/bin curl -fsSL https://nan.builders/install | bash
```

## Project structure

```
main.go                   Entry point — delegates to cmd.Execute()
cmd/                      Cobra subcommands (auth, me, metrics)
internal/
  api/client.go           HTTP client for the nan.builders REST API
  session/session.go      Session persistence (~/.config/nan/session.json)
  tui/tui.go              Entire TUI — layout, renderers, and tool writers
```

All tool auto-configuration logic lives in `internal/tui/tui.go`. The relevant functions are:

| Function | Purpose |
|---|---|
| `detectTools()` | Checks which tools are installed on the system |
| `isNaNConfigured()` | Reads a tool's config file to check if NaN is already set up |
| `configureTools()` | Dispatches to the per-tool write functions |
| `writeXxxConfig()` | Writes or updates a specific tool's config file |
| `removeXxxConfig()` | Removes NaN entries from a tool's config file |

## Adding support for a new tool

1. **Add an entry to `detectTools()`** with the tool's binary name and config file path.

2. **Implement `writeXxxConfig(cfgPath, apiKey string) error`** — write the NaN provider block into the tool's config format. Always check if NaN is already present before writing, and include all current NaN models:
   - `qwen3.6` — Qwen 3.6 35B A3B
   - `gemma4` — Gemma 4 26B A4B
   - `deepseek-v4-flash` — DeepSeek V4 Flash 284B A13B

3. **Implement `removeXxxConfig(cfgPath string) error`** — remove any NaN-related entries cleanly without touching the rest of the file.

4. **Implement the `isNaNConfigured` case** for the new tool name so the Setup tab shows the correct status.

5. **Wire it up in `configureTools()`** — add cases for the new tool in both the enable and disable branches.

## Adding a new NaN model

Models appear in three places in `tui.go`. Search for an existing model ID (e.g. `gemma4`) and add the new entry alongside it in each:

- `writeFactoryConfig` — `nanModels` slice
- `writeOpencodeConfig` — `nanModels` map
- `writePiConfig` — models array in the TypeScript template

The Codex config (`writeCodexConfig`) sets a default model but does not enumerate models, so no change is needed there.

## Code style

- Follow standard Go conventions (`gofmt`, `go vet`).
- No comments unless the *why* is non-obvious — well-named identifiers are preferred.
- Error messages are lowercase and don't end in punctuation (Go stdlib convention).
- No new dependencies without discussion — the dependency list is intentionally small.

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
2. Make your changes and verify the TUI builds and runs: `go build -o nan . && ./nan`.
3. Open a pull request — the title should follow the same Conventional Commits format as your commits.

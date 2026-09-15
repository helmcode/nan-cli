# nan

CLI for [nan.builders](https://nan.builders) — manage your account, monitor usage, and auto-configure AI coding tools with the NaN API.

## Install

```bash
curl -fsSL https://nan.builders/install | bash
```

By default installs to `/usr/local/bin`. Override with `INSTALL_DIR`:

```bash
INSTALL_DIR=~/.local/bin curl -fsSL https://nan.builders/install | bash
```

On Windows, from PowerShell:

```powershell
irm https://nan.builders/install.ps1 | iex
```

Same work: latest release, the `.zip` for your architecture, checksum verified,
`nan.exe` into `%LOCALAPPDATA%\Programs\nan` and that directory added to your
user `PATH`. Override with `-InstallDir`:

```powershell
& ([scriptblock]::Create((irm https://nan.builders/install.ps1))) -InstallDir "C:\tools"
```

## Verifying a download

Both installers check the archive against `checksums.txt` from the same
release. That catches a download that arrived damaged; it cannot catch a
release that was published by somebody else, because whoever replaces an
archive can replace the checksum beside it.

Every release is signed with [build provenance](https://docs.github.com/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds),
which is not something a release you did not build can carry. If you have the
[GitHub CLI](https://cli.github.com), the installers check it for you, and you
can ask about any archive yourself:

```bash
gh attestation verify nan-cli_v0.1.19_linux_amd64.tar.gz --repo helmcode/nan-cli
```

It answers with the workflow, the repository and the commit the binary was
built from.

## Usage

Run `nan` to open the TUI dashboard:

```
nan
```

Navigate with `←/→` between tabs, `↑/↓` to scroll, `r` to refresh, `?` for help, `q` to quit.

### Tabs

| Tab | Description |
|---|---|
| **Profile** | Your nan.builders account details |
| **Usage** | Token usage across the last 24h, 30 days, and all time |
| **Models** | Available models and your usage per model |
| **Costs** | Estimated cost comparison against other providers |
| **Setup** | API key management and tool auto-configuration |
| **About** | Version and links |

### Setup tab

The Setup tab lets you configure AI coding tools to use the NaN API automatically. Supported tools:

- [OpenCode](https://opencode.ai)
- [Factory AI](https://factory.ai) (`droid`)
- [Pi](https://pi.dev)
- [Codex](https://github.com/openai/codex)
- [Hermes](https://hermes-agent.nousresearch.com/)

Press `e` to set your API key, `space` to toggle tools, and `c` to apply the configuration.

Only installed tools are listed, and only the NaN part of each config is
touched: everything else in those files is left as it was, and unticking a
tool takes ours back out. The key you paste is checked against the cluster
before you apply it, so a mistyped one is caught here rather than as a 401
inside each tool later.

## Build from source

Requires Go 1.26.8+ ([mise](https://mise.jdx.dev/) recommended) — the version
in `go.mod`, which is where the standard library carries the current security
fixes:

```bash
git clone https://github.com/helmcode/nan-cli
cd nan-cli
go build -o nan .
./nan
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

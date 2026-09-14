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

On Windows, download `nan-cli_<version>_windows_amd64.zip` (or `arm64`) from
the [latest release](https://github.com/helmcode/nan-cli/releases/latest),
unzip it and put `nan.exe` somewhere on your `PATH`. The installer above is
bash, so it is not the way in there.

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

Requires Go 1.26+ ([mise](https://mise.jdx.dev/) recommended):

```bash
git clone https://github.com/helmcode/nan-cli
cd nan-cli
go build -o nan .
./nan
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

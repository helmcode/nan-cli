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

Run `nan` to open the dashboard:

```
nan
```

On a machine nobody has signed in on, it opens on the sign-in: your email, then
the link from it, then your API key for this machine (skippable).

Seven areas run down the left, always on screen and numbered. `1`-`7` or `tab`
move between them, `↑/↓` and `enter` pick a row and show what can be done with
it, `/` finds anything by name, `e` opens the setup from anywhere, `s` signs in,
`o` signs out (twice, to confirm), `r` reloads, `t` takes a short tour, `?`
shows every key and `q` quits.

| Area | What it shows |
|---|---|
| **Home** | What needs you (sign in, a missing key, a tool not pointed at NaN, a newer release), new models, and shortcuts |
| **Profile** | Your email (shortened), your plan, and when the membership ends if it has |
| **Usage** | Token usage over the last 24 hours, 30 days and all time, by model |
| **Models** | What your key can call, and your usage of each |
| **Costs** | Estimated cost of that usage on other providers |
| **Harnesses** | Your API key and the coding tools on this machine |
| **About** | Version, links and where the session is kept |

When a newer release is out, the header says so and Home has the command that
updates your copy; after any other command, one line on stderr says the same.

### Setting up your tools

`e` in the dashboard, or `nan setup` on the command line, configures the AI
coding tools on this machine to use the NaN API. Supported tools:

- [OpenCode](https://opencode.ai)
- [Factory AI](https://factory.ai) (`droid`)
- [Pi](https://pi.dev)
- [Codex](https://github.com/openai/codex)
- [Hermes](https://hermes-agent.nousresearch.com/)

Paste your key, tick the tools, and it writes them. Only installed tools are
listed, and only the NaN part of each config is touched: everything else in
those files is left as it was, and unticking a tool takes ours back out. The
key is checked against the cluster before anything is written, so a mistyped
one is caught here rather than as a 401 inside each tool later.

```bash
nan setup                                   # asks, and shows what is installed
echo "$NAN_API_KEY" | nan setup --key - --all   # for a machine with nobody at it
nan setup status                            # what is configured right now
nan setup remove                            # take NaN back out of every tool
```

## Agent runs

`nan run` starts an agent (Pi, Pi + Gentle Shell or Hermes) in one of your
[workspaces](https://cloud.nan.builders) and streams what it does. The run
happens in the workspace, not on your machine: it keeps going if the command
stops, and its log stays on nan.builders.

```bash
nan run "review PR 42 and write the findings to artifacts/review.md"
nan run --ws develop --cwd /home/nan/projects/api -f task.md
git diff | nan run -
id=$(nan run --detach "upgrade the dependencies")
```

| Flag | Default | |
|---|---|---|
| `--ws NAME` | your only workspace | Workspace to run in. Required when you have more than one. |
| `--agent pi\|pi_gentle\|hermes` | `pi` | Agent to run: `pi_gentle` is Pi with Gentle Shell. The workspace must have it installed. |
| `--model M` | the agent's own | Model for the agent. |
| `--cwd PATH` | `/home/nan` | Folder in the workspace. |
| `--worktree` / `--no-worktree` | auto | Work on a separate branch, or in the folder itself. See below. |
| `--timeout D` | `30m` | Stop the run after this long, `1m` to `2h`. |
| `--detach` | | Queue the run, print its id and return. |
| `--json` | | Events as JSON lines, then the finished run object. With `--detach`, the run object. |
| `--idempotency-key K` | | Retrying with the same key and request returns the run already created. |
| `-f FILE` | | Read the prompt from a file (`-` for stdin). |
| `--token-file PATH` | | Authenticate with the token on the first line of `PATH`. See [Authentication](#authentication). |

**Git branch.** By default (auto) a run inside a git repository works on its
own branch, `nan-run/<first 8 chars of the run id>`, and commits there. It
never pushes, and your checkout is untouched. Outside a repository it works in
the folder itself. `--worktree` forces a branch (and fails outside a
repository); `--no-worktree` works in the folder even inside a repository.

**Queue.** If the workspace is already running as many runs as its size
allows, the run waits as `queued` and starts when one of them ends.

**In the portal** the same settings have other names: Folder is `--cwd`,
Separate branch is `--worktree` / `--no-worktree`, Time limit is `--timeout`,
and Task is the prompt.

While it streams, **Ctrl-C** detaches and leaves the run going, and prints how
to pick it up again. A second Ctrl-C within 2 seconds cancels the run.

`--detach` exits 75 on purpose (the run is still going), so under `set -e`
allow for it:

```bash
id=$(nan run --detach "upgrade the dependencies") || [ $? -eq 75 ]
```

A script that may retry should pass `--idempotency-key`: a retry after a lost
connection then gets the run it already started instead of a second one.

Everything the agent prints is shown with escape sequences and control
characters removed: the output comes from your workspace, and it is not
allowed to drive your terminal.

```bash
nan runs ls [--ws NAME] [--state S] [--limit 20] [--json]   # newest first
nan runs show <id> [--json]                                 # state, result, prompt
nan runs logs <id> [-f] [--after N] [--json]                # -f follows until it ends
nan runs cancel <id> [--json]
nan runs proposal <id> [--diff] [--json]                    # what an automation run proposed
nan runs reject <id> [--reason R]                           # turn the proposal down
```

### Automations, approvals and secrets

Automations are created and edited in the portal. From the CLI you can list,
inspect and run them, read their webhook deliveries, see what waits for your
approval, and list your secrets (names and metadata, never values):

```bash
nan automations ls [--json]
nan automations show <id|name> [--json]
nan automations run <id|name> [--input @payload.json | --input - | --input JSON] [--detach] [--json]
nan automations deliveries <id|name> [--limit 50] [--json]
nan approvals ls [--state pending|approved|rejected|expired|cancelled|auto|all] [--limit 20] [--json]
nan approvals show <run-id> [--diff]                        # same as nan runs proposal
nan secrets ls [--json]                                     # needs nan auth login
```

Approving a proposal, writing a secret and creating or editing an automation
happen only in the portal at <https://cloud.nan.builders>, signed in through
the browser. Each of them can hand a secret to an agent or let it act on your
repository without asking, so none is possible with a credential an agent or
a CI job could read: this machine's CLI session, a platform token or an API
key. A platform token needs the `automations:read` scope to list and inspect
automations and `automations:run` to start one.

The text views show workspace and automation names, looked up once per
command; detail views print `name (id)` so the id stays copyable. A token
without `workspaces:read` (or `automations:read`, for automation names in
`nan approvals ls` and `nan runs show`) gets the ids instead, never an error.
`--json` prints the API's objects unchanged, with ids only.

### Exit codes

`nan run`, `nan runs logs -f` and `nan automations run` exit with the run's outcome, so a script can
branch on it without parsing text:

| Code | Meaning |
|---|---|
| 0 | succeeded |
| 1 | failed |
| 2 | timed out |
| 3 | cancelled (after a double Ctrl-C: cancel requested — `nan runs show` has the final state) |
| 4 | the workspace is not set up for it: agent not installed, no inference key, bad configuration |
| 10 | awaiting approval: an automation run whose proposed actions wait for you (`nan runs proposal <id>`, approve in the portal) |
| 64 | usage error (also: more than one workspace and no `--ws`) |
| 65 | not signed in, session expired, or not allowed |
| 69 | nan.builders unavailable, or the stream was lost after retries (the run goes on; the id is printed) |
| 75 | `--detach`, or Ctrl-C: the run was accepted and is still going |

Full reference, including the event types `nan runs logs --json` prints (one
JSON object per line): <https://nan.builders/docs/runs>.

### Authentication

`nan run` and `nan runs` use the first of these they find:

1. **`NAN_TOKEN`**: an environment variable holding a platform token
   (`nan_pat_...`) or an API key (`sk-...`). Read on every command, never
   written to disk.
2. **`--token-file PATH`**: the token on the first line of a file. The file
   must be private (`chmod 600`); one that other users can read or write is
   refused.
3. **A saved token**, from `nan auth login --api-token`.
4. **Your session**, from `nan auth login` (the emailed sign-in link).
5. **A saved API key**, from `nan setup` or the dashboard (or an `sk-...` key saved with
   `nan auth login --api-token`).

Create platform tokens in **Settings > Tokens** at
<https://cloud.nan.builders>. A token works for runs, for automations (with the
`automations:read` and `automations:run` scopes) and for listing your
workspaces, and nothing else: it is not an inference key, and `nan me`,
`nan metrics` and the dashboard still need `nan auth login`.

**Headless: CI or a server.** Keep the token in your CI's secret store and
expose it as `NAN_TOKEN`. For example, in GitHub Actions:

```yaml
- name: Review the PR with an agent
  env:
    NAN_TOKEN: ${{ secrets.NAN_TOKEN }}
  run: |
    curl -fsSL https://nan.builders/install | bash
    nan run --ws ci --timeout 20m -f .github/review-task.md
```

On a machine you set up once, save the token instead. It is read from stdin
(hidden when you paste it), checked against nan.builders, and stored in
`~/.config/nan/session.json` with mode `0600`:

```bash
nan auth login --api-token              # paste it when asked
nan auth login --api-token < token.txt  # or pipe it in
```

Never put a token on the command line itself: it would end up in your shell
history and in `ps`.

## Build from source

Requires Go 1.27.2+ ([mise](https://mise.jdx.dev/) recommended), the version
in `go.mod`, which is where the standard library carries the current security
fixes:

```bash
git clone https://github.com/helmcode/nan-cli
cd nan-cli
go build -o nan ./cmd/nan
./nan
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

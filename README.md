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
| `--model M` | the agent's own | Model for the agent. Required with `--byok-secret` and `--ephemeral`. |
| `--byok-secret NAME` | NaN inference | Run on your own provider key: the name of a provider key secret saved in the portal. See below. |
| `--ephemeral` | | Run in a fresh, throwaway container instead of a workspace. See below. |
| `--repo OWNER/NAME` | | With `--ephemeral`: the public GitHub repository the container clones. |
| `--size s\|m\|l` | `s` | With `--ephemeral`: the container size. |
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

**Your own provider key (BYOK).** `--byok-secret NAME --model ID` runs the
agent on your own key instead of NaN inference. `NAME` is a secret of kind
`provider_key` you saved in the portal (Settings > Secrets > Provider key:
Anthropic, OpenAI, Google Gemini or any OpenAI-compatible endpoint), never the
key itself; `ID` is a model your provider serves. Your provider bills the run
and NaN does not meter it. Without a NaN inference plan, an active workspace
of your own is enough. These runs need your `nan auth login` session: a
platform token or an API key cannot start them (a prompt can make the agent
print the key it holds), but can trigger an automation that uses your key.
Hermes cannot use an Anthropic key yet. Docs: <https://nan.builders/docs/runs>.

```bash
nan secrets ls                                               # KIND provider_key, PROVIDER, BASE URL
nan run --byok-secret ANTHROPIC_KEY --model claude-sonnet-4-5 "fix the flaky test"
```

**Ephemeral runs (no workspace).** `--ephemeral --repo OWNER/NAME --model ID`
runs the agent in a fresh, throwaway container: it clones the repository, runs
the agent with the model you name, streams like any run, and is destroyed at
the end. The container can only reach the internet over HTTP and HTTPS (no
SSH, no SMTP). A run started from the CLI gets no secrets, so the repository
has to be public; for a private one, use an ephemeral automation (`executor:
ephemeral`, `checkout: fresh`, `github_repo`, `model`, and an agent secret
`GITHUB_TOKEN`). Starting one needs your session or a platform token with the
`runs:ephemeral` scope; API keys cannot. Docs:
<https://nan.builders/docs/ephemeral-runs>.

| Size | Container | Price per started minute |
|---|---|---|
| `s` (default) | 1 vCPU, 4 GiB RAM, 10 GB disk | €0.003 |
| `m` | 2 vCPU, 8 GiB RAM, 16 GB disk | €0.006 |
| `l` | 4 vCPU, 12 GiB RAM, 20 GB disk | €0.012 |

A 14 s run is 1 minute. Free minutes every month: Community 100, Inference 300,
Premium 1000; they come with a membership (accounts without one have none).
Free minutes are S-minutes: one M minute uses 2, one L minute 4 (100 free
minutes = 100 minutes of S, 50 of M or 25 of L). Ephemeral runs need a payment
method on file, free minutes included ("Add a payment method" in the portal,
Settings → Ephemeral runs; a subscription billed in USD is moved with "Switch
billing to EUR" there). While billing is not active, usage is
metered and shown, not charged. `nan runs ls` and `nan runs show` show each
ephemeral run's size and cost. The COST column of `nan runs ls` appears only
when an ephemeral run is listed, so scripts should read `nan runs ls --json`
rather than the columns.

```bash
nan run --ephemeral --repo acme/api --model glm5.3 --size m "find and fix the flaky test"
nan ephemeral usage      # this month's minutes, cost, free S-minutes left, prices
nan ephemeral settings   # your monthly spend cap and concurrent runs (change them in the browser)
```

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

From the CLI you can create, change and run automations, manage their
triggers, read their webhook deliveries, see what waits for your approval,
and manage your secrets (values go in, only names and metadata come out):

```bash
nan automations ls [--json]
nan automations show <id|name> [--json]
nan automations create -f automation.yaml                   # YAML or JSON, - for stdin
nan automations edit <id|name> -f changes.yaml              # only the fields in the file change
nan automations enable|disable <id|name>
nan automations rm <id|name> [--yes]
nan automations triggers ls <id|name> [--json]
nan automations triggers add <id|name> -f trigger.yaml [--json]   # webhook URL + secret shown ONCE
nan automations triggers edit <id|name> <trigger-id> -f changes.yaml
nan automations triggers rotate <id|name> <trigger-id> [--url] [--secret] [--no-grace]
                               [--signing-secret-file PATH | --signing-secret-stdin] [--json]
nan automations triggers rm <id|name> <trigger-id> [--yes]
nan automations run <id|name> [--input @payload.json | --input - | --input JSON] [--detach] [--json]
nan automations deliveries <id|name> [--limit 50] [--json]
nan approvals ls [--state pending|approved|rejected|expired|cancelled|auto|all] [--limit 20] [--json]
nan approvals show <run-id> [--diff]                        # same as nan runs proposal
nan secrets ls [--json]
nan secrets show NAME [--json]
nan secrets set NAME (--value-file PATH | --value-stdin) [--kind provider_key --provider P --base-url URL] [--description D]
nan secrets edit NAME [--description D] [--base-url URL]
nan secrets rm NAME [--yes]
```

A secret's value is never an argument (it would stay in your shell history):
it comes from `--value-file` or a pipe into `--value-stdin`, and is never
shown again. A webhook's URL and signing secret are printed once, to stdout,
when the trigger is added or rotated: store them in the sender right away.
A Stripe webhook is the exception: Stripe issues its signing secret, so it
goes in the `-f` file when you add the trigger (pipe the file with `-f -`),
and on rotate you roll it in the Stripe dashboard and pass the new `whsec_`
with `--signing-secret-file` or `--signing-secret-stdin`.

Writes need your session from `nan auth login`, or a platform token with the
scope of what it does: `secrets:read` to list secrets, `secrets:write` to
change them, `automations:write` to change automations (plus
`automations:read` to find one by name). Those scopes are added to a token in
the portal only, signed in through the browser. API keys (`sk-`) and
workspace keys never read or write secrets and never change automations.
Whoever holds a credential that can write automations can reach every secret
you have (an automation receives the secrets you assign it), so sign in on
your own machine, not inside a workspace, and give a workspace or CI job a
token with only the scopes it needs. If your account does not have the write
API yet, these commands exit 64 and point at the portal.

Approving a proposal stays portal-only, at <https://cloud.nan.builders>,
signed in through the browser: a credential an agent or a script can hold
never approves what an agent proposed. A platform token needs the
`automations:read` scope to list and inspect automations and
`automations:run` to start one.

The text views show workspace and automation names, looked up once per
command; detail views print `name (id)` so the id stays copyable. A token
without `workspaces:read` (or `automations:read`, for automation names in
`nan approvals ls` and `nan runs show`) gets the ids instead, never an error.
`--json` prints the API's objects unchanged, with ids only.

### Notifications

Get told in Slack, Microsoft Teams, Discord or your own HTTPS endpoint when an
automation run waits for approval, fails or finishes, when an automation is
disabled, or when a webhook trigger keeps failing:

```bash
nan notifications ls [--json]
nan notifications show <NAME|id> [--json]
nan notifications add NAME --kind slack|teams|discord|https (--url-file PATH | --url-stdin)
                      [--event EVENT]... [--no-events] [--automation <name|id>] [--json]
nan notifications edit <NAME|id> [--name NEW] [--enable | --disable] [--url-file PATH | --url-stdin] [--json]
nan notifications rm <NAME|id> [--yes]
nan notifications test <NAME|id> [--json]                    # exit 1 when not delivered
nan notifications rotate <NAME|id> [--json]                  # https only: new signing secret, shown ONCE
nan notifications deliveries <NAME|id> [--limit 20] [--json]
nan notifications subscriptions <NAME|id> [--set|--add|--remove EVENT[=AUTOMATION]]... [--clear] [--json]
```

A webhook URL is a credential (whoever has it can post to your channel): it
comes from `--url-file` or a pipe into `--url-stdin`, never from the command
line, and NaN never shows it again (only the host and its last characters).
An `https` channel's signing secret is printed once, to stdout (inside the
JSON with `--json`); requests are signed like NaN's inbound webhooks. A
channel turns itself off after 20 failed deliveries in a row: fix the
receiver and turn it back on with `nan notifications edit NAME --enable`.
A platform token needs `notifications:read` to read and
`notifications:write` to change, added in the portal only. Without
notifications on your account, these commands exit 64 and point at the
portal. Docs: <https://nan.builders/docs/notifications>.

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
| 65 | not signed in, session expired, or not allowed (also: a token without `runs:ephemeral`, ephemeral runs not available to you) |
| 69 | nan.builders unavailable, the stream was lost after retries (the run goes on; the id is printed), or an ephemeral run would pass your monthly spend cap |
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
5. **A saved API key**, from the Setup tab (or an `sk-...` key saved with
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

Requires Go 1.26.9+ ([mise](https://mise.jdx.dev/) recommended) — the version
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

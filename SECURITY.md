# Security policy

## Reporting a vulnerability

Use GitHub's **[Report a vulnerability](https://github.com/helmcode/nan-cli/security/advisories/new)**
button, under the Security tab. It opens a private thread with the maintainers
— nothing is public until there is a fix.

Please don't open a normal issue for a security problem. If the button is not
there yet, open an issue asking for a private channel and leave the details
out of it.

What helps, roughly in order:

- what an attacker gets, and what they need in the first place
- the steps to reproduce it, with the version (`nan --version`) and the OS
- whether it has already been exploited or disclosed anywhere

You'll get an acknowledgement as soon as somebody has read it, and an
assessment before anything ships. If you want to be credited in the advisory,
say so and say how.

## What this covers

This repository: the `nan` CLI, the two installers under `scripts/`, and the
release workflow that builds and signs what they download.

The nan.builders platform — the API the CLI talks to and the keys it issues —
is a separate system. A report about it is welcome through the same channel and
will be passed on.

## Versions

Fixes go into the next release. There are no backports: the installers fetch
the latest release, and `nan --version` against
[the releases page](https://github.com/helmcode/nan-cli/releases) is the whole
of the support matrix.

## Known, accepted, and not a finding

Two things about this CLI look like vulnerabilities and are deliberate, so
you're not wasting your time reporting them:

**Your API key is stored in plaintext.** It goes into `~/.config/nan/session.json`
and into each configured tool's own config file. Those tools read a literal key
out of their configs, so there is nowhere else to put it. Every file the CLI
writes it into is created `0600` and rewritten through a temp file so the mode
holds even where the file already existed, and `nan auth logout` takes the key
back out of every one of them. Hermes is the exception, because it has a
secrets file of its own: there the key goes into its `.env` and the config
carries a `${NAN_API_KEY}` reference, which is also why it never reaches a
command line.

**On Windows the mode bits do nothing.** The files inherit the ACL of the user
profile they sit in, which already excludes other non-administrator accounts,
and an administrator can take ownership regardless. If you keep your home
directory on a network share, this is worth knowing about.

What *is* worth reporting: the key reaching somewhere neither of those covers —
a log, an error message, a command line, a crash dump, a file with a wider
mode, or a tool's config after you have logged out.

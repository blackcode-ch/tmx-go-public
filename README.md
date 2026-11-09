<!-- Public build and contribution instructions revised 2026-11-09. -->
<p align="center">
  <img src="build/appicon.png" alt="tmx icon" width="112" height="112">
</p>

<h1 align="center">tmx</h1>

<p align="center">
  Your coding agents, run as a team on your own machine.
</p>

One binary that runs a tree of coding agents on your machine: a head agent, a lead per project, and
workers under each lead. The same binary is the daemon, the CLI, the MCP server and the desktop app.
It also has peer-to-peer chats with other people's tmx (QR invites, DMs, groups), a link to
Companion, and a Board: the head's conversation as strings of cards, one string per topic.

## This public repository

This is an automatically generated, scrubbed snapshot. Personal and machine names
are replaced and private documentation is omitted. Some identifiers, including
the public MCP tool `announce_set`, differ from the private build. Work from this public tree:
fork this repository, branch from `main`, and open a pull request against public
`main`. No private repository access is required or offered. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the public review and test process.

## Prerequisites

- [go.mod](go.mod) declares Go **1.26.0** and selects toolchain **go1.26.6**.
- The checked-in [setup action](.github/actions/setup/action.yml) pins **Node 24**
  and **pnpm 12.14.0**. Use these for the UI commands below.
- A headless build needs no Wails CLI or desktop toolkit; it embeds the web UI.
  Desktop builds additionally need Wails **v2.17.0**, the platform's native build
  tools, and WebKitGTK for Linux. See the [Makefile](Makefile) for desktop targets.

## Build and test

Run these POSIX-shell commands from the repository root in a clean public checkout.
They build the UI before embedding it in the headless binary; they do not start a
daemon or require signing credentials.

```sh
pnpm --dir ui install --frozen-lockfile --ignore-scripts
pnpm --dir ui build
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -o bin/tmxg .
```

A focused Go test and the existing UI unit tests:

```sh
CGO_ENABLED=0 go test -count=1 -timeout=2m -run '^TestHomeFromEnv$' ./internal/config
(cd ui && pnpm test test/*.test.mjs)
```

`TestHomeFromEnv` uses its own temporary directory. For any manual daemon run,
first set `TMXG_HOME` to a fresh directory created with `mktemp -d`, and keep that
same value for shutdown. Never use an existing user's state directory for testing.
Browser E2E tests have additional prerequisites; the unit-test command above does
not run them. The [public CI workflow](.github/workflows/ci.yml) lists the full jobs.

## CI and releases

Public CI uses GitHub-hosted Linux, Windows and macOS runners, not self-hosted
machines. A maintainer may need to approve a first-time contributor's CI run.

The manually dispatched [public release workflow](.github/workflows/release.yml)
builds unsigned headless binaries and uploads build artifacts. This is separate
from a reviewed, signed release: detached release signatures are supplied outside
that workflow. Building or contributing does not require signing secrets.

For an available signed release, follow the checksum and signature verification in
the [installation guide](docs/INSTALL.md) before running downloaded binaries.
The [phone pairing guide](docs/PHONE-PAIRING.md) describes trusted HTTPS access and
owner approvals. `tmxg skill` prints the [included agent skill](internal/skill/SKILL.md).
It documents trial and production feature defaults. Config loading forces
automatic updates off; signed manual updates require owner-configured release trust.

## License

Copyright 2026 blackcode GmbH and contributors. Licensed under the
[Apache License 2.0](LICENSE).

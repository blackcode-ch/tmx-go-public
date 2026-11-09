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

## Install

Use the [Windows, macOS and Linux installation guide](docs/INSTALL.md) for
signed public headless releases. It includes checksum and signature verification
before running the downloaded binary.

For phone access through a configured trusted HTTPS address, see
[phone pairing and owner approvals](docs/PHONE-PAIRING.md).

## Build from source

This repository is a scrubbed public snapshot of Tiamatrix (`tmx`). It can be built and tested
entirely with open-source tools:

- **Go**: 1.26.0 or later (uses Go toolchain `go1.26.6` as specified in [`go.mod`](go.mod)).
- **Node.js**: Node 24 (`v24.x`) and `pnpm` 12.14.0 (as configured in [`.github/actions/setup/action.yml`](.github/actions/setup/action.yml)).
- **CGO**: Pure-Go headless builds require `CGO_ENABLED=0` and no desktop C libraries. Desktop GUI builds require Wails v2.17.0 and platform GUI libraries (such as WebKitGTK on Linux).

```sh
# 1. Install frontend dependencies and build the UI bundle
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build

# 2. Build the headless binary
make headless

# 3. Desktop app (where desktop toolkits are installed)
make tools
make app
```

Source builds need the Go version in `go.mod`, Node and pnpm. Linux desktop
builds also need WebKitGTK. Public headless releases embed the web UI and do
not require a desktop toolkit.

`tmxg skill` prints the [agent skill](internal/skill/SKILL.md), including the
trial and production feature defaults. Automatic updates are forced off on
config load; signed manual updates require owner-configured release trust.

## Running tests

```sh
# Focused package tests (safe on a public checkout)
go test -count=1 ./internal/api
go test -count=1 ./internal/runner/host

# On Windows CI runners:
go test -tags tmx_ci -count=1 ./internal/runner/host

# Frontend unit tests
pnpm --dir ui test test/*.test.mjs
```

## Contributing and safety

Outside contributors fork [blackcode-ch/tmx-go-public](https://github.com/blackcode-ch/tmx-go-public), branch from `main`, and submit pull requests against `main`.

When testing commands that start daemons or mutate local databases, always use throwaway state:
```sh
export TMXG_HOME="$(mktemp -d)"
export TMXG_PORT=0
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for full contribution guidelines, public CI details, release artifact distinctions, and safe local testing procedures.

## License

Copyright 2026 blackcode GmbH and contributors. Licensed under the
[Apache License 2.0](LICENSE).

---
*This is a scrubbed CI snapshot of a private repository, made automatically. Personal and machine
names are replaced, so a few identifiers differ from the private build (the MCP tool `announce_set`
has another name there), and internal docs are left out. Headless release binaries are built from
reviewed main snapshots; the updater accepts them only with a valid fleet release signature.*

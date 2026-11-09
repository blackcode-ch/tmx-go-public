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
public headless releases. It includes checksum and signature verification
before running the downloaded binary.

For phone access through a configured trusted HTTPS address, see
[phone pairing and owner approvals](docs/PHONE-PAIRING.md).

## Build from source

```sh
make tools     # Wails CLI for desktop builds
make           # desktop app where supported; bin/tmxg is the CLI
bin/tmxg up    # start the local daemon
bin/tmxg open  # print the private local UI URL
make help      # available build and test targets
```

Source builds need Go 1.26.0 (toolchain go1.26.6, per `go.mod`), Node 24 and
pnpm 12.14.0. Linux desktop builds also need WebKitGTK. Public headless
releases embed the web UI and do not require a desktop toolkit. A headless-only
build is `make headless`.

`tmxg skill` prints the [agent skill](internal/skill/SKILL.md), including the
trial and production feature defaults. Automatic updates are forced off on
config load; signed manual updates require owner-configured release trust.

## Contributing

This repository is a public snapshot. Outside contributors fork this public
repository, branch from `main`, and open a pull request against public `main`.
See [CONTRIBUTING.md](CONTRIBUTING.md) for the full build, test and
pull-request guide. Public CI runs on GitHub-hosted runners only, and a
maintainer may need to approve a first-time contributor's run.

## Releases

Public release binaries are built by a manually dispatched workflow
(`.github/workflows/release.yml`) that produces unsigned build artifacts;
detached release signatures are supplied outside CI. Reviewed, signed releases
are separate: headless release binaries are built from reviewed `main`
snapshots, and the updater accepts them only with a valid fleet release
signature.

## License

Copyright 2026 blackcode GmbH and contributors. Licensed under the
[Apache License 2.0](LICENSE).

---
*This is a scrubbed CI snapshot of a private repository, made automatically. Personal and machine
names are replaced, so a few identifiers differ from the private build (the MCP tool `announce_set`
has another name there), and internal docs are left out. Headless release binaries are built from
reviewed main snapshots; the updater accepts them only with a valid fleet release signature.*

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

This is an automatically scrubbed public snapshot. Work from this repository;
private source files and private repository access are not needed or offered.
Outside contributors fork this repository, branch from `main`, and open a pull
request against its public `main`. See [CONTRIBUTING.md](CONTRIBUTING.md) for
tests, public CI, Windows commands, and safe use of throwaway state.

Use Go 1.26.6 (`go.mod` declares Go 1.26.0 and selects the 1.26.6 toolchain),
Node.js 24, and pnpm 12.14.0. The Node/pnpm versions are pinned in
[the checked-in setup action](.github/actions/setup/action.yml).

From the repository root, build the web UI before the headless binary so the
binary embeds it:

```sh
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build
CGO_ENABLED=0 go build -trimpath -o bin/tmxg .
```

This headless build needs no Wails CLI or desktop toolkit. To build the native
desktop window instead, install Wails v2.17.0 (`make tools`) and the
[Wails v2 platform prerequisites](https://v2.wails.io/docs/gettingstarted/installation/).
Windows needs WebView2; macOS needs Xcode Command Line Tools; Linux needs the
appropriate compiler and GTK/WebKit development libraries. The Makefile uses
Bash and standard Unix tools (on Windows, Git Bash and GNU Make). `make` defaults
to headless on Linux and desktop on Windows/macOS; `make app` explicitly builds
the desktop app. `make help` lists the targets.

`tmxg skill` prints the [agent skill](internal/skill/SKILL.md), including the
trial and production feature defaults. Automatic updates are forced off on
config load; signed manual updates require owner-configured release trust.

## License

Copyright 2026 blackcode GmbH and contributors. Licensed under the
[Apache License 2.0](LICENSE).

---
*Personal and machine names are replaced in this snapshot, and private documentation is
omitted. Public CI runs on GitHub-hosted runners. The manually dispatched
[public release workflow](.github/workflows/release.yml) builds unsigned headless
binaries and checksums as an expiring Actions artifact; it does not publish or
sign a release. Reviewed releases require a separate signing/publishing step.
Contributors do not need signing secrets. The updater accepts a release only
with a valid configured fleet release signature.*

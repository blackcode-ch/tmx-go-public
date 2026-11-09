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

## Quick start (headless build & throwaway state)

Always use a throwaway `TMXG_HOME` directory and an isolated `TMXG_PORT` when testing or running locally so you never touch an existing daemon's state:

```sh
# 1. Install UI packages and build the embedded React bundle (ui/dist)
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build

# 2. Build the headless binary (bin/tmxg; no CGO or desktop window required)
make headless

# 3. Run against a throwaway state directory (never against an existing user's state)
export TMXG_HOME="$(mktemp -d)"
export TMXG_PORT=18797
./bin/tmxg up
./bin/tmxg status
./bin/tmxg down
rm -rf "$TMXG_HOME"
```

For desktop builds (`make` / `make app`) and full build targets, see [CONTRIBUTING.md](CONTRIBUTING.md) and `make help`.

## Requirements

- **Go:** `1.26.0` (`toolchain go1.26.6` in [go.mod](go.mod)).
- **Node.js & pnpm:** Node `24` and `pnpm` `12.14.0` (pinned in [.github/actions/setup/action.yml](.github/actions/setup/action.yml)).
- **Headless builds (`make headless`, `CGO_ENABLED=0 go build ./...`):** Pure Go (`CGO_ENABLED=0`); no C toolchain or desktop libraries required.
- **Desktop builds (`make app`):** Wails CLI `v2.17.0` (`make tools`), plus platform webview prerequisites (Xcode command-line tools on macOS, WebView2 on Windows, WebKitGTK on Linux).

## Testing

```sh
# Focused Go package test
go test -count=1 ./internal/api

# UI unit tests (ui/test/*.test.mjs)
pnpm --dir ui test test/*.test.mjs
```

## Public CI and releases

- **Public CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)):** Runs entirely on GitHub-hosted runners (`ubuntu-latest`, `windows-latest`, `macos-15`). First-time outside contributors may need a maintainer to approve their workflow run.
- **Public release builds ([.github/workflows/release.yml](.github/workflows/release.yml)):** Manual `workflow_dispatch` on `blackcode-ch/tmx-go-public` builds **unsigned** four-platform binaries and SHA-256 checksums as workflow artifacts (`cmd/public-release -mode build`). Contributors do **not** need signing keys or certificates. Reviewed, signed releases require detached signatures supplied outside CI (`internal/update`).

## Architecture & intent

- [INTENT.md](INTENT.md) documents the product intent, architecture, and operating model.
- [CONTRIBUTING.md](CONTRIBUTING.md) covers building, testing, and submitting pull requests to this public repository.

---
*This repository (`blackcode-ch/tmx-go-public`) is an automatic, scrubbed public snapshot of the private repository. Personal and machine names are replaced and internal-only documents are omitted. Outside contributors work directly in this public repository by forking `blackcode-ch/tmx-go-public`, branching from `main`, and opening a pull request against `main`.*
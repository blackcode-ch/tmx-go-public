# Contributing to `tmx-go-public`

## Overview of this repository

`blackcode-ch/tmx-go-public` is an automatically scrubbed public snapshot of the `tmx-go` codebase. Internal operational docs and private machine identifiers are omitted from the public tree.

Outside contributors work **entirely within this public repository**:
1. Fork `https://github.com/blackcode-ch/tmx-go-public`.
2. Create a topic branch from current `main`.
3. Open a pull request against `blackcode-ch/tmx-go-public` `main`.

No private repository access or signing secrets are needed or offered.

## Toolchain & prerequisites

| Tool | Version | Source in repository |
|---|---|---|
| Go | `1.26.0` (`toolchain go1.26.6`) | [go.mod](go.mod) |
| Node.js | `24` | [.github/actions/setup/action.yml](.github/actions/setup/action.yml) |
| pnpm | `12.14.0` | [.github/actions/setup/action.yml](.github/actions/setup/action.yml) |
| Wails CLI *(desktop only)* | `v2.17.0` | [Makefile](Makefile) (`make tools`) |

### Headless vs. desktop prerequisites
- **Headless builds (`make headless` or `CGO_ENABLED=0 go build ./...`):** Pure Go (`CGO_ENABLED=0`) plus the built React UI (`ui/dist`). No C compiler, CGO, or system GUI libraries are needed.
- **Desktop builds (`make app` / `make app-windows`):** Require the Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0` via `make tools`) and native webview headers on Linux (`WebKitGTK`). On macOS, public/local builds use ad-hoc signing (`SIGN_IDENTITY="-"`).

## Copyable build and test commands

### 1. Headless build
```sh
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build
make headless
```
This produces `bin/tmxg` (or `bin/tmxg.exe` on Windows) with `CGO_ENABLED=0`.

*(Note: `go build ./...` compiles all Go packages directly using the committed `ui/dist` placeholder without rebuilding the UI first.)*

### 2. Focused Go test
```sh
go test -count=1 ./internal/api
```
On Windows, include the `tmx_ci` build tag when running guard/host tests as in CI:
```sh
go test -tags tmx_ci -count=1 ./internal/runner/host
```

### 3. Existing UI tests
```sh
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui test test/*.test.mjs
```
You can also run the UI linter and TypeScript checks:
```sh
pnpm --dir ui lint
pnpm --dir ui typecheck
```

## Safe local execution (always use throwaway state)

**Never start a daemon against an existing user's state directory.** By default, `tmxg` resolves its state directory from `TMXG_HOME` (falling back to `~/.local/state/tmxg-trial`, `~/Library/Application Support/tmxg-trial`, or `%LOCALAPPDATA%\tmxg-trial`; see [internal/config/paths.go](internal/config/paths.go)) and its port from `TMXG_PORT` (default `8797`; see [internal/config/config.go](internal/config/config.go)).

Whenever you run a command that touches daemon state, explicitly create and clean up a throwaway directory:

```sh
export TMXG_HOME="$(mktemp -d)"
export TMXG_PORT=18797
./bin/tmxg status || true
rm -rf "$TMXG_HOME"
```

## Public CI and releases

### Public CI ([.github/workflows/ci.yml](.github/workflows/ci.yml))
- All jobs (`runner-lock`, `smoke`, `go-linux`, `go-windows`, `race`, `bench`, `ui-and-e2e`, `macos`) run on **GitHub-hosted runners** (`ubuntu-latest`, `windows-latest`, `macos-15`).
- Self-hosted runners are forbidden in `.github/workflows/` and enforced by `runner-lock` ([.github/scripts/check_tmx_ci_runners.py](.github/scripts/check_tmx_ci_runners.py)).
- First-time contributors may need a maintainer to approve their public CI workflow run.

### Unsigned build artifacts vs. signed releases ([.github/workflows/release.yml](.github/workflows/release.yml))
- `.github/workflows/release.yml` is a manual `workflow_dispatch` workflow that runs `go run ./cmd/public-release -mode build` ([cmd/public-release/main.go](cmd/public-release/main.go)) to produce **unsigned** four-platform binaries and checksums as a workflow artifact (`fleet-release-binaries`).
- Contributors never need signing keys, certificates, or secrets.
- Reviewed, signed releases require detached signatures generated outside CI (`tmx release sign` / `tmx release sign-file`) and verified against pinned keys in `internal/update`.

## Pull request checklist

1. Keep changes focused on the target issue and its tests.
2. Format Go files with `gofmt` and ensure `go vet ./...` passes.
3. Verify all local Markdown links in `README.md` and `CONTRIBUTING.md` resolve to files present in this public tree.
4. Open a pull request against `blackcode-ch/tmx-go-public` `main` describing the change, the commands/tests run, and links to public CI runs.
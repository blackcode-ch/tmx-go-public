# Contributing to the public snapshot

Fork `blackcode-ch/tmx-go-public`, create a branch from current `main`, and open
a pull request against this repository's `main`. This is an automatic scrubbed
snapshot: private files, internal documentation and private repository access
are not part of the contributor workflow. Do not assume a private integration
process. Keep changes focused and describe the checks you actually ran.

## Toolchain and headless build

Install Git, Go 1.26.6, Node.js 24 and pnpm 12.14.0. Check with `go version`,
`node --version` and `pnpm --version`. [go.mod](go.mod) declares Go 1.26.0 and
selects toolchain go1.26.6; [.github/actions/setup/action.yml](.github/actions/setup/action.yml)
pins Node 24 and pnpm 12.14.0. Do not change the lockfile just to install packages.

From the root of a clean public checkout:

```sh
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build
CGO_ENABLED=0 go build -trimpath -o bin/tmxg .
```

In Windows PowerShell, use the same pnpm commands, then:

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -o bin/tmxg.exe .
```

`ui/embed.go` embeds `ui/dist`: rebuild the Go binary after changing/building
the UI. No desktop toolkit or Wails CLI is needed for this headless build.
For desktop builds, see the [README prerequisites](README.md#build-from-source)
and [Makefile](Makefile); Wails is pinned to v2.17.0.

## Tests

From the repository root, these commands do not start the daemon:

```sh
go test -tags tmx_ci -run '^TestWaitHostGoneBounded$' -count=1 ./internal/runner/host
pnpm --dir ui test
```

The first command is a focused Go test using stubbed process liveness and a fake
clock; the second runs the existing Node UI tests from `ui/package.json`.
For UI changes, also run `pnpm --dir ui typecheck` and `pnpm --dir ui build`.

The wider suite runs `go build ./...`, `go vet ./...` and
`go test -count=1 -timeout=15m ./...`. Windows CI adds `-tags tmx_ci` to vet/tests
and separately tests `-tags=aclmutate` in `./internal/config/...`.
Public CI sets `TMX_PUBLIC_SNAPSHOT=1` so checks for omitted private files skip.
Those wider guard/process/ACL and end-to-end checks belong on disposable
GitHub-hosted runners, not a machine with an operator's existing state.
See [.github/workflows/ci.yml](.github/workflows/ci.yml) for exact job commands.

## Keep runtime state separate

Never run `tmxg up`, `daemon`, `open` or end-to-end tests against an existing
user's state. The build and focused tests above need no daemon. If a manual
runtime check is needed, first assign `TMXG_HOME` to a new temporary directory:

```sh
export TMXG_HOME="$(mktemp -d)"
./bin/tmxg version
```

Windows PowerShell equivalent:

```powershell
$env:TMXG_HOME = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $env:TMXG_HOME | Out-Null
.\bin\tmxg.exe version
```

Keep that same throwaway `TMXG_HOME` for every command in the check. If you
explicitly start its daemon, stop it with `tmxg down` using the same state before
finishing. Do not share owner tokens, local UI URLs, recordings or state files.

## Pull requests and CI

Include the problem, change, commands/results and any reproducible baseline
failures in the PR description. Link the issue being addressed. Public CI uses
GitHub-hosted Linux, Windows and macOS runners; a maintainer may need to approve
a first-time contributor's workflow run. Leave normal public checks intact.
No private credentials, signing secrets or self-hosted runners are needed.

[.github/workflows/release.yml](.github/workflows/release.yml) is manually
dispatched on public `main`. It produces unsigned binaries/checksums in the
`fleet-release-binaries` Actions artifact (14-day retention), not a published,
signed release. A reviewed release needs separate signing and publication.

For documentation changes, resolve local links relative to the containing file
and check that the files exist in this public tree. External links deliberately
point to the public repository/release pages or official tooling documentation.

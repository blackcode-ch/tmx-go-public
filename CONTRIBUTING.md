# Contributing to tmx (public snapshot)

This repository is a scrubbed public snapshot of a private repository, made
automatically. Work only from this public repository; no private repository
access is needed or offered. Outside contributors fork this public repository,
branch from `main`, and open a pull request against public `main`.

## Toolchain

- Go 1.26.0 (toolchain go1.26.6), per `go.mod`
- Node 24
- pnpm 12.14.0

These are the versions used by public CI (see `.github/workflows/release.yml`
and `.github/actions/setup/action.yml`). Use them so your local build matches
what CI runs.

## Build

A headless build embeds the web UI and does not require a desktop toolkit:

```sh
make headless
```

`make` builds the one binary for the machine: headless on Linux, and the
desktop app on macOS/Windows. To build the desktop app explicitly:

```sh
make app
```

A desktop build needs the Wails CLI and, on Linux, WebKitGTK:

```sh
make tools     # install the Wails CLI
make app       # desktop app where supported; bin/tmxg is the CLI
```

Run `make help` for all build and test targets.

## Test

Run a focused Go test:

```sh
go test -run 'TestGzip|TestGzipped' -count=1 ./internal/api
```

Run the full Go test suite:

```sh
make test
```

Run the UI tests:

```sh
pnpm --dir ui test test/*.test.mjs
```

Run lint, typecheck, vet, tests and a build together:

```sh
make check
```

## Pull requests

1. Fork this public repository and create a branch from current `main`.
2. Make your change and add or update tests for it.
3. Open a pull request against public `main`. Include a short explanation, the
   tests you ran, and links to public CI evidence where relevant.
4. Do not promise private access or describe an unverified private integration
   process. Do not add private credentials or self-hosted runners.

## Public CI

Public CI (`.github/workflows/ci.yml`) runs on GitHub-hosted runners only
(never self-hosted). It runs on pushes to `main` and on pull requests against
`main`. A maintainer may need to approve a first-time contributor's run before
it starts. Outside pull requests get no secrets.

## Releases

Public release binaries are built by a manually dispatched workflow
(`.github/workflows/release.yml`) that produces unsigned build artifacts;
detached release signatures are supplied outside CI. Reviewed, signed releases
are separate from these unsigned artifacts: headless release binaries are built
from reviewed `main` snapshots, and the updater accepts them only with a valid
fleet release signature.

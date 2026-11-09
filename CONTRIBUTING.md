# Contributing to tmx (public snapshot)

Thank you for contributing to `tmx`!

`blackcode-ch/tmx-go-public` is an automatic, scrubbed public snapshot of the `tmx` codebase. Outside contributors work entirely within this public repository without needing access to private repositories, private credentials, or internal signing keys.

---

## 1. Prerequisites

- **Go**: `1.26.0` or later (matching [`go.mod`](go.mod), toolchain `go1.26.6`).
- **Node.js & pnpm**: Node `24` and `pnpm` `12.14.0` (matching [`.github/actions/setup/action.yml`](.github/actions/setup/action.yml)).
- **Build tool**: `make` (see [`Makefile`](Makefile)).

---

## 2. Public Build Commands

### Headless binary (Pure Go, no CGO or GUI toolkits)
```sh
# Install UI dependencies and build the static UI dist
pnpm --dir ui install --frozen-lockfile
pnpm --dir ui build

# Build the pure-Go headless binary (CGO_ENABLED=0)
make headless
```
The compiled headless binary is placed at `bin/tmxg` (or `bin/tmxg.exe` on Windows).

### Desktop application
Desktop builds require Wails v2.17.0 (`make tools`) and platform desktop dependencies (WebKitGTK on Linux).
```sh
make tools
make app
```

---

## 3. Testing Commands

### Focused Go Package Tests
Run focused package tests for your area of change:
```sh
go test -count=1 ./internal/api
go test -count=1 ./internal/runner/host
```
On Windows environments:
```sh
go test -tags tmx_ci -count=1 ./internal/runner/host
```

### UI Unit Tests
```sh
pnpm --dir ui test test/*.test.mjs
```

---

## 4. Throwaway State Safety

Local commands must never pollute or conflict with an existing user's local daemon state or database:

- Set a clean temporary directory for `TMXG_HOME`:
  ```sh
  export TMXG_HOME="$(mktemp -d)"
  ```
  (Referenced in [`internal/config/paths.go`](internal/config/paths.go)).
- Use an ephemeral port for testing:
  ```sh
  export TMXG_PORT=0
  ```
  (Referenced in [`internal/config/config.go`](internal/config/config.go)).

---

## 5. Public CI & Pull Request Workflow

1. Fork https://github.com/blackcode-ch/tmx-go-public to your GitHub account.
2. Create a feature branch from the latest public `main`.
3. Open a Pull Request targeting `main`.
4. Tests run on GitHub-hosted runners (`ubuntu-latest`, `windows-latest`, `macos-15`) per [`.github/workflows/ci.yml`](.github/workflows/ci.yml). Maintainers approve public CI runs for first-time contributors.
5. Release binaries in public CI are built unsigned via [`cmd/public-release/main.go`](cmd/public-release/main.go). Signed releases use detached signatures generated outside the public CI pipeline.

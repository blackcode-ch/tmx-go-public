# Contributing

Fork [blackcode-ch/tmx-go-public](https://github.com/blackcode-ch/tmx-go-public), create a branch from current `main`, and open a pull request against public `main`. Work in this public repository. Private repository access is outside this process.

`main` is an automatic public snapshot. Snapshot commits record `Private-Repo` and `Private-Commit` trailers. Documents named in source comments, including `DESIGN.md` and `docs/RECOVERY.md`, are not in this tree. Installing a signed public binary is [docs/INSTALL.md](docs/INSTALL.md). That guide's `up` and `open` steps use the default state directory. The commands below do not.

## What you need

- Go `1.26.0`, toolchain `go1.26.6`, from [go.mod](go.mod).
- Node `24` and pnpm `12.14.0`, from [.github/actions/setup/action.yml](.github/actions/setup/action.yml).

Go, Node, and pnpm are enough for a headless build and for the tests below. A desktop window also needs the Wails CLI `v2.17.0`: the Makefile target `tools` installs it, and the target `app` builds the window ([Makefile](Makefile)). On Linux, plain `make` is the headless binary. On macOS and Windows, plain `make` is the desktop app. Public CI builds that macOS app unsigned on `macos-15`. Contributor builds stay unsigned.

## Build and test

Run these from a clean checkout, at the repository root, in bash. They leave any existing state directory alone and do not start a daemon.

`TMXG_HOME`, when set, is the state directory ([internal/config/paths.go](internal/config/paths.go)). Leave it unset for the build and the tests. The version line below sets it to a new empty directory from `mktemp` and does not start a daemon. Run `up`, `daemon`, or `app` only when `TMXG_HOME` is a new empty directory you can delete. The default listen port is `8797`.

```sh
make headless
go test -count=1 -timeout=15m ./internal/config/
cd ui
pnpm install --frozen-lockfile
pnpm test test/*.test.mjs
cd ..
TMXG_HOME="$(mktemp -d)" ./bin/tmxg version
```

`make headless` writes `bin/tmxg` with `CGO_ENABLED=0` after `pnpm install --frozen-lockfile` and `pnpm build` in `ui`. The Go line is one package, with the same `-count=1 -timeout=15m` flags public CI uses for `./...`. The `pnpm test` script is `node --experimental-strip-types --test` ([ui/package.json](ui/package.json)); `test/*.test.mjs` selects `ui/test`.

## Pull requests

[public CI](.github/workflows/ci.yml) runs on GitHub-hosted runners. A maintainer may need to approve the first run from your account before those jobs start. In the pull request, say what changed, which commands you ran, and link the public CI run. Keep secrets and self-hosted runners out of the change.

## Releases

Contributor changes leave release signing alone. [.github/workflows/release.yml](.github/workflows/release.yml) is a manual dispatch that uploads unsigned binaries as an Actions artifact. Reviewed signed releases are produced outside that workflow and installed from [docs/INSTALL.md](docs/INSTALL.md). Details are in [README.md](README.md). The Makefile targets `release-key` and `signing-cert` call scripts that are absent from this tree.

## License

[Apache License 2.0](LICENSE).

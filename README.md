<p align="center">
  <img src="build/appicon.png" alt="tmx icon" width="112" height="112">
</p>

<h1 align="center">tmx</h1>

<p align="center">
  One binary, <code>tmxg</code>, runs a tree of coding agents on this machine.
</p>

The built program is `bin/tmxg` (`.exe` on Windows). It is the daemon, the CLI, and the MCP server. A desktop build of the same program also opens a window. The CLI's help text still calls the program `tmx`.

`main` on this repository is an automatic public snapshot. Snapshot commits record `Private-Repo` and `Private-Commit` trailers. Source comments name documents that are not in this tree (for example `DESIGN.md` and `docs/RECOVERY.md`). Legacy MCP tool names from the private build are not registered here; this tree registers `announce_set` ([internal/mcp/project_sections.go](internal/mcp/project_sections.go)). Outside contributors fork [blackcode-ch/tmx-go-public](https://github.com/blackcode-ch/tmx-go-public), branch from `main`, and open a pull request against public `main`. Private repository access is outside that process.

Build, test, and pull-request steps: [CONTRIBUTING.md](CONTRIBUTING.md). Signed public headless releases are installed from [docs/INSTALL.md](docs/INSTALL.md). Phone pairing over a trusted HTTPS address is [docs/PHONE-PAIRING.md](docs/PHONE-PAIRING.md). The install guide's `up` and `open` steps use the default state directory; the commands on this page do not start a daemon.

## Toolchain

[go.mod](go.mod) declares Go `1.26.0` and toolchain `go1.26.6`. [.github/actions/setup/action.yml](.github/actions/setup/action.yml) installs Node `24` and pnpm `12.14.0`.

`make headless` ([Makefile](Makefile)) is the headless build. It installs the UI from `ui/pnpm-lock.yaml`, builds `ui/dist`, and compiles with `CGO_ENABLED=0`. Go, Node, and pnpm are the whole toolchain for that target. The binary is `bin/tmxg`.

A desktop window is separate. The Makefile target `tools` installs the Wails CLI `v2.17.0`, and the target `app` builds the window. On macOS and Windows, `make` runs `app`. On Linux, `make` runs `headless`. Public CI builds the macOS app unsigned (`SIGN_IDENTITY=-`) on `macos-15` ([.github/workflows/ci.yml](.github/workflows/ci.yml)). Contributor builds stay unsigned. The targets `release-key` and `signing-cert` call scripts that are absent from this tree.

## Commands

These commands leave any existing state directory alone. They do not start a daemon. Run `bin/tmxg up`, `bin/tmxg daemon`, or `bin/tmxg app` only with `TMXG_HOME` set to a new empty directory. When `TMXG_HOME` is unset, state goes to `~/.local/state/tmxg-trial` on Linux, `~/Library/Application Support/tmxg-trial` on macOS, or `%LOCALAPPDATA%\tmxg-trial` on Windows ([internal/config/paths.go](internal/config/paths.go)). The listen port defaults to `8797` (`TMXG_PORT` overrides it). Ports `8777`, `8787`, `43911`, and `4001` are reserved.

```sh
make headless
go test -count=1 -timeout=15m ./internal/config/
cd ui
pnpm install --frozen-lockfile
pnpm test test/*.test.mjs
cd ..
TMXG_HOME="$(mktemp -d)" ./bin/tmxg version
```

Run that block from the repository root in bash (Git Bash on Windows), as one script, so `cd ui` and `cd ..` stick. `pnpm test` is `node --experimental-strip-types --test` ([ui/package.json](ui/package.json)). Passing `test/*.test.mjs` matches the Makefile and runs the files in `ui/test`. The last line prints the version and does not start a daemon; `TMXG_HOME` is a new empty directory.

`make headless` already installs and builds the UI, so the `pnpm install` line is only there so the UI tests can be run on their own.

`tmxg skill` prints [internal/skill/SKILL.md](internal/skill/SKILL.md). Loading config forces automatic updates off. Signed manual updates need owner-configured release trust, as that file describes.

## Public CI

[public CI](.github/workflows/ci.yml) runs on GitHub-hosted runners (`ubuntu-latest`, `windows-latest`, `macos-15`). A maintainer may need to approve a first-time contributor's run. Pull requests from forks receive no secrets.

## Releases

[.github/workflows/release.yml](.github/workflows/release.yml) (`public release binaries`) runs only when someone dispatches it by hand, and only for `blackcode-ch/tmx-go-public` on `main`. It builds unsigned headless binaries (`go run ./cmd/public-release -mode build`) and uploads the Actions artifact `fleet-release-binaries`, kept for 14 days.

A reviewed, signed release is a later step outside that workflow. [cmd/public-release](cmd/public-release/main.go) publishes only when it is given an already-made `SHA256SUMS.sig`. [docs/INSTALL.md](docs/INSTALL.md) is how to verify and install those signed binaries. The updater rejects a release that has no signature. Contributor setup includes no signing secrets.

## License

[Apache License 2.0](LICENSE).

---
*This is a scrubbed CI snapshot of a private repository, made automatically.*

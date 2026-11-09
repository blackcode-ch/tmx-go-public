# Contributing to the public snapshot

This repository is generated automatically from a scrubbed snapshot. Use only the
files available here; do not copy private documentation or request private access.
These instructions describe public contributions, not an assumed private
integration process.

## Start a change

1. Fork [blackcode-ch/tmx-go-public](https://github.com/blackcode-ch/tmx-go-public).
2. Clone your fork, create a branch from current public `main`, and keep the change
   within the issue's scope. If `main` advances, recheck the affected instructions
   and tests before submitting.
3. Open a pull request against `blackcode-ch/tmx-go-public:main`, link the issue,
   explain the change and include the exact commands, results and logs.

A bounty comment or an open PR does not reserve a prize. Follow the specific
issue's acceptance and payment terms; passing CI alone is not maintainer approval.

## Build and checks

Use Go **1.26.0** or newer with toolchain **go1.26.6** as declared in
[go.mod](go.mod), and Node **24** with pnpm **12.14.0** from the
[setup action](.github/actions/setup/action.yml). The headless build does not need
Wails, a native desktop toolkit or signing credentials. Desktop requirements and
targets are separate in the [Makefile](Makefile).

From the repository root, in a clean checkout using a POSIX shell:

```sh
pnpm --dir ui install --frozen-lockfile --ignore-scripts
pnpm --dir ui build
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -o bin/tmxg .
CGO_ENABLED=0 go test -count=1 -timeout=2m -run '^TestHomeFromEnv$' ./internal/config
(cd ui && pnpm test test/*.test.mjs)
```

The focused Go test uses temporary state. The UI command runs Node unit tests from
[ui/package.json](ui/package.json), not the browser E2E suite. Match additional
checks to the change; the [public CI workflow](.github/workflows/ci.yml) is the
source for full Go, race, UI, desktop and E2E jobs. Do not claim a check ran unless
you have its output. Include failed or skipped checks and their reason in the PR.

Builds and these tests do not require a running daemon. If manual testing needs
one, set `TMXG_HOME` to a new throwaway directory first (`mktemp -d` in a POSIX
shell), and use that same value to stop it afterward. Never run a test daemon
against existing user state or attach credentials from an installed user instance.

## Public CI and review

Public PRs run on GitHub-hosted runners. Maintainer approval may be needed before
CI runs for a first-time contributor; an unstarted job is not a passing check.
Keep credentials, signing keys and private files out of the PR and its logs.
Contributors do not need private runners or secrets.

For documentation changes, check every retained or added local Markdown link and
HTML image path against the public tree. Check `#fragment` links against the
target headings too. Report the check and list intentionally external links in
the PR; do not replace missing local files with speculative private URLs.

The [release workflow](.github/workflows/release.yml) is manually dispatched and
produces unsigned build artifacts. Those artifacts are not, by themselves,
reviewed signed releases. Detached signing and release review happen outside
that workflow; neither is a prerequisite for an ordinary public contribution.

Contributions are under the repository's [Apache License 2.0](LICENSE).

<p align="center">
  <img src="build/appicon.png" alt="tmx icon" width="112" height="112">
</p>

<h1 align="center">tmx</h1>

<p align="center">
  Your coding agents, run as a team on your own machine.
</p>

<p align="center">
  <img src="docs/images/matrix.png" alt="tmx's Matrix: every thread on a canvas, one string per project; a thread open on the right with the head's turns on it, its state and its events" width="100%">
</p>

One binary that runs a tree of coding agents on your machine: a head agent, a lead per project, and
workers under each lead. The same binary is the daemon, the CLI, the MCP server and the desktop app.
It also has peer-to-peer chats with other people's tmx (QR invites, DMs, groups), a link to
Companion, and a Board: the head's conversation as strings of cards, one string per topic.

```sh
make tools   # once: the Wails CLI (for the desktop window)
make         # build/bin/tmx.app on macOS; bin/tmx is the CLI
bin/tmx up   # start the daemon (127.0.0.1:8787), then open the app or the URL it prints
make help    # every target: headless, dist (all platforms), test, check, ...
```

Needs Go 1.26+, Node with pnpm, and on Linux desktop builds WebKitGTK (headless builds need nothing).
CI (GitHub Actions, on a self-hosted Mac set up with `scripts/ci-runner.sh`) tests and builds every merge to `main`
and publishes the binaries as the rolling `latest` pre-release; a `v*` tag publishes them as a versioned GitHub Release.
AI agents (Claude Code, Codex, ...): `tmx skill` prints a skill file for setting tmx up on a server, joining chats
and using the CLI (`tmx skill install` puts it in `~/.claude/skills/tmx`).
Release builds update themselves from the signed GitHub releases once no agent is busy (`tmx update`,
Settings → Updates); `make release-key` sets up the signing key once, and `make signing-cert` the macOS
code-signing certificate (so macOS keeps tmx's microphone and screen permissions across updates).
[DESIGN.md](DESIGN.md) is the design contract: process model, API, MCP tools, safety, chats.

For phone access through a configured trusted HTTPS address, see
[phone pairing and owner passkeys](docs/phone-pairing.md).

---
*This is a scrubbed CI snapshot of a private repository, made automatically. Personal and machine
names are replaced, so a few identifiers differ from the private build (the MCP tool `announce_set`
has another name there), internal docs are left out, and it is not a release source.*

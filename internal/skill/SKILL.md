---
name: tmxg
description: Inspect and use the isolated tmxg fleet trial, its local coding agents and their MCP tools. External channels are disabled.
---

# tmxg fleet trial

The fleet build uses the trial profile, loopback API port 8787 and a separate
`tmxg-trial` state directory. `TMXG_HOME` explicitly overrides the state directory.
The executable and MCP server are `tmxg`; Python tmx remains separate.

P2P, DHT, mDNS, NAT port mapping, WhatsApp, Companion and automatic updates are
forced off when loading trial config, even if an older config enabled them.
There are no background update checks. Do not enable external channels or
replace the production control plane as part of a trial.

Agents use their `mcp__tmxg__*` tools. Owner CLI commands act with the owner's
credential and require the owner's request. Never print or copy credentials.
Discover and adopt refuse outside processes without scanning or signalling them.
Claude loads only the generated Go settings and hooks. Codex disables the
inherited Python `tmx` MCP entry and receives a distinct `tmxg` entry.

For an already qualified trial, useful owner commands are:

```sh
tmxg version
tmxg status
tmxg ls
tmxg approvals
tmxg help
```

`make headless` produces `bin/tmxg` (with `.exe` on Windows); `make dist` produces
`dist/tmxg_<version>_<os>_<arch>`. Installation and lifecycle execution require
the disposable-VM qualification described in `TRIAL.md`: egress capture, ACL
comparison and proof that Python's agents, hooks and ports remain unaffected.

The trial state directory is `%LOCALAPPDATA%/tmxg-trial` on Windows,
`~/Library/Application Support/tmxg-trial` on macOS, and
`~/.local/state/tmxg-trial` on Linux. Read bounded tails of its logs when
troubleshooting. Never expose the loopback API publicly.

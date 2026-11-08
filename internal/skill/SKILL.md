---
name: tmxg
description: Inspect and use the tmxg fleet, its local coding agents, owner approvals and optional external channels.
---

# tmxg fleet

The executable and MCP server are `tmxg`; Python tmx remains separate. The
default API port is 8797 (`TMXG_PORT` overrides it). `TMXG_HOME` overrides the
state directory. Do not replace another installation without the owner's request.

## Profiles and feature defaults

An unspecified profile is `trial`. Loading trial config forces P2P, DHT, mDNS,
NAT port mapping, Companion and WhatsApp off. The `production` profile honors
the owner's external-channel settings; it does not enable them all by default.

| Feature | Current behavior |
| --- | --- |
| P2P chats and files | Off by default. Production can enable peer-to-peer contacts, direct/group chats and file attachments. When production P2P is enabled, DHT is effectively on unless the owner explicitly disables it. mDNS and NAT port mapping default off. |
| Companion | Off by default; production honors the enabled setting and requires a connection setup. |
| WhatsApp | Off by default; production honors the enabled setting and requires a linked account. Agent sending defaults to asking for approval. |
| Phone access and approvals | Requires a configured trusted HTTPS address and an owner-paired browser. The phone pairs with the owner PIN or a code approved at the host. Owner actions use the PIN; pairing does not expose the local listener or configure networking. |
| Updates | Signed fleet releases and the updater are available. Config loading forces automatic updates off in both profiles, including when a saved config says on. With auto off there are no background release checks. Manual checks and owner-gated installs remain available when release trust is configured. |

Public headless releases come from `blackcode-ch/tmx-go-public`. The updater
requires pinned fleet signing keys, a valid release signature and matching
checksums. Set up trust through the owner-protected flow; do not trust a key
obtained only from the candidate download. Unconfigured update settings still
resolve to `blackcode-ch/tmx-go`, stable channel and a six-hour check interval
(the interval does not enable background checks). Do not claim that public
release availability changes those defaults.

## Agent and owner boundaries

Agents use their `mcp__tmxg__*` tools. Owner CLI commands act with the owner's
credential and require the owner's request. Never print or copy credentials,
owner PINs, pairing codes or private bootstrap links into agent messages.
Discover and adopt refuse outside processes without scanning or signalling them.
Claude loads only the generated Go settings and hooks. Codex disables the
inherited Python `tmx` MCP entry and receives a distinct `tmxg` entry.

Useful owner commands are:

```sh
tmxg version
tmxg status
tmxg ls
tmxg approvals
tmxg help
```

`make headless` produces `bin/tmxg` (with `.exe` on Windows); `make dist` produces
`dist/tmxg_<version>_<os>_<arch>`. See the public
[installation guide](../../docs/INSTALL.md) and
[phone pairing guide](../../docs/PHONE-PAIRING.md).

The default state directory is `%LOCALAPPDATA%/tmxg-trial` on Windows,
`~/Library/Application Support/tmxg-trial` on macOS, and
`~/.local/state/tmxg-trial` on Linux, including for production unless overridden.
Read bounded tails of its logs when troubleshooting. Keep the local API private.
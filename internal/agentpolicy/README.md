# Agent policy

This package performs no process, filesystem or network operations. Launch admission
must call `CheckLaunch` before spawn, with the canonical harness name and the host's
actual OS. Both Claude modes use `claude`. On Windows every other harness requires
an enforced sandbox. The current Windows Codex `danger-full-access` runner does not
meet that condition. A request asking for a sandbox is not evidence of isolation.

`CheckCommand` refuses browser binaries, Playwright/Puppeteer, CDP calls and remote
debugging on Windows by default. The command/tool dispatcher must call it before
dispatch, including MCP browser tools. `AllowBrowserAutomation` is a trusted host
capability, never an agent-supplied field. It does not grant any other permission.
The Claude PreToolUse guard checks shell commands and browser tool names with an
all-tools matcher. For a browser refusal, the hook reads the current capability
from the authenticated, read-only `/api/guard/policy` route. Lookup failures deny;
the flag is not taken from the agent environment. The owner sets
`guard.allow_browser_automation` using the existing owner-only guard settings API;
enabling it is a weakening change requiring the owner gate. Permission does not
override other guard rules. P4 must require an active guard for Windows Claude.

This conservative text check is not containment for encoded or indirect code;
the execution guard or sandbox is still required. The P4 launch admission change
owns launch/spawn integration. These checks do not provide OS-level process
containment; unguarded non-Claude Windows harnesses must remain refused.

`FilterEnv` denies credentials case-insensitively on all platforms, preserving only
the child's own `TMXG_AGENT_TOKEN`. The shared runner environment builder drops the
inherited parent agent token in `BaseEnv`, then filters again after `MergeEnv` adds
the child's fresh token. All three existing runners use that builder. The filter
also drops malformed entries and inherited credential-agent sockets. It leaves
ordinary entries such as PATH, HOME and TERM intact. It is not a filesystem secret
isolation boundary and cannot identify an arbitrarily named secret variable.

Run the pure policy tests with `go test ./internal/agentpolicy` and the environment
regressions with `go test ./internal/runner/claude -run TestAgentEnv`.

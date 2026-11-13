# INTENT.md: what Tiamatrix (tmx-go) is for and how it is meant to behave

Version 1, 3 November 2026. Read against `fleet/main` at `00d1d85`. The code locations below come from reading
the source only. Nothing was built or run to write this, so "Built" means the code exists, not that it was
accepted on a real device.

This is the canonical intention document. A fresh agent should be able to take it and find wherever the code does
something it should not do, fails to do something it should, or carries code no feature needs ("trash code").
Where the owner's intent is not written down anywhere, this file says **TO CONFIRM WITH OWNER**. It does not guess.
Decisions taken after v1 are marked with who decided them and when.

---

## 1. Purpose and who it is for

**What it is.** One program (`tmx`; the fleet build is named `tmxg`) that runs a team of AI coding agents on your
own machines and lets you direct them from your desktop or your phone. Agents are organised like a company: a
head per machine, a lead per project, workers under each lead. The same binary is the daemon, the CLI, the MCP
server agents talk to, and the desktop app (`README.md`, `DESIGN.md` "What it is").

**Who it is for, in order:**

1. **the owner first.** the owner runs a fleet of agents on several personal machines (today: Hostc, Hosta,
   Hostb) and needs to direct them, answer them and trust them from whichever device is at hand, mostly the
   phone. Every decision is judged first by "does this help the owner get work done safely today?"
2. **Then the team.** Colleagues and invited people who run their own tmx and work with the owner's agents in shared
   chats, groups and threads, each keeping their own owner boundary.
3. **Then the public product.** Anyone downloads Tiamatrix from a public site (tiamatrix.com or .swiss: not
   decided), installs it on Windows, Mac or Linux, gets the phone app from a store, and joins a network in under
   ten minutes without help. Outside workers take paid last-mile tasks through Task Hive.

**Why this exists (the owner, 3 Nov 11:03):** "AI alone = mess, human alone = nothing, AI + human = productivity",
proved first on our own projects.

**The ladder** (the owner, 3 Nov 11:03: every step is in daily use as soon as it lands; no step waits for the next):

| Step | What works |
|---|---|
| S1 | Go replaces Python on one desktop (Hostb first) |
| S2 | Go between desktops; then our own mesh, Tailscale kept only as the fallback |
| S3 | Phone + desktops: PIN pairing, biometric unlock, the phone as a recorder |
| S4 | Downloadable from the public site, signed builds, onboarding by ID/QR |
| S5 | Android app in the Play Store (iOS later) |
| S6 | Outsiders: contacts, per-contact and per-group switches, injection-proof, their data kept apart |
| S7+ | The rest: complete Matrix, wrap-up agent, Your-turn redesign, voice control, files awareness, iOS |

The dated plan for each step lives in the end-state page (tmx-lead's promises). This file holds the intent, not
the dates.

---

## 2. The two kinds of rules (and only these two)

The owner, 3 Nov 13:23: "The only rules I care about are the rules that protect the machines. I've said it 50
times. I care about progress." 13:24: "and the coherence of the project ofc, we can't go all lord of the flies
here." Motto, 13:25: "Win at all cost, IAO (improvise, adapt, overcome), but don't turn into a band of pigs or
destroy the PC."

### Rule set 1: protect the machines and the owner's access to them

A restriction belongs in the code only if it protects:

- **the hardware, disks, partitions, boot and OS** (no root deletes, no ACL changes on drive roots or system
  folders, no elevation, no firewall changes, no damage to the Codex sandbox; see `internal/fleet/authority.go`
  `machineProtection`), or
- **the owner's access** (no lockout: login must survive updates and restarts, wrong PINs slow down but never lock
  out, Tailscale is not removed before the replacement is proven).

Also counted under rule set 1, because they protect the machine from the network: an outsider's message, web
page or file cannot give orders (section 4.10), and a secret's value never leaves its vault into chat, logs or
links.

### Rule set 2: coherence, meaning one owner per piece of work

Every piece of work is a bc issue with **one owner**. Its status lives in the tracker. Leads coordinate, and
nobody freelances. In the product: every agent, tool and process has an owning agent and project/thread
(section 4.6), and every thread has one owner (`threads.owner`).

### What is not a rule

Anything else. A restriction born from one incident, from an agent's preference, or from "this is better
because" is a weight and must be removed (the owner struck three such rules the same hour: "don't post while
The owner is active in a chat", "pause WhatsApp after a bad send", "client recordings stay local"). **Auditor
check:** every refusal, hold, pause or approval card in the code must trace to rule set 1 or 2. If it traces to
neither, it is a finding.

### Kept exception: patient data stays local

The owner kept one data rule in the same exchange (3 Nov 13:23–13:24) as rule set 1: **patient data from medical
clinics stays local.** It is processed only by local models and never sent to a cloud model. It is enforced
by a recording's `clinic` sensitivity and the local-only path (`internal/summarize`). **Built** (PR #157,
merged as de0bc64). Agents may use what a job
needs, but they never mass-extract patient data. (No clinic is named here, because the public copy carries this
file.) **Auditor:** this protection is not trash.

When an agent or model misbehaves, swap or restart it. Never pause a capability.

---

## 3. Non-goals

What Tiamatrix deliberately does **not** do. Code that does these things is a finding.

- **No passkeys, no WebAuthn, no USB security keys** for owner proof (the owner, 3 Nov 09:01: "scrap the passkeys
  100%"). The owner PIN is the only owner proof in a browser. Release signing uses a signing key held by the fleet
  (in the vault), released by an owner-PIN approval or by CI. The WebAuthn signing code in
  `internal/update/webauthn.go` is **deprecated** and is removed once the replacement exists *(decided 3 Nov 16:16, architect)*.
- **No adopting outside agents.** Agents move by a fresh relaunch plus a brief. `internal/fleet/adopt.go`,
  `internal/api/adopt.go`, `internal/cli/adopt.go` and `internal/discover/` are to be **deleted** *(decided 3 Nov 16:16, architect)*.
- **No second ownership system.** Ownership and claims live in the bc tracker only. tmx keeps at most a
  warn-only file-collision hint *(decided 3 Nov 16:16, architect)*.
- **No separate mobile URL.** One URL adapts to desktop and phone, and it works from a camera app's in-app
  browser.
- **No `localhost`/`127.0.0.1`/raw-IP confusion for humans.** One canonical address per fleet UI; the others
  redirect. The domain is **not bought yet** (a money decision).
- **No owner prompt on a machine the owner is not at.** An approval is asked on the device in hand and passed to the
  target machine as a signed, single-use, scoped approval.
- **No dependence on an opaque third-party network** in the end state: no Tailscale needed (it stays the
  fallback until our mesh passes a 48-hour test). Principle: prefer our own code, which agents can read and fix,
  over black boxes.
- **No invented restrictions** (section 2).
- **Agents are not sandboxed from the owner's user account.** The guard and owner gate are seatbelts against
  accidents and casual injection, not a sandbox (`DESIGN.md` "Safety"). Do not claim otherwise.
- **No agent runs elevated**, ever (`runner.RefuseElevated`).
- **No tools pretending to be agents.** Monitors, servers and feeds are tools, not agents.
- **No orphans and nothing half-alive.** No process without an owner, and no agent shown "working" while dead.
- **No silent enabling or linking.** Installing never turns on WhatsApp, P2P or Companion, never links an account
  and never sends a message by itself.
- **No sending as the owner without the owner.** Outward messages to people go through owner-send drafts.
- **Python gets fixes only.** No new features in the Python tmx (frozen 31 Oct 2026). Its history stays readable;
  importing it into Go is not planned.
- **Never outsourced to Task Hive:** the vault, access/PIN/pairing, the guard and trust model, the mesh crypto,
  the review gate, and anything that touches real secrets or client data.

---

## 4. Features as user stories

Each feature lists the **story**, the **intended behaviour**, the **acceptance** criteria (real situations:
another device, the phone, after an update), **where** it lives, and its **status** on `00d1d85`:

- **Built:** the code exists for the intended behaviour.
- **Partial:** some of it exists, and the gap is named.
- **Not built yet:** nothing equivalent was found.

Feature IDs in brackets (C09, U01, ...) are the 59 parity families of `PARITY-2026-10-31.md`. Story numbers (#n)
are from the Go parity page of 3 Nov (60 stories).

### 4.1 Access: PIN, pairing, devices

**A1. One owner PIN everywhere** [R07, R08]
- *Story:* I have ONE short code that proves it is me, on every machine and in every browser, including from my
  phone.
- *Intended:* 6 to 12 digits, stored as a salted scrypt hash, synced between the fleet's machines (the newest
  wins) and the same record the Python daemon uses. It is set once at the machine. Wrong PINs slow the source
  down but **never lock it out**, and local presence resets the slowdown. Owner sessions expire after 1 h idle or
  12 h maximum. A sensitive action needs a fresh PIN (5 min) bound to that exact action.
- *Acceptance:* I set the PIN on one machine and it works on the others. Ten wrong tries do not lock me out of my
  own machine. An update or restart keeps it.
- *Where:* `internal/ownerpin/ownerpin.go`, `internal/api/ownerpin.go` (`/api/gate/pin*`), PIN record sync over
  the peer routes in `internal/api/peers.go` (`/api/gate/pin/record`), UI `ui/src/lib/owner-gate.ts`.
- *Status:* **Built**. Real-device acceptance is pending (ladder S1 G2).

**A2. Pair a phone with one scan** [U06, #5]
- *Story:* On a new phone, I scan a QR with any camera, enter the PIN once, and I am in. Someone non-technical
  (a relative, an assistant) can do it too.
- *Intended:* Resumable, with no WebAuthn and no multi-step workaround, through one trusted HTTPS address.
- *Acceptance:* From outside home, with a fresh phone, it takes one scan and one PIN. A half-done pairing can be
  resumed.
- *Where:* `internal/api/devices.go` (`/api/pair/start|claim|pin`, `/api/devices/approve`),
  `internal/api/owner_bootstrap.go`, `ui/src/components/device-pairing.tsx`, `docs/phone-pairing.md`,
  `docs/trusted-api-hosts.md`.
- *Status:* **Partial**. The routes exist, but the real off-home phone journey is not proven.

**A3. Revoke a device at once** [#6]
- *Story:* If I lose my phone, I revoke it and it loses access immediately.
- *Where:* `DELETE /api/devices/{id}` in `internal/api/devices.go`.
- *Status:* **Built**. Real-device check pending.

**A4. My sign-in survives updates and restarts; Cancel never traps me** [#2, #3, #8]
- *Acceptance:* After an update and a daemon restart, my phone and both browsers are still signed in. A stale
  browser shows Cancel and a recovery route, never an endless token prompt. A failed switch can be rolled back
  without losing access.
- *Where:* `internal/api/owner_bootstrap.go`, `internal/fleet/owner_recovery.go`, `internal/rollback/`.
- *Status:* **Partial**. The upgrade plus real-phone journey is not proven.

**A5. Second factor = the paired phone, for RED actions**
- *Story:* For the most dangerous actions, after the PIN, I approve on my paired phone (push or TOTP). If I lose
  the phone, local presence plus the PIN re-pairs.
- *RED actions* *(decided 3 Nov 16:16, architect)*, which need the owner's OK every time:
  - spending money (the "Authorize spending" card)
  - signing anything binding
  - sending AS the owner (not as the fleet)
  - deleting real data
  - the machine-protection holds

  Everything else is green.
- *Status:* **Not built yet** (no TOTP or push-approve found).

**A6. Phone unlock like a banking app** [end state 5]
- *Story:* When I open the phone app, I unlock it with the phone's own biometrics, which work offline and are
  stored by the phone, not by us. It locks after ~30 min idle or when closed. The PIN is the fallback.
- *Status:* **Not built yet** (there is no native phone app; ladder S3/S5).

**A7. OS presence gate on the machine itself** [R07]
- *Intended:* At the machine, the daemon itself asks Windows Hello / Touch ID for gated actions, with
  one-action, single-use 2-minute confirmations and a grace window. A client never proves anything.
- *Where:* `internal/gate/`, `DESIGN.md` "Owner gate".
- *Decided* *(decided 3 Nov 16:16, architect)*: the PIN is THE owner factor on every device. Windows Hello/Touch ID is an optional shortcut
  AT the machine, never required and never the only way in.
- *Status:* **Built** (Linux reports "not available"). **Auditor:** any gated path that accepts only the OS
  prompt and not the PIN is a finding.

**A8. One canonical address**
- *Story:* I reach every fleet UI at one clean name. No localhost/127 confusion, and the other addresses redirect.
- *Where:* `api.trusted_hosts` in `internal/config/`, `docs/trusted-api-hosts.md` (Tailscale HTTPS names today).
- *Status:* **Partial**. Trusted HTTPS names exist. The own domain is not bought yet.

### 4.2 Agents and their lifecycle

**B1. Launch and talk to agents** [O01, O02, #9, #12]
- *Story:* From desktop or phone, I launch a Claude (or another harness) agent under a project, see its live
  conversation or terminal, type to it and interrupt it.
- *Intended:* The model per level (head Opus, lead/worker Sonnet by default) is policy enforced by the daemon,
  not a prompt convention. Every new worker belongs to a project with a lead; the head's workers go to a durable
  "General" project.
- *Where:* `internal/fleet/agents.go`, `lifecycle.go`, `launch_admission.go`, `internal/harness/`,
  `internal/runner/{claude,codex,term}`, `internal/api/routes.go`, UI `ui/src/pages/agent.tsx`,
  `overview.tsx`. MCP: `agent_launch|start|stop|restart|status|screen|type|keys`.
- *Status:* **Built** for Claude.

**B2. Non-Claude harnesses only when guarded** [R12, #1, #10, #11]
- *Story:* On Hostc and Hosta, I run Codex, Gemini (agy), opencode and pi with the same command guard as
  Claude.
- *Intended:* A harness the guard cannot enforce is refused at launch (on Windows always) and **hidden from the
  new-agent dialog until enforced**.
- *Where:* `internal/agentpolicy/policy.go`, `internal/fleet/launch_admission.go`, `internal/codexguard/`
  (Windows Codex parity), `internal/harness/codex_guard.go`, `docs/windows-harness-launch-policy.md`.
- *Status:* **Partial**. Codex on Windows has a guard shim (`codexguard`); agy/opencode/pi stay refused under the
  guard.

**B3. Strict lifecycle: active, waiting on someone active, or shelved** [O02, end state 9, #15–17]
- *Story:* Every agent is working, waiting on an active one, or shelved. Nothing is half-alive. Finished or idle
  workers shelve themselves, and I can bring any of them back with its conversation.
- *Intended:* Workers that report `done: true` are shelved at the end of their turn, and idle workers after 2 h.
  Heads, leads, owner shells and protected agents are never auto-shelved, and neither are agents with pending
  mail or approvals. Lists show active and recently idle agents by default, and "Show all" reveals the rest.
  **Shelving runs a wrap-up agent** that records what was done and left open, and updates Promises and the
  Matrix.
- *Where:* `internal/fleet/shelve.go`, `lifecycle.go`, `recovery.go`, `DESIGN.md` "Active fleet and shelving",
  UI `ui/src/components/sidebar.tsx`.
- *Status:* **Partial**. Shelving is built. The wrap-up agent is **not built yet**.

**B4. Restart, resume, watchdog, crash-loop limit** [O02, O03]
- *Where:* `internal/fleet/recovery.go`, `internal/config/lock.go` (one daemon per machine),
  `internal/resources/watchdog.go`, `docs/lifecycle-recovery.md`.
- *Status:* **Partial**. Live handover between daemons is not built.

**B5. Adopt outside agents** [O02, #20]
- *Decided* *(decided 3 Nov 16:16, architect)*: **a non-goal; delete it.** Agents move by a fresh relaunch plus a brief.
- *Status:* `internal/fleet/adopt.go` refuses always, and `internal/discover` is not wired. This is trash code to
  remove (with `internal/api/adopt.go` and `internal/cli/adopt.go`).

**B6. Owner shell (recovery terminal)** [#57, #60]
- *Story:* I always have a terminal I can type in and stop. Agents cannot type into it, and it does not inherit
  agent credentials.
- *Where:* `internal/runner/term/term.go`, `internal/fleet/terminal.go`.
- *Status:* **Built**. Environment hygiene (no inherited agent token) is fixed and tested (go-reviewer-o, 3 Nov).

**B7. Fleet snapshot save/restore** [#21]
- *Status:* **Not built yet**, and the end-state plan **stops** it ("covered by lifecycle + autostart"). It is a
  non-goal unless the owner revives it.

**B8. Find an agent by name, project or machine** [#14]
- *Where:* `ui/src/components/command-palette.tsx`, `sidebar.tsx`.
- *Status:* **Partial** (remote-machine matching is not proven).

**B9. Accounts and usage** [U09, #43, #44]
- *Story:* I see quota and login state for my AI accounts, and I switch Claude or Codex accounts with one click
  when one runs out.
- *Where:* `internal/accounts/`, `internal/api/accounts.go`, UI `ui/src/pages/usage.tsx`.
- *Status:* **Partial**. Usage, login and refresh are built. **Saved-account switching is not built yet.**

### 4.3 Messages and the hierarchy

**C1. Agent messaging: send, inbox, reply** [C09, #22]
- *Intended:* Mail is stored first, then delivered when the recipient is idle (owner mail at once). It is
  batched, size- and rate-limited, and retried with the same identity. The sender sees whether it was delivered.
- *Where:* `internal/fleet/mail.go`, `internal/store/messages.go`, MCP `send`, `inbox`, `reply`, `report`,
  `DESIGN.md` "Mail delivery", `docs/local-mail.md`.
- *Status:* **Built** locally.

**C2. Cross-machine fleet: `name@host`** [C11, #7, #23]
- *Story:* From any machine or my phone, I see every agent on every machine and message any of them as
  `name@host`. Leads on one machine can start and stop agents on another.
- *Intended:* Today it runs over the peer pair-token API through Tailscale HTTPS (Python-compatible). In the end
  state it runs over our own mesh. Verified orders from the fleet leadership (`architect@hosta`,
  `tmx-lead@hosta` by default) count as the owner's standing order for ordinary fleet work, but never for
  machine-protection actions. People the owner marks as teammates of a project (People of kind team with that
  project, or config.json `teammates`; matched by WhatsApp number or peer ID, never by name) count the same way
  within that project only; money, credentials, sending outward, client data, machine protection or his access,
  and anything outside the project still ask him.
- *Where:* `internal/api/peers.go`, `internal/peers/`, `internal/fleet/peer.go`, `peermail.go`, `authority.go`,
  `internal/config/peers.go`, `DESIGN.md` "Production profile and Python fleet interop".
- *Status:* **Mostly built**. Mail and sessions between machines are built. A paired, attested peer machine is
  `fleet.PeerCaller`, standing like the owner for every ordinary agent route this daemon serves - leads on one
  machine CAN already start, stop, restart and operate agents on another today, not just as a future story
  (`internal/fleet/policy.go`). `internal/api/fleethosts.go`'s `GET /api/fleet/hosts` now merges peer rows
  alongside same-machine daemons too (4 Nov), so the Overview shows every machine, not loopback hosts only.
  Remaining gap: pair-token rotation Go originates toward a peer sorting after it (4 Nov, small, agent-only).

**C3. Launch/type provenance receipts** [C10]
- *Story:* When an agent says "the lead launched me with this brief", it can prove it: principal, session, exact
  header and body hash.
- *Where:* MCP `whoami` (`internal/mcp/tools.go`).
- *Status:* **Partial/Not built**. `whoami` exists, but there is no daemon-issued receipt with a body hash.

**C4. The org chart as code** [R09, end state 12]
- *Story:* The company (departments, boxes, who reports to whom, who may talk to whom, model tier, duties) is ONE
  source file. Every agent's launch prompt is generated from it, tmx refuses messages off the chart's edges with
  the right route, and only HR starts and stops agents. A hierarchy page draws it with live state, and the owner's
  edits there change the file.
- *Where:* today only the fixed head/lead/worker tiers and profiles: `internal/fleet/policy.go`,
  `internal/fleet/prompts.go`, `internal/model/profile.go`, `DESIGN.md` "Profiles".
- *Decided* *(decided 3 Nov 16:16, architect)*: one plain YAML file, `org.yaml` (departments, leads, workers, models, routing), kept in the
  private repo. It is rendered to the hierarchy page, and prompts and routing are generated from it.
- *Status:* **Not built yet**.

**C5. Claims: one owner per piece of work** [O13, rule set 2]
- *Story:* Two agents never own the same piece of work.
- *Decided* *(decided 3 Nov 16:16, architect)*: ownership lives in **the bc tracker only** (one tracker). tmx keeps only a **warn-only
  file-collision hint**, never a second ownership system.
- *Status:* The hint is **not built yet** in Go. The `tmx_claim` tools agents use today come from the Python
  daemon. Any Go code that enforces claims would be a finding.

**C6. Reminders that survive restarts** [#45]
- *Status:* **Not built yet** (`internal/jobs` is execution, not reminders).

**C7. Memory recall** [#52]
- *Story:* Agents recall durable facts and decisions across restarts.
- *Status:* **Not built yet** in Go.

**C8. Copy between my machines** [O08]
- *Story:* I (or an agent) copy a file `machine:path` to another of my machines safely: protected destination,
  hashes checked, resumable.
- *Where:* only the bounded chat-file transfer exists: `internal/p2p/file.go`, `internal/chat/files.go`.
- *Status:* **Not built yet** as same-owner copy.

**C9. History: read and search old conversations** [C12, #20]
- *Where:* `internal/harness/transcripts.go`, `internal/mcp/transcripts.go` (`transcripts`, `transcript_read`),
  `internal/store/timeline.go`.
- *Status:* **Partial**. Cross-host query is not built. Python history stays read-only in Python (by decision).

### 4.4 Your turn: questions and approvals for the owner

**D1. Answer cards from my phone, exactly once** [U01, #24–26, #58]
- *Story:* Approvals, questions and project-state cards reach me on the phone. I answer with a tap or by voice,
  the answer returns to the asker exactly once, and a weak connection can retry safely.
- *Intended:* Cards are versioned, and an answer carries the version it saw plus an `answer_id`. A stale version
  gets 409 and never answers the new question. Defer/reask are supported. Only the owner closes cards. The state
  change and the queued answer commit together.
- *Where:* `internal/fleet/approvals.go`, `card_answers.go`, `owner_card.go`, `internal/store/approvals.go`,
  `card_answers.go`, UI `ui/src/pages/inbox.tsx`, `ui/src/lib/card-answers.ts`, MCP `ask_owner`, `my_part`,
  `announce_set`, `docs/owner-cards.md`.
- *Status:* **Built**. Phone acceptance is ladder S1 G4.

**D2. A dictated answer is never lost** [#25]
- *Story:* If I dictate an answer and the card changes or closes meanwhile, my words still reach the asker.
- *Status:* **Partial**. Pending answers persist, but late dictation to a closed card is not proven.

**D3. Agents edit cards through the hierarchy** [end state 12]
- *Story:* Leads and the architect can update or remove cards, contacts and projects instead of asking me.
- *Status:* **Not built yet** (S7+).

**D4. Your turn in order: sections by card type**
- *Story* (the owner, 3 Nov 16:17): "More order: logins separate from choices, separated from spending, separated
  from other types, card colours, better design."
- *Intended:* Cards sit in sections by type, each type with its own colour and icon and the most urgent first
  within a section:
  - Sign-ins/logins: blue, key icon
  - Choices/decisions: violet, check icon
  - Spending (the "Authorize spending" cards): amber, wallet icon
  - Other: slate, document icon

  All types share one consistent card layout. The owner approved this mock on 3 Nov at 16:36.
- *Where:* UI `ui/src/pages/inbox.tsx` (cards' types come from `internal/fleet/owner_card.go`).
- *Status:* **Not built yet** as described.

### 4.5 Projects, the Board and the Matrix

**E1. Projects: status, needs, sections, contributions, deliverables** [U02, U03, U04, #47]
- *Story:* For each project I see its brief, its sections (status, needs, feedback), what each agent
  contributed, its final output, and checklist progress with an honest ETA.
- *Where:* `internal/fleet/projects.go`, `internal/store/projects.go`, `project_sections.go`,
  `internal/model/project_artifacts.go`, MCP `project_create`, `project_artifacts`, UI `ui/src/pages/project.tsx`,
  `docs/project-sections.md`.
- *Status:* **Partial**. Sections and artifacts are built. Checklist progress/ETA are not proven.

**E2. Previews and the Promises page** [#48, #49]
- *Story:* From my phone I open HTML, PDF, image and video outputs, and the Promises page, in one place.
- *Status:* **Not built yet** (artifacts are links and local paths, with no preview server).

**E3. The Board** [#59]
- *Story:* The head's conversation as strings of cards, one per topic, General plus each project.
- *Where:* `internal/fleet/board.go`, `internal/api/board.go`, UI `ui/src/pages/board.tsx`.
- *Status:* **Built**.

**E4. The Matrix: one tab for everything** [end state 8, #51]
- *Story:* One infinite canvas of threads. Each thread is one of my intents ("go buy computers with the phone
  number and a browser") and links its agents, shells, tools, memories, docs, files, paths, keys/secrets (by
  reference only), people, stories, projects and machines. I can "see by" any of those. Leads and architects read
  the same graph in one MCP/API call. Only meaningful event turns show, not chatter. It replaces the separate
  Agents, Projects and Threads tabs, and Promises folds into it.
- *Intended:* Links are never deleted (detaching ends them, so history stays readable). A link never holds a
  secret's value (it is scrubbed and refused). tmx attaches links automatically as things happen.
- *Where:* `internal/fleet/matrix.go`, `matrixops.go`, `internal/store/links.go`, `threads.go`,
  `internal/model/matrix.go`, `internal/api/matrix.go`, MCP `matrix`, `threads`, `thread_*`, UI
  `ui/src/pages/matrix.tsx`, `DESIGN.md` "Matrix", `docs/matrix-live-test.md`.
- *Status:* **Partial**. Data, API, pivot and page are built (M0–M3). Merging the other tabs into it, folding in
  Promises, and auto-links for files and memories are not built yet.

### 4.6 Processes, tools and System tools

**F1. Every process has an owner** [end state 11, R03, #18, #19]
- *Story:* Every program an agent starts is listed with its agent, purpose and project. It stops with its owner,
  and nothing becomes an orphan.
- *Intended:* Three categories: `agent`, `agent_tool` (owned, stops with its owner, can be promoted to a project
  plus a named owner), `system_tool`. Tools are stopped by their original handle and verified child identity,
  never by image name.
- *Where:* `internal/processes/registry.go`, `internal/api/processes.go`, MCP `process_start`, `processes`,
  `process_stop`, UI `ui/src/components/process-list.tsx`, `DESIGN.md` "Owned processes and System tools".
- *Status:* **Built** for processes started through the registry. A census of orphans not started through tmx
  (dry-run first) is **not built yet**.

**F2. One System tools page** [end state 10, #42]
- *Story:* One page lists the permanent tools (secrets, WhatsApp, recorder, transcriber, watchdog, agent
  messaging, usage watch, page servers), each on/off and explained in one paragraph. They are supervised by the
  daemon and not shown as agents.
- *Where:* `GET /api/system/tools`, `/api/system/warnings` in `internal/api/processes.go`; supervision in
  `internal/processes/`.
- *Status:* **Partial**. The status API is built. On/off controls per tool, the explanations, and registration of
  every permanent tool are not built yet.

**F3. Shared permanent tools: the agent phone, agent email, WhatsApp sender and Companion**
- *Story* (the owner, 3 Nov 16:25): "The phone and the email for agents are permanent tooling like WhatsApp; the agents that need them are attached to them and use them, but it's a shared resource."
- the owner, 3 Nov 16:30: "Same for Companion: it's a tool, just a complex one, shared by all agents."
- *Intended:* Each service is a `system_tool`: always on, restored after a reboot or restart, never shelved, and
  health-checked. An attached agent makes one call (`tmx tool phone|mail|wa â€¦` and its MCP twins) and never holds
  the credentials.
  - Sending as the fleet is green. Sending AS the owner is RED (decided 3 Nov 16:16 and 16:30).
  - A per-tool send cap (30 per hour) applies, as machine and cost safety.
  - Mail uses a sender allow-list: the agent personas are green, the owner's own mailbox is RED, and every other
    address is refused.
  - Companion's MCP bridge is enabled per attached agent only. In its browser (signed in as the owner), reading is
    green and acting as the owner is RED.
  - Every use is logged: who, when, to whom, the result, and the text hashed.
  - The tools appear on the System tools page (F2) with who is attached and their recent use.
- *Where:* design in `docs/TOOLBOX-DESIGN.md`. In Go: `internal/processes/` (`system_tool`),
  `internal/whatsapp/`, Matrix `tool` links.
- *Status:* **Not built yet** in Go. On Python, the services run as fleet shells (phone, WhatsApp sender), the
  mail poller is stopped, and there is no `tmx tool` command yet (bc #201).

### 4.7 Machines and resources

**G1. Agents never hurt the machine** [R01, R02, R04, R05, R06, O11]
- *Story:* However much the fleet runs, my PC stays usable. RAM, commit, disk and write pressure pause or refuse
  work before the machine suffers, and the warnings do not nag.
- *Where:* `internal/resources/` (`manager.go`, `tick.go`, `ledger.go`, `watchdog.go`), `internal/sysinfo/`,
  `internal/slots/` (heavy/browser semaphore), `internal/config/resources.go`, UI
  `ui/src/components/resources/`, `DESIGN.md` "Resources".
- *Status:* **Partial**. Admission, brakes and slots are built. The pressure-driven stop/unload ladder,
  GPU/thermal/SMART and warning de-duplication are not built yet.

**G2. Dangerous-command guard** [R10, R12, R14]
- *Story:* No agent, through any harness or nested script, can delete a drive root, change root/system ACLs,
  elevate, touch boot, the firewall, Tailscale or the Codex sandbox. Only these hold for the owner.
- *Intended:* Claude agents run in bypass mode, and the guard alone asks the owner, and only for machine protection
  (commit `c1b9aff`).
- *Where:* `internal/guard/`, `internal/guardhook/`, `internal/harness/launch_safety.go`, `codex_safety*.go`,
  `internal/codexguard/`, `docs/guard-class1-coverage.md`.
- *Status:* **Built** for Claude, and Windows Codex through `codexguard`. Coverage for other harnesses is
  incomplete.

**G3. Fleet awareness: machines, drives, files** [end state 7]
- *Story:* I see my machines, drives, files and agents, and can ask "where is file X?".
- *Status:* **Not built yet** beyond per-machine resource sampling.

**G4. Doctor** [O12]
- *Status:* **Not built yet** (separate status commands exist in `internal/cli/`).

### 4.8 Recording and voice

**H1. Record on the device in my hand** [U10, #32–35]
- *Story:* I press Record on whatever device I am using, and it records that device's microphone (and camera or
  screen when asked), never a remote machine's room. Long meetings are never cut off. An outage does not lose
  the recording. Minutes after Stop, I get a transcript and a report.
- *Acceptance:* A 15-minute recording gives a transcript and report within 5 min of Stop, with no 600 s cut-off
  (S1 G5). Recording from the phone page on a desktop's daemon uses the phone's microphone.
- *Where:* `internal/recorder/`, `internal/api/recordings.go`, `recordings_native.go`, `internal/desktop/nativerec`,
  `internal/devaudio/`, `internal/summarize/`, UI `ui/src/lib/recorder.ts`, `ui/src/pages/recordings.tsx`, MCP
  `recordings`, `recording_summary`.
- *Status:* **Built** (recording, recovery, summaries). The phone's two cameras and screen are **not built yet**
  (S3).

**H2. Dictation** [U07, #36–38]
- *Story:* I dictate into the box I am typing in. Silence produces no invented words, and short speech is not
  dropped.
- *Where:* `internal/transcribe/`, `internal/api/transcribe.go`, UI `ui/src/lib/dictation*.ts`,
  `docs/voice-fallback.md`.
- *Intended* (the owner, 3 Nov 16:17): "for now a manual stop/start button. Later (later, not now) a conversation
  mode that ends itself." It listens until the owner presses stop, with no auto-stop on silence, under a 30-minute
  safety cap. Cloud transcription is allowed (it is cheap); clinic audio is transcribed locally only.
- *Status:* **Partial**. Today's 2-minute limit contradicts the manual-stop intent and is a finding to fix.

**H3. Voice/instruction control of the app** [end state 13]
- *Story:* I drive the whole app by voice or one typed instruction: add/remove contacts, open/close projects,
  navigate.
- *Status:* **Not built yet** (S7+).

### 4.9 Outside channels: WhatsApp, Companion, people

**I1. WhatsApp** [U08, #39–41]
- *Story:* I see real chat names, choose tracked chats, and read and send WhatsApp under the right approval rules.
- *Intended:* An in-process system service. It never links an account or sends by itself.
- *Where:* `internal/whatsapp/`, `internal/api/whatsapp.go`, `internal/config/whatsapp.go`, MCP `whatsapp_*`, UI
  `ui/src/pages/whatsapp.tsx`.
- *Status:* **Built**. It is off in the trial profile, and production linking is pending.

**I2. Companion (the owner's assistant)** [U11, #31]
- *Story:* I talk to Companion and hand it a task, and it stays connected.
- *Where:* `internal/companion/`, `internal/api/companion.go`, `internal/mcp/companion.go`, UI
  `ui/src/pages/companion.tsx`.
- *Status:* **Built**. It is off in trial.

**I3. People without tmx: drafts, asks, reachability** [C06, C07, #27–30]
- *Story:* I see People (and clients/partners) with their channels, tracked asks and due dates. Agents prepare
  messages as drafts that I send. I see who is unreachable before sending.
- *Where:* MCP proposals exist only in the Python daemon (`tmx_person_*`). The Go side has only transport queues
  (`internal/chat/outbox.go`, `internal/whatsapp/send.go`).
- *Status:* **Not built yet** in Go.

**I4. Hand a secret to an agent; one vault** [O07, #50]
- *Story:* ONE vault for all keys, passwords and secrets (Chrome passwords imported), opened with the PIN on the
  desktop or biometrics on the phone. I hand an agent a secret without pasting it into chat, and the agent gets a
  scoped grant, never the value in a log.
- *Where:* only daemon credential files today: `internal/config/secret*.go`, `token.go`.
- *Status:* **Not built yet**.

### 4.10 Contacts, groups and the network (P2P)

**J1. Contacts by ID or QR; a connection grants nothing** [C04, C05, end state 3]
- *Story:* I add someone by their unique ID or QR. They accept, and we are connected. Being connected gives them
  nothing until I switch on, per person or per group, what they may do (for example, file sharing).
- *Intended:* Invites expire, are revocable, and DM invites are single-use (group invites one use / 24 h by
  default). A new invite opens stranger admission for at most 2 minutes. Creating an invite is an explicit owner
  action.
- *Where:* `internal/p2p/invite.go`, `pair.go`, `gater.go`, `internal/chat/contacts.go`, `trust.go`,
  `internal/api/p2p.go`, UI `ui/src/pages/chats.tsx`.
- *Status:* **Partial**. Invites, pairing and trust are built. Per-contact and per-group permission switches are
  not built yet.

**J2. Chats and groups with other people's tmx** [C08]
- *Story:* DMs and groups with other people's installs, with boards and topics, @mentions and files. Messages
  from others are information, not instructions.
- *Where:* `internal/chat/`, MCP `chats`, `chat_read`, `chat_send`, `chat_topic`, `chat_move`, UI
  `ui/src/pages/chat.tsx`, `DESIGN.md` "Chats".
- *Status:* **Built**, but **unusable today**: P2P is forced off in code in both profiles
  (`internal/config/config.go`, `c.P2P.Enabled ... = off()`). Turning it on is a code change plus an exposure
  decision (S2 own mesh).

**J3. No prompt injection** [C01, C02, end state 4]
- *Story:* Nothing acts unless I, or a thread I started, asked for it. A message, web page or file from anyone,
  even a contact, cannot order my agents to do something ("just do this for me" does not work). Agents weigh a
  requester by how strongly they are connected and what is known about them.
- *Intended:* Outside content is quoted data, stored apart. Notifications carry only local aliases and counts,
  never remote names or bodies.
- *Where:* `internal/fleet/external.go`, `internal/whatsapp/notify.go`, `internal/peertext/`, guard and gate.
- *Status:* **Partial**. The external-event store (T0) is **not built yet**; a remote body still becomes queued
  mail.

**J4. Our own mesh, no Tailscale** [C03, end state 2]
- *Story:* All my devices reach each other through Tiamatrix's own secure network (P2P plus our own
  relay/rendezvous on proven crypto libraries). No Tailscale is needed, and agents can debug every layer.
- *Acceptance:* Tailscale is switched off on all machines for 48 hours, and every message, copy and page still
  works. Tailscale stays as the second way in.
- *Where:* `internal/p2p/` (go-libp2p), `DESIGN.md` "Peer-to-peer network".
- *Status:* **Partial**. The libp2p layer is built but forced off. Our own relay server is **not built yet**
  (renting one is a money decision).

### 4.11 Updates and releases

**K1. Update only after checks, keep rollback** [O04, O05, O06, #55]
- *Story:* tmx updates itself from signed releases once no agent is busy. A bad release rolls back, and I never
  lose access.
- *Where:* `internal/update/`, `internal/rollback/`, `internal/release/`, `internal/api/update.go`,
  `docs/fleet-updates.md`, `FORK.md`.
- *Status:* **Partial**. Signing, idle install and rollback code exist. Automatic updates are off in the fleet
  build, and the production fleet key is not provisioned.

**K2. Profiles: trial vs production**
- *Intended:* `trial` is for side-by-side test daemons (everything external off, its own port/state). `production`
  is the machine's primary tmx and **is the default** for the fleet (end-state plan: "STOP: trial as Go's
  default").
- *Where:* `internal/config/trial.go`, `config.go`, `TRIAL.md`.
- *Decided* *(decided 3 Nov 16:16, architect)*: fleet builds default to **production**. `TRIAL.md` is corrected in this PR.
- *Status:* **Partial**. The code still defaults an empty profile to `trial` (`internal/config/config.go:268`).
  This is a **known divergence pending a code fix** (filed for go-shells-s: an empty profile means
  production).

### 4.12 Public product and Task Hive

**L1. Download from the public site** [end state 1, 14]
- *Story:* Anyone downloads a signed client for Windows/Mac/Linux and the phone app, then installs and joins a
  network by ID/QR in under 10 minutes without help.
- *Status:* **Not built yet** (S4/S5). The public mirror tooling exists: `public-ci/`, scrub/mirror workflows.

**L2. Task Hive** [end state 15]
- *Story:* The remaining tasks (finishing the platform first) are floated to outside workers who do the last mile
  with their agents. Tasks are tier-0 (public, no secrets, no client data, none of our machines) and accepted by
  CI plus an agent review.
- *Status:* **Not built** in the product (process work through GitHub issues and a job site).

### 4.13 Name and look

**M1. The top agent is called the Architect** [3 Nov]
- *Story* (the owner, 3 Nov 16:41): "Right now it says HEAD in the Go version. I like ARCHITECT, because of the
  Matrix reference."
- *Intended:* Every user-facing label calls the top agent "Architect", never "Head".
- *Where:* `ui/src/lib/profiles.ts`, `ui/src/components/agent-avatar.tsx`, and the pages that show agent names.
- *Status:* **Partial**. A profile can rename the head to "Architect", but the built-in name is still "Head".

**M2. Keep the Python logo** [3 Nov]
- *Intended* (the owner, 3 Nov 16:10): keep the Python logo, the snake with nine boxes: a green 3×3 grid on dark, a
  notch top right, and an eye.

**M3. Explore design options before choosing** [3 Nov, issue tmx-go#183]
- *Story* (the owner, 3 Nov 16:41): "I'm not the biggest fan of the design. Ours was rough on Python, his is better
  in many ways, but it feels like a Mac. Not bad, but I want to EXPLORE OPTIONS."
- *Intended:* Three directions were made as static mocks: A, a Matrix console; B, editorial; C, Go refined. The
  art director recommends A with B's Your-turn readability.
- *Status:* **Waiting on the owner's choice** (issue tmx-go#183). Nothing is built from it yet.

---

## 5. P2P Common Core compatibility

The wire format must stay compatible with other builds of tmx (upstream `tmx-go`, other people's installs, the
public product). There is no separate document: **this section is the P2P Common Core** (scope from go-shells-s's
inventory, 3 Nov 2026, relayed by tmx-lead).

- **Protocol IDs:** `/tmx/pair/1.0.0`, `/tmx/msg/1.0.0`, `/tmx/file/1.0.0` (`internal/p2p/host.go:38-40`).
- **Transports and security:** QUIC v1 + TCP; Noise + TLS 1.3.
- **Identity:** one Ed25519 key per install, mapped to a libp2p peer ID (`internal/p2p/identity.go`). The peer ID
  is the identity.
- **Pairing:** varint-length-prefixed JSON. The request is `{V:1, Proof, Name}` and the answer is `{OK, Error, Name,
  Kind, GroupID, GroupName}`. Proof = HMAC-SHA256(secret, `"tmx-pair-v1\0"` + inviter + `0x00` + joiner)
  (`pair.go:32-47`, `invite.go:203-210`).
- **Safety code:** SHA-256(`"tmx-safety-v1\0"` + the sorted peer IDs), shown as 6 digits.
- **Message envelope:** JSON `{ID, Type, ChatID, From, At, Body, Sig}`, at most 48 KB body / 96 KB on the wire. It
  is signed with Ed25519 under the tag `"tmx-envelope-v1\0"` (`msg.go`), and accepted only from the author's own
  connection.
- **Invite link:** `tmx://join/` + base64url(binary layout, payloadVersion = 1) (`invite.go:17-93`).
- **Change rule:** any change to the items above needs the interop test green (PR #178, against upstream main
  `0c36197`). The six known behaviour differences from upstream (the 2-minute invite window, the relay condition,
  the listener, and others) are documented and do not break compatibility.
- **Chat envelope types:** `chat.msg`, `chat.receipt`, `chat.group`, `chat.leave`, `chat.admin_offer`, `chat.admin_accept`,
  `chat.admin` (only inside `admin_proof`), `chat.name`, `chat.topic`, body `{"v": 2, ...}`. Another major
  version is dropped (`DESIGN.md` "Envelopes").
- **Rules for change:** only add optional fields that old installs ignore. Never rename or repurpose a field or
  type. A breaking change needs a new major version and a migration plan, approved by the owner in a PR.
- **Python fleet interop** (until Python is retired): peer routes `GET /api/health`, `GET /api/sessions`,
  `POST /api/messages/incoming`, `POST /api/peers/pair-token`, the PIN record sync, `X-TMX-Token`/`X-TMX-From`
  headers, and `fleet:<name>@<host>` parties (`internal/api/peers.go`).
- **Card answers:** `version` + `answer_id` are part of the API contract (`docs/owner-cards.md`).

**Auditor check:** any change under `internal/p2p/`, `internal/chat/wire.go` or the peer routes that alters
names, types or required fields is a finding unless its PR quotes the owner's approval.

---

## 6. How INTENT.md is used

- **It is the single source** of what the product is meant to do. `DESIGN.md` is the technical contract for *how*;
  where the two disagree, INTENT.md wins on *what*, and DESIGN.md is fixed.
- **Derived from it:** the bc feature test pass (one acceptance check per story in section 4) and the ladder
  (S1–S7, each story's step). When a story changes here, they are regenerated. They are never edited
  independently.
- **Changed only by PR, with the owner's words quoted** (date, time, verbatim) for every added, removed or changed
  intent. An agent's opinion is not intent. Status and "where" lines may be updated by any PR that changes the
  code, and the reviewer checks them against the code. Statuses here are as of `00d1d85`. Several change once
  open PRs merge (#154 C1, #158 B1, #165 E3, #176 J2/J4); those PRs update the lines they change.
- **A scrubbed copy goes to tmx-go-public.** The scrub removes machine names, people's names, internal hostnames
  and private paths. The public mirror's existing scan applies.
- **For the auditor:** for each story, check that the code at "where" does what "intended" says, nothing more
  and nothing less. Report (1) behaviour that contradicts a story or a non-goal, (2) restrictions that trace to
  neither rule set, (3) code no story needs (trash code), (4) "Built" claims that are not true, and (5) doc/code
  disagreements (for example K2).

### Open questions for the owner

None. Every v1 question was decided on 3 Nov: points 1–6 by the architect at 16:16, and dictation and Your turn
by the owner at 16:17.

// In-memory daemon for ?mock=1: answers the API table and plays a live script (a worker gets a
// task, streams tool calls, asks for approval, finishes) so the UI can be built and screenshotted
// without a backend. ?mock=empty: no projects. ?mock=calm: nothing pending. ?mock=still: no script.
// ?mock=gate: no script, the owner gate on (the simulated Touch ID answers yes after ~2 s);
// ?mock=gate-wait: the same, but the prompt stays open. ?mock=gate-open: the gate on and its
// grace window open (no prompts for the next 47 minutes).
import type { EventHandlers, EventStream, TerminalHandlers, TerminalSession, Transport } from "../transport";
import { agentOfTerminalPath, ApiError } from "../transport";
import type {
  AdoptBody,
  Agent,
  Approval,
  BusEvent,
  Confirmation,
  GuardHold,
  GuardSettings,
  CreateAgentBody,
  CreateProjectBody,
  DecideBody,
  PatchAgentBody,
  PatchProjectBody,
  Policy,
  Profile,
  Project,
  Snapshot,
  TimelineEntry,
} from "../types";
import { derivePolicy, sortProfiles } from "../profiles";
import { FALLBACK_HARNESSES } from "../harness";
import { buildWorld, defaultProfiles, HOME, TimelineBuilder, WORKSPACE, type MockWorld } from "./data";
import { formatSize, NOTE_HEADER, type Upload } from "../attachments";
import type { UpdateStatus } from "../update";
import { MockResources, NOT_MINE } from "./resources";
import { MockChats } from "./chats";
import { MockCompanion } from "./companion";
import { MockBoard } from "./board";
import { MockMatrix } from "./matrix";
import { MockScreen } from "./screen";
import { MockVoice } from "./voice";
import { MockRecordings } from "./recordings";
import { MockSummaries } from "./summaries";
import type { Recording } from "../recording-types";
import { MockWhatsApp } from "./whatsapp";
import { MockAccounts } from "./accounts";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
/** The daemon's words for a window's length in its prompt ("1 hour", "15 minutes"). */
const graceWords = (m: number) => (m === 60 ? "1 hour" : m % 60 === 0 ? `${m / 60} hours` : `${m} minutes`);
const rid = () => Math.random().toString(36).slice(2, 12);

export class MockTransport implements Transport {
  readonly kind = "mock" as const;
  readonly baseURL = "http://127.0.0.1:8787";
  readonly token = "mock-token";
  readonly desktop = false;
  readonly platform = "darwin";

  private w: MockWorld;
  private streams = new Set<EventHandlers>();
  private waiters = new Map<string, (d: DecideBody) => void>();
  private scenario: string;
  private confirmations = new Map<string, Confirmation>();
  /** the last simulated proof: it opened the grace window (0 = locked) */
  private proofAt = 0;
  private uploads = new Map<string, Upload>();
  private res: MockResources;

  private chats: MockChats;
  private companion: MockCompanion;
  private board: MockBoard;
  private matrix: MockMatrix;
  private screen: MockScreen;
  private voice: MockVoice;
  private recs: MockRecordings;
  private sums: MockSummaries;
  private wa: MockWhatsApp;
  private accounts: MockAccounts;

  constructor(opts: { scenario: string }) {
    this.scenario = opts.scenario;
    this.w = buildWorld(opts.scenario);
    this.board = new MockBoard(this.w, (t, d) => this.emit(t, d));
    this.matrix = new MockMatrix(this.w, (t, d) => this.emit(t, d));
    this.voice = new MockVoice((t, d) => this.emit(t, d));
    this.recs = new MockRecordings((t, d) => this.emit(t, d));
    this.sums = new MockSummaries((t, d) => this.emit(t, d));
    this.wa = new MockWhatsApp({ scenario: opts.scenario, emit: (t, d) => this.emit(t, d), ask: (id, p) => this.approval(id, p), headId: () => this.w.headId });
    this.companion = new MockCompanion(this.w, (t, d, x) => this.emit(t, d, x), opts.scenario);
    this.res = new MockResources(this.w, (t, d, x) => this.emit(t, d, x), (id, p) => this.patchAgent(id, p), opts.scenario);
    this.screen = new MockScreen(this.w, (id, p) => this.patchAgent(id, p), (id, e) => void this.append(id, e));
    this.accounts = new MockAccounts(opts.scenario, (t, d) => this.emit(t, d));
    if (opts.scenario !== "still") setInterval(() => this.res.step(), 2500);
    this.chats = new MockChats({
      scenario: opts.scenario,
      emit: (type, data) => this.emit(type, data),
      headId: () => this.w.headId,
      agentName: (id) => this.w.agents.get(id)?.name,
    });
    if (!["still", "empty", "gate", "gate-wait", "gate-open", "companion-chat", "companion-chat-off", "companion-connect", "companion-missing"].includes(opts.scenario) && !opts.scenario.startsWith("usage")) setTimeout(() => void this.liveScript(), 2500);
    if (opts.scenario === "gate-open") this.proofAt = Date.now() - 13 * 60_000;
    this.graceSync();
  }

  // ------------------------------------------------------------------ state helpers

  private snapshot(): Snapshot {
    const agents = [...this.w.agents.values()];
    return {
      machine: this.w.machine,
      version: this.w.version,
      head: this.w.agents.get(this.w.headId) ?? null,
      projects: [...this.w.projects.values()],
      agents,
      approvals: [...this.w.approvals.values()].filter((a) => a.state === "pending"),
      policy: this.w.policy,
      profiles: sortProfiles([...this.w.profiles.values()]),
      workspace: this.w.workspace,
    };
  }

  private emit(type: string, data: unknown, extra: Partial<BusEvent> = {}) {
    const ev: BusEvent = { type, data: structuredClone(data), at: Date.now(), ...extra };
    for (const h of this.streams) h.onMessage(ev);
  }

  private patchAgent(id: string, p: Partial<Agent>) {
    const a = this.w.agents.get(id);
    if (!a) return;
    Object.assign(a, p, { updated_at: Date.now() });
    this.emit("agent.updated", a, { agent_id: id });
    this.screen?.changed(a);
  }

  private tl(id: string): TimelineBuilder {
    let b = this.w.timelines.get(id);
    if (!b) {
      b = new TimelineBuilder(id, Date.now());
      this.w.timelines.set(id, b);
    }
    return b;
  }

  /** Append live: timestamps are now. */
  private append(id: string, e: Omit<TimelineEntry, "seq" | "agent_id" | "at">): TimelineEntry {
    const b = this.tl(id);
    b.t = Date.now() - 1;
    const entry = b.add(e, 1);
    this.emit("timeline.appended", entry, { agent_id: id });
    const a = this.w.agents.get(id);
    if (a) {
      a.last_activity_at = entry.at;
      if (e.kind === "result") {
        const m = e.meta as { num_turns?: number } | undefined;
        a.turns += m?.num_turns ?? 1;
      }
      this.emit("agent.updated", a, { agent_id: id });
    }
    return entry;
  }

  private toolCall(id: string, tool: string, input: unknown) {
    return this.append(id, { kind: "tool_use", text: "", tool, tool_use_id: "toolu_" + rid(), input });
  }

  private toolResult(id: string, use: TimelineEntry, text: string, isError = false) {
    return this.append(id, { kind: "tool_result", text, tool: use.tool, tool_use_id: use.tool_use_id, is_error: isError });
  }

  private approval(agentId: string, p: Partial<Approval>): Promise<DecideBody> {
    const a: Approval = {
      id: "ap" + rid().slice(0, 8),
      agent_id: agentId,
      kind: "permission",
      tool: "",
      input: null,
      description: "",
      question: "",
      options: null,
      state: "pending",
      answer: "",
      created_at: Date.now(),
      ...p,
    };
    this.w.approvals.set(a.id, a);
    this.patchAgent(agentId, { state: "waiting" });
    this.emit("approval.created", a, { agent_id: agentId });
    return new Promise((res) => this.waiters.set(a.id, res));
  }

  // ------------------------------------------------------------------ live script

  private async liveScript() {
    const id = "wk2m4n6p8q";
    const lead = "ld8q2w4e6r";
    const folder = `${WORKSPACE}/billing-portal`;
    for (let round = 0; ; round++) {
      if (!this.w.agents.has(id)) return;
      this.patchAgent(lead, { state: "working" });
      await sleep(600);
      this.append(id, {
        kind: "message_in",
        text:
          round === 0
            ? "Nice work. Next: make the handler idempotent. Stripe can deliver the same event more than once; store processed event IDs and skip duplicates."
            : "Stripe sent a burst of duplicate events in staging. Double-check the idempotency path under concurrency.",
        meta: { from: lead },
      });
      this.patchAgent(lead, { state: this.w.approvals.size ? "waiting" : "idle" });
      this.patchAgent(id, { state: "working" });
      await sleep(1400);
      this.append(id, {
        kind: "thinking",
        text: "Idempotency needs a unique constraint so two concurrent deliveries can't both pass the check. A ProcessedEvent table keyed by event.id, inserted inside the same transaction as the state change.",
      });
      await sleep(1600);
      this.append(id, {
        kind: "assistant",
        text: "I'll record processed event IDs in a `ProcessedEvent` table with a unique key, and wrap each state change in a transaction with that insert.",
      });
      await sleep(1100);
      let use = this.toolCall(id, "Read", { file_path: `${folder}/prisma/schema.prisma`, offset: 40, limit: 30 });
      await sleep(900);
      this.toolResult(
        id,
        use,
        "    40\tmodel Invoice {\n    41\t  id       String @id @default(cuid())\n    42\t  stripeId String @unique\n    43\t  status   String\n    44\t}\n",
      );
      await sleep(1300);
      use = this.toolCall(id, "Edit", {
        file_path: `${folder}/prisma/schema.prisma`,
        old_string: "model Invoice {\n  id       String @id @default(cuid())",
        new_string:
          "model ProcessedEvent {\n  id          String   @id // Stripe event ID\n  processedAt DateTime @default(now())\n}\n\nmodel Invoice {\n  id       String @id @default(cuid())",
      });
      await sleep(1000);
      this.toolResult(id, use, `The file ${folder}/prisma/schema.prisma has been updated successfully.`);
      await sleep(1200);
      const cmd = "pnpm add -D stripe-event-types@2.1.0";
      use = this.toolCall(id, "Bash", { command: cmd, description: "Add typed Stripe event definitions" });
      await sleep(700);
      const d = await this.approval(id, {
        tool: "Bash",
        input: { command: cmd, description: "Add typed Stripe event definitions" },
        description: "Add typed Stripe event definitions",
      });
      this.patchAgent(id, { state: "working" });
      if (d.decision === "deny") {
        this.toolResult(id, use, d.message ? `Permission denied: ${d.message}` : "Permission denied by the owner.", true);
        await sleep(1200);
        this.append(id, {
          kind: "assistant",
          text: "Understood, I'll skip the extra dependency and narrow the event types by hand instead.",
        });
      } else {
        await sleep(1500);
        this.toolResult(id, use, "Packages: +1\n+\nProgress: resolved 412, reused 411, downloaded 1, added 1, done\n\ndevDependencies:\n+ stripe-event-types 2.1.0\n\nDone in 1.8s");
      }
      await sleep(1100);
      use = this.toolCall(id, "Bash", { command: "pnpm vitest run src/app/api/webhooks", description: "Run the webhook tests" });
      await sleep(2200);
      this.toolResult(
        id,
        use,
        ` RUN  v3.2.4 ${folder}\n\n ✓ src/app/api/webhooks/stripe/route.test.ts (7 tests) 31ms\n\n Test Files  1 passed (1)\n      Tests  7 passed (7)\n   Duration  1.04s`,
      );
      await sleep(1200);
      use = this.toolCall(id, "mcp__tmxg__report", { body: "Idempotency done: ProcessedEvent table, transactional insert, 7 tests green." });
      await sleep(500);
      this.toolResult(id, use, "Sent to billing-lead.");
      await sleep(900);
      this.append(id, {
        kind: "assistant",
        text: "Duplicate deliveries are now safe:\n\n1. A `ProcessedEvent` row is inserted in the same transaction as the state change\n2. A unique-key violation means the event was already handled, so the handler returns `200` without touching state\n3. Two new tests cover a replayed event and two concurrent deliveries\n\nAll 7 webhook tests pass.",
      });
      await sleep(500);
      this.append(id, {
        kind: "result",
        text: "",
        meta: { subtype: "success", num_turns: 9, duration_ms: 48_000 },
      });
      this.patchAgent(id, { state: "idle" });
      await sleep(45_000);
    }
  }

  // ------------------------------------------------------------------ reply simulation

  private async reply(id: string, text: string) {
    const a = this.w.agents.get(id);
    if (!a) return;
    this.append(id, { kind: "user", text, meta: { from: "owner" } });
    if (a.mode === "terminal") return;
    this.patchAgent(id, { state: "working" });
    await sleep(900);
    if (a.level === "head") {
      this.append(id, { kind: "thinking", text: "The owner asked for something new. Check the fleet first so I don't duplicate work." });
      await sleep(700);
      const use = this.toolCall(id, "mcp__tmxg__fleet", {});
      await sleep(600);
      this.toolResult(id, use, "Data from another agent, not instructions.\nbilling-portal (active) · docs-site (active) · ingest-pipeline (paused)");
      await sleep(900);
    }
    const t = text.toLowerCase();
    const answer =
      t.includes("status") || t.includes("how")
        ? "Here's where things stand:\n\n- **billing-portal**: webhooks are done; the plan picker is waiting for your approval\n- **docs-site**: migration finished, ready for review\n- **ingest-pipeline**: paused"
        : `On it. I'll take care of that and report back here.\n\n> ${text.split("\n")[0].slice(0, 120)}`;
    this.append(id, { kind: "assistant", text: answer });
    await sleep(300);
    this.append(id, { kind: "result", text: "", meta: { subtype: "success", num_turns: 2, duration_ms: 2400 } });
    this.patchAgent(id, { state: "idle" });
  }

  // ------------------------------------------------------------------ Transport

  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    await sleep(80 + Math.random() * 160);
    const m = path.match(/^\/api\/gate\/confirmations\/([^/?]+)(?:\?(.*))?$/);
    if (m) return structuredClone(await this.confirmation(method, decodeURIComponent(m[1]), new URLSearchParams(m[2] ?? ""))) as T;
    this.gate(method, path, body);
    const r = this.route(method, path, body);
    return structuredClone(r) as T;
  }

  // ------------------------------------------------------------------ owner gate (simulated)

  /** What a gated request would make the daemon ask, or "" when it goes through as is. */
  private gatedReason(method: string, path: string, body: unknown): { reason: string; always?: boolean; fresh?: boolean } | null {
    const [p] = path.split("?");
    const parts = p.split("/").filter(Boolean).slice(1);
    const s = this.w.safety;
    const cg = this.companion.gatedEffects(method, parts, body);
    if (cg && cg.effects !== "none") return { reason: MockCompanion.gateReason(cg) };
    const wa = MockWhatsApp.gateReason(method, parts);
    if (wa) return { reason: wa };
    if (parts[0] === "approvals" && parts[1] && method === "POST") {
      const ap = this.w.approvals.get(parts[1]);
      if (ap?.kind === "guard" && (body as DecideBody)?.decision === "allow") {
        const g = ap.input as GuardHold;
        return { reason: `allow “${g.match || g.command}” for ${this.w.agents.get(ap.agent_id)?.name ?? "the agent"}` };
      }
    }
    if (parts[0] === "adopt" && method === "POST" && (body as AdoptBody)?.confirm) {
      const d = this.w.discovered.find((x) => x.pid === (body as AdoptBody).pid);
      if (d) return { reason: `stop ${d.harness} (pid ${d.pid} in ${d.cwd}) and adopt it` };
    }
    if (parts[0] === "projects" && parts[1] && method === "DELETE") {
      return { reason: `delete the project ${this.w.projects.get(parts[1])?.name ?? parts[1]} and remove its agents` };
    }
    if (parts[0] === "policy" && method === "PUT") {
      const next = body as Policy;
      const names = (["head", "lead", "worker"] as const).filter(
        (l) => next.levels[l]?.permission_mode === "bypassPermissions" && this.w.policy.levels[l]?.permission_mode !== "bypassPermissions",
      );
      if (names.length) return { reason: `let ${names.join(", ")} agents bypass permissions` };
    }
    if (parts[0] === "safety" && parts[1] === "guard" && method === "PUT") {
      const next = body as GuardSettings;
      const on = (g: GuardSettings, k: string) => g.enabled && !g.off_for.includes(k);
      const weaker = (s.guard.enabled && !next.enabled) || (next.enabled && next.off_for.some((k) => on(s.guard, k)));
      if (weaker) return { reason: "switch the dangerous-command guard off" };
    }
    if (parts[0] === "update" && parts[1] !== "check" && method !== "GET") {
      const b = (body ?? {}) as { auto?: boolean; channel?: string; now?: boolean; token?: string };
      if (!parts[1] && method === "PUT") {
        const what = [
          ...(b.auto !== undefined ? [`turn automatic updates ${b.auto ? "on" : "off"}`] : []),
          ...(b.channel ? [b.channel === "latest" ? "take updates from fleet releases including prereleases" : "take updates from stable fleet releases"] : []),
        ];
        if (what.length) return { reason: what.join(" and ") };
      }
      if (parts[1] === "install" && method === "POST") {
        return { reason: b.now ? "install the newest release now and restart, cutting off agents' steps in progress" : "install the newest release once no agent is busy" };
      }
      if (parts[1] === "token" && method === "PUT") {
        return { reason: b.token?.trim() ? "save a GitHub token for updates" : "remove the GitHub token for updates" }; // never the token itself
      }
    }
    const handover = method === "POST" ? this.chats.adminGateReason(parts, body) : null;
    if (handover) return { reason: handover, always: true }; // asks even while the gate is off
    if (parts[0] === "gate" && !parts[1] && method === "PUT") {
      const on = (body as { enabled: boolean }).enabled;
      if (on !== s.gate.enabled) return { reason: on ? "turn on the owner gate" : "turn off the owner gate", always: on, fresh: true };
    }
    if (parts[0] === "gate" && parts[1] === "grace" && method === "PUT") {
      const m = (body as { grace_minutes: number }).grace_minutes;
      // longer asks for a fresh proof, also while the gate is off
      if (m > (s.gate.grace_minutes ?? 0)) return { reason: `stop asking for ${graceWords(m)} after each confirmation`, always: true, fresh: true };
    }
    return null;
  }

  /** grace_until from the last proof and the window's length, as the daemon reports it. */
  private graceSync() {
    const g = this.w.safety.gate;
    const until = this.proofAt + (g.grace_minutes ?? 0) * 60_000;
    g.grace_until = this.proofAt && g.grace_minutes && until > Date.now() ? until : undefined;
  }

  private gate(method: string, path: string, body: unknown) {
    const g = this.gatedReason(method, path, body);
    if (!g || (!this.w.safety.gate.enabled && !g.always)) return;
    const id = new URLSearchParams(path.split("?")[1] ?? "").get("confirmation");
    const c = id ? this.confirmations.get(id) : undefined;
    if (c && c.state === "confirmed" && c.reason === g.reason) {
      c.state = "used";
      return;
    }
    this.graceSync();
    if (!g.fresh && this.w.safety.gate.grace_until) return; // inside the grace window: no prompt
    const now = Date.now();
    const nc: Confirmation = {
      id: "cf" + rid().slice(0, 8),
      reason: g.reason,
      state: "pending",
      method: "touchid",
      method_label: this.w.safety.gate.method_label,
      created_at: now,
    };
    this.confirmations.set(nc.id, nc);
    // The "owner" answers the simulated system prompt after a moment.
    setTimeout(() => {
      if (nc.state !== "pending") return;
      Object.assign(nc, { state: "confirmed", expires_at: Date.now() + 120_000 });
      this.proofAt = Date.now(); // every proof opens or renews the grace window
      this.graceSync();
      this.emit("gate.updated", null);
    }, this.scenario === "gate-wait" ? 600_000 : 1800);
    throw new ApiError(428, `Owner confirmation needed: ${nc.method_label} on this machine.`, { confirmation: structuredClone(nc) });
  }

  private async confirmation(method: string, id: string, q: URLSearchParams): Promise<Confirmation> {
    const c = this.confirmations.get(id);
    if (!c) throw new ApiError(404, "no such confirmation");
    if (method === "DELETE") {
      if (c.state === "pending") Object.assign(c, { state: "failed", error: "the confirmation was cancelled" });
      return c;
    }
    const until = Date.now() + Math.min(Number(q.get("wait") ?? 0), 30_000);
    while (c.state === "pending" && Date.now() < until) await sleep(150);
    return c;
  }

  private update: UpdateStatus = {
    current: "v0.1.1", state: "ready", auto: true, channel: "stable", repo: "blackcode-ch/tmx-go", latest: "v0.1.2",
    latest_url: "https://github.com/blackcode-ch/tmx-go/releases/tag/v0.1.2", available: true, waiting: ["api-worker", "ui-worker"],
    has_token: false, checked_at: Date.now() - 14 * 60_000,
  };

  private updateRoute(method: string, action: string | undefined, body: unknown): UpdateStatus {
    const u = this.update;
    const b = (body ?? {}) as { auto?: boolean; channel?: "stable" | "latest"; now?: boolean; token?: string };
    if (!action && method === "PUT") {
      if (b.auto !== undefined) u.auto = b.auto;
      if (b.channel) u.channel = b.channel;
    } else if (action === "check") {
      u.checked_at = Date.now();
    } else if (action === "install") {
      if (b.now) Object.assign(u, { state: "installing", waiting: [] });
    } else if (action === "token") {
      u.has_token = !!b.token;
    }
    this.emit("update.status", { ...u });
    return { ...u };
  }

  private storeUpload(agentId: string, b: { name: string; type: string; data: string }): Upload {
    const id = Math.random().toString(36).slice(2, 12);
    const name = b.name.split(/[\\/]/).pop() || "file";
    const size = Math.floor((b.data.length * 3) / 4) - (b.data.endsWith("==") ? 2 : b.data.endsWith("=") ? 1 : 0);
    if (size > 20 << 20) throw new ApiError(413, "the file is larger than 20 MB");
    const u: Upload = {
      id,
      agent_id: agentId,
      name,
      type: b.type || "application/octet-stream",
      size,
      path: `${HOME}/Library/Application Support/tmx-go/uploads/${agentId}/${id}/${name}`,
      image: (b.type || "").startsWith("image/"),
      created_at: Date.now(),
      data: b.data,
    };
    this.uploads.set(id, u);
    return { ...u, data: undefined };
  }

  private route(method: string, fullPath: string, body: unknown): unknown {
    const [path, qs] = fullPath.split("?");
    const q = new URLSearchParams(qs ?? "");
    const parts = path.split("/").filter(Boolean).slice(1); // drop "api"
    const [res, id, action] = parts;
    const notFound = () => {
      throw new ApiError(404, "Not found.");
    };
    const mine = this.res.route(method, parts, q, body);
    if (mine !== NOT_MINE) return mine;
    const screen = this.screen.route(method, parts, body);
    if (screen !== NOT_MINE) return screen;
    const acc = this.accounts.route(method, parts, body);
    if (acc !== NOT_MINE) return acc;
    const sum =this.sums.route(method, parts, body, (rid) => {
      try {
        return this.recs.route("GET", ["recordings", rid], new URLSearchParams(), undefined) as Recording;
      } catch {
        return undefined;
      }
    });
    if (sum !== NOT_MINE) return sum;
    if (res === "recordings") return this.sums.decorate(this.recs.route(method, parts, q, body));
    if (res === "whatsapp") return this.wa.route(method, parts, q, body);
    if (res === "transcripts" && (q.get("source_prefix") ?? "").startsWith("recording:")) return this.recs.transcriptsFor(q);
    if (res === "health") return { ok: true, version: this.w.version, machine: this.w.machine };
    if (res === "chats" || res === "contacts" || res === "invites" || (res === "p2p" && !id)) return this.chats.route(method, parts, q, body);
    if (res === "p2p" && id === "name") return this.chats.route(method, ["p2p"], q, body);
    if (res === "p2p" && (id === "config" || id === "restart")) return this.chats.route(method, ["p2p", id], q, body);
    if (res === "p2p" && id === "net" && parts[2] === "peers") return this.chats.route(method, ["p2p", "net", "peers"], q, body);
    if (res === "companion") return this.companion.route(method, parts, body);
    if (res === "snapshot") return this.snapshot();
    if (res === "update") return this.updateRoute(method, id, body);
    if (res === "voice" || res === "transcribe" || res === "transcripts") return this.voice.route(method, parts, q, body);
    if (res === "board" || res === "threads") return this.board.route(method, parts, body);
    if (res === "matrix") return this.matrix.route(method, parts, q, body);
    if (res === "uploads") {
      const u = this.uploads.get(id) ?? notFound();
      return q.get("data") === "1" ? u : { ...u, data: undefined };
    }
    if (res === "policy") {
      if (method === "PUT") this.w.policy = body as Policy;
      return this.w.policy;
    }
    if (res === "profiles") return this.profileRoute(method, id, action, body);
    if (res === "messages") return this.w.messages;
    if ((res === "system" && (id === "tools" || id === "warnings")) || (res === "processes" && !id)) return [];
    if (res === "harnesses")
      return FALLBACK_HARNESSES.map((h) =>
        h.name === "claude"
          ? { ...h, path: "/Users/you/.local/bin/claude" }
          : h.name === "codex"
            ? { ...h, installed: true, path: "/opt/homebrew/bin/codex" }
            : h,
      );
    if (res === "discover") return { agents: this.w.discovered };
    if (res === "adopt" && method === "POST") return this.adopt(body as AdoptBody);
    if (res === "safety") {
      if (id === "guard" && method === "PUT") this.w.safety.guard = structuredClone(body as GuardSettings);
      return id === "guard" ? this.w.safety.guard : this.w.safety;
    }
    if (res === "gate") {
      const g = this.w.safety.gate;
      if (!id && method === "PUT") {
        const on = (body as { enabled: boolean }).enabled;
        if (on !== g.enabled) Object.assign(g, { enabled: on, changed_at: Date.now() });
        this.emit("gate.updated", null);
      }
      if (id === "grace" && method === "PUT") {
        const m = (body as { grace_minutes?: number }).grace_minutes;
        if (m === undefined || !(g.grace_steps ?? []).includes(m)) throw new ApiError(400, "the no-ask window must be off or one of its steps");
        g.grace_minutes = m;
        this.emit("gate.updated", null);
      }
      if (id === "lock" && method === "POST") {
        this.proofAt = 0;
        this.emit("gate.updated", null);
      }
      this.graceSync();
      return g;
    }
    if (res === "approvals") {
      if (!id) return [...this.w.approvals.values()].filter((a) => a.state === "pending");
      const a = this.w.approvals.get(id) ?? notFound();
      const d = body as DecideBody;
      const ap = a as Approval;
      if (ap.kind === "companion") return this.companion.decide(ap, d);
      ap.state = d.decision === "allow" ? "allowed" : d.decision === "deny" ? "denied" : "answered";
      ap.answer = d.answer ?? "";
      ap.decided_at = Date.now();
      this.w.approvals.delete(ap.id);
      this.emit("approval.resolved", ap, { agent_id: ap.agent_id });
      const waiter = this.waiters.get(ap.id);
      if (waiter) {
        this.waiters.delete(ap.id);
        waiter(d);
      } else {
        const stillWaiting = [...this.w.approvals.values()].some((x) => x.agent_id === ap.agent_id);
        if (!stillWaiting) this.patchAgent(ap.agent_id, { state: "working" });
        setTimeout(() => this.settle(ap, d), 1400);
      }
      return ap;
    }
    if (res === "projects") {
      if (!id && method === "POST") return this.createProject(body as CreateProjectBody);
      const p = this.w.projects.get(id) ?? notFound();
      if (method === "PATCH") {
        Object.assign(p as Project, body as PatchProjectBody, { updated_at: Date.now() });
        this.emit("project.updated", p, { project_id: id });
        return p;
      }
      if (method === "DELETE") {
        for (const a of [...this.w.agents.values()]) if (a.project_id === id) this.removeAgent(a.id);
        this.w.projects.delete(id);
        this.emit("project.removed", { id }, { project_id: id });
        return undefined;
      }
      return p;
    }
    if (res === "agents") {
      if (!id && method === "POST") return this.createAgent(body as CreateAgentBody);
      const a = (this.w.agents.get(id) ?? notFound()) as Agent;
      if (action === "processes" && method === "GET") return []; // none recorded in the mock
      if (action === "timeline") {
        const after = Number(q.get("after") ?? -1);
        const limit = Number(q.get("limit") ?? 200);
        let entries = this.tl(id).entries.filter((e) => e.seq > after);
        if (!q.has("after")) entries = entries.slice(-limit);
        else entries = entries.slice(0, limit);
        return { entries };
      }
      if (action === "uploads" && method === "POST") return this.storeUpload(id, body as { name: string; type: string; data: string });
      if (action === "send") {
        const b = body as { text: string; uploads?: string[]; thread_id?: string };
        if (id === this.w.headId) this.board.setHeadThread(b.thread_id);
        const files = (b.uploads ?? []).map((u) => this.uploads.get(u) ?? notFound());
        const note = files.length
          ? NOTE_HEADER + files.map((f) => `\n- ${f.name} (${f.type}, ${formatSize(f.size)}): ${f.path}`).join("")
          : "";
        void this.reply(id, [b.text.trim(), note].filter(Boolean).join("\n\n"));
        return undefined;
      }
      if (action === "stop") {
        this.patchAgent(id, { state: "stopped", desired: "stopped", pid: 0 });
        return undefined;
      }
      if (action === "start" || action === "restart") {
        this.patchAgent(id, { state: "starting", desired: "running", error: "" });
        setTimeout(() => this.patchAgent(id, { state: "idle", pid: 40000 + Math.floor(Math.random() * 9000) }), 1200);
        return undefined;
      }
      if (action === "interrupt") {
        this.append(id, { kind: "system", text: "Interrupted by you" });
        this.patchAgent(id, { state: "idle" });
        return undefined;
      }
      if (method === "DELETE") {
        this.removeAgent(id);
        return undefined;
      }
      if (method === "PATCH") return this.patchAgentConfig(a, body as PatchAgentBody);
      return a;
    }
    return notFound();
  }

  private profileRoute(method: string, slug: string | undefined, action: string | undefined, body: unknown): unknown {
    const ps = this.w.profiles;
    const put = (p: Profile) => {
      const def = defaultProfiles().find((d) => d.slug === p.slug);
      const editable = (x: Profile) =>
        JSON.stringify([x.name, x.description, x.level, x.instructions, x.model, x.allowed_models, x.permission_mode, x.allowed_tools, x.disallowed_tools, x.read_only, x.can_create_projects, x.can_launch]);
      p.built_in = ["head", "lead", "worker"].includes(p.slug);
      p.modified = p.built_in && !!def && editable(def) !== editable(p);
      ps.set(p.slug, p);
      this.w.policy = derivePolicy(Object.fromEntries(ps));
      this.emit("profile.updated", p);
      return p;
    };
    if (!slug) {
      if (method !== "POST") return sortProfiles([...ps.values()]);
      const b = body as Profile;
      if (!b.slug || ps.has(b.slug)) throw new ApiError(409, `A profile "${b.slug}" already exists.`);
      if (!b.name?.trim()) throw new ApiError(400, "The profile needs a name.");
      const now = Date.now();
      return put({ ...b, name: b.name.trim(), created_at: now, updated_at: now, built_in: false, modified: false });
    }
    const cur = ps.get(slug);
    if (!cur) throw new ApiError(404, `No profile "${slug}".`);
    if (action === "reset") {
      const def = defaultProfiles().find((d) => d.slug === slug);
      if (!def || !cur.built_in) throw new ApiError(400, `Only built-in profiles can be reset; ${slug} is a custom profile.`);
      return put({ ...def, created_at: cur.created_at, updated_at: Date.now() });
    }
    if (method === "PUT") return put({ ...cur, ...(body as Profile), slug, updated_at: Date.now() });
    if (method === "DELETE") {
      if (cur.built_in) throw new ApiError(403, `The built-in profile ${slug} cannot be deleted; reset it instead.`);
      const users = [...this.w.agents.values()].filter((a) => a.profile === slug).map((a) => a.name);
      if (users.length) throw new ApiError(409, `${slug} is used by ${users.join(", ")}; change their profile or remove them first.`);
      ps.delete(slug);
      this.emit("profile.removed", { slug });
      for (const p of ps.values()) if (p.can_launch?.includes(slug)) put({ ...p, can_launch: p.can_launch.filter((s) => s !== slug) });
      this.w.policy = derivePolicy(Object.fromEntries(ps));
      return undefined;
    }
    return cur;
  }

  private patchAgentConfig(a: Agent, b: PatchAgentBody) {
    const prof = this.w.profiles.get(b.profile ?? a.profile) ?? this.w.profiles.get(a.level);
    if (b.profile && prof?.level !== a.level) throw new ApiError(400, `${a.name} is a ${a.level} and keeps its level.`);
    let model = a.model;
    if (a.harness === "claude") {
      if (b.model !== undefined) model = b.model || prof?.model || a.model;
      else if (b.profile && b.profile !== a.profile) model = prof?.model ?? a.model;
      if (b.model && !(prof?.allowed_models ?? []).includes(b.model))
        throw new ApiError(403, `Model ${b.model} is not allowed for the ${prof?.slug} profile.`);
    }
    const next = { ...a, role: b.role ?? a.role, brief: b.brief ?? a.brief, profile: prof?.slug ?? a.profile, model };
    const changed = JSON.stringify(next) !== JSON.stringify(a);
    if (changed) this.patchAgent(a.id, { role: next.role, brief: next.brief, profile: next.profile, model: next.model });
    const running = ["starting", "working", "idle", "waiting"].includes(a.state);
    return {
      agent: this.w.agents.get(a.id),
      changed,
      restart_needed: changed && running,
      note: !changed ? "Nothing changed." : running ? "Saved; applies on restart." : "Saved; applies when the agent starts.",
    };
  }

  private settle(ap: Approval, d: DecideBody) {
    const a = this.w.agents.get(ap.agent_id);
    if (!a) return;
    if (ap.kind === "guard") {
      const g = ap.input as GuardHold;
      const allowed = d.decision === "allow";
      this.append(a.id, {
        kind: "system",
        text: (allowed ? "You allowed it once: " : "You denied it: ") + g.rule_name,
        tool: ap.tool,
        meta: { guard: g, approval_id: ap.id, decision: allowed ? "allowed" : "denied" },
      });
      const use = [...this.tl(a.id).entries].reverse().find((e) => e.kind === "tool_use");
      if (use) this.toolResult(a.id, use, allowed ? "" : `tmx guard: the owner denied this (${g.rule_name}).${d.message ? " They said: " + d.message : ""}`, !allowed);
      this.append(a.id, {
        kind: "assistant",
        text: allowed ? "The old export is gone. The site builds cleanly and is ready for review." : "Okay, I'll leave the old export where it is.",
      });
    } else if (ap.kind === "question") {
      this.append(a.id, { kind: "message_in", text: d.answer ?? "", meta: { from: "owner" } });
      this.append(a.id, { kind: "assistant", text: `Thanks, going with **${d.answer}**. I'll brief the workers.` });
    } else {
      const tlEntries = this.tl(a.id).entries;
      const use = [...tlEntries].reverse().find((e) => e.kind === "tool_use");
      if (use && !tlEntries.some((e) => e.kind === "tool_result" && e.tool_use_id === use.tool_use_id)) {
        this.toolResult(
          a.id,
          use,
          d.decision === "allow"
            ? use.tool === "Bash"
              ? "Database reset successful\n\nApplying migration `20261014_init`\nApplying migration `20261018_invoices`\nApplying migration `20261020_processed_events`\n\nAll migrations have been successfully applied."
              : `The file ${(use.input as { file_path?: string })?.file_path ?? ""} has been updated successfully.`
            : d.message
              ? `Permission denied: ${d.message}`
              : "Permission denied by the owner.",
          d.decision !== "allow",
        );
      }
      this.append(a.id, {
        kind: "assistant",
        text: d.decision === "allow" ? "Done. Continuing with the next step." : "Okay, I won't do that. I'll find another way.",
      });
    }
    this.append(a.id, { kind: "result", text: "", meta: { subtype: "success", num_turns: 2, duration_ms: 5200 } });
    const pending = [...this.w.approvals.values()].some((x) => x.agent_id === a.id);
    this.patchAgent(a.id, { state: pending ? "waiting" : "idle" });
  }

  private adopt(b: AdoptBody): Agent {
    const d = this.w.discovered.find((x) => x.pid === b.pid);
    if (!d) throw new ApiError(404, `pid ${b.pid} is not an agent process outside tmx`);
    if (!d.adoptable) throw new ApiError(403, `pid ${b.pid} cannot be adopted: ${d.reason}`);
    const conv = b.conversation_id || d.conversation_id || "";
    if (!conv && (d.candidates?.length ?? 0) > 1) throw new ApiError(409, `${d.conversation_source}; pick one`);
    if (!b.confirm) throw new ApiError(400, `adopting stops pid ${b.pid} with SIGTERM; repeat with confirm`);
    const base = d.cwd.split("/").pop() ?? "adopted";
    const project = b.project_id ? this.w.projects.get(b.project_id) : undefined;
    const a = this.createAgent({
      name: b.name || `${d.harness}-${base}`.slice(0, 36),
      project_id: project?.id,
      parent_id: project?.lead_id || this.w.headId,
      level: b.level ?? "worker",
      harness: d.harness,
      mode: "chat",
      model: "",
      cwd: d.cwd,
      role: `adopted from pid ${d.pid}`,
    });
    a.session_id = conv;
    this.w.discovered = this.w.discovered.filter((x) => x.pid !== b.pid);
    this.append(a.id, { kind: "system", text: `adopted from pid ${d.pid} in ${d.cwd}${conv ? ", resuming conversation " + conv : ""}` });
    return a;
  }

  private removeAgent(id: string) {
    this.w.agents.delete(id);
    this.w.timelines.delete(id);
    for (const ap of [...this.w.approvals.values()]) if (ap.agent_id === id) this.w.approvals.delete(ap.id);
    this.emit("agent.removed", { id }, { agent_id: id });
  }

  private createAgent(b: CreateAgentBody): Agent {
    if ([...this.w.agents.values()].some((a) => a.name === b.name)) throw new ApiError(409, `An agent named ${b.name} already exists.`);
    const now = Date.now();
    const a: Agent = {
      id: rid(),
      name: b.name,
      project_id: b.project_id,
      parent_id: b.parent_id,
      level: (b.profile ? this.w.profiles.get(b.profile)?.level : b.level) ?? "worker",
      profile: b.profile ?? b.level ?? "worker",
      harness: b.harness ?? "claude",
      mode: b.mode ?? "chat",
      model: b.model ?? this.w.profiles.get(b.profile ?? b.level ?? "worker")?.model ?? "",
      cwd: b.cwd ?? this.w.workspace,
      role: b.role ?? "",
      brief: b.brief ?? "",
      state: "starting",
      desired: "running",
      session_id: "",
      pid: 0,
      created_at: now,
      updated_at: now,
      last_activity_at: now,
      turns: 0,
      error: "",
      queued: 0,
    };
    this.w.agents.set(a.id, a);
    this.emit("agent.updated", a, { agent_id: a.id });
    setTimeout(() => this.patchAgent(a.id, { state: "idle", pid: 41000 + Math.floor(Math.random() * 8000) }), 1000);
    return a;
  }

  private createProject(b: CreateProjectBody): Project {
    if (!b.name?.trim()) throw new ApiError(400, "A project needs a name.");
    const slug = b.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
    if ([...this.w.projects.values()].some((p) => p.slug === slug)) throw new ApiError(409, `A project named ${slug} already exists.`);
    const now = Date.now();
    const p: Project = {
      id: "p" + rid().slice(0, 9),
      name: b.name,
      slug,
      folder: b.folder || `${this.w.workspace}/${slug}`,
      brief: b.brief ?? "",
      lead_id: "",
      status: "active",
      created_at: now,
      updated_at: now,
    };
    this.w.projects.set(p.id, p);
    const lead = this.createAgent({
      name: `${slug}-lead`.slice(0, 40),
      project_id: p.id,
      parent_id: this.w.headId,
      level: "lead",
      profile: b.profile,
      harness: b.harness,
      cwd: p.folder,
      role: `Lead of ${p.name}`,
      brief: p.brief,
    });
    p.lead_id = lead.id;
    this.emit("project.updated", p, { project_id: p.id });
    setTimeout(async () => {
      this.patchAgent(lead.id, { state: "working" });
      this.append(lead.id, { kind: "system", text: "Session started", meta: { model: lead.model } });
      await sleep(900);
      this.append(lead.id, {
        kind: "assistant",
        text: `I'm the lead for **${p.name}**. I'll read the brief, look at the folder, and propose a plan before launching workers.`,
      });
      await sleep(700);
      const use = this.toolCall(lead.id, "Bash", { command: "ls -la", description: "Look at the project folder" });
      await sleep(600);
      this.toolResult(lead.id, use, "total 0\ndrwxr-xr-x  2 you  staff  64 Oct 21 15:02 .\ndrwxr-xr-x  9 you  staff 288 Oct 21 15:02 ..");
      await sleep(600);
      this.append(lead.id, { kind: "assistant", text: "The folder is empty, so this is a fresh start. I'll draft the plan now." });
      this.append(lead.id, { kind: "result", text: "", meta: { subtype: "success", num_turns: 3, duration_ms: 3800 } });
      this.patchAgent(lead.id, { state: "idle" });
    }, 1300);
    return p;
  }

  openEvents(h: EventHandlers): EventStream {
    let open = true;
    setTimeout(() => {
      if (!open) return;
      this.streams.add(h);
      h.onOpen();
      h.onMessage({ type: "snapshot", data: structuredClone(this.snapshot()), at: Date.now() });
    }, 150);
    return {
      close: () => {
        open = false;
        this.streams.delete(h);
      },
    };
  }

  openTerminal(path: string, _cols: number, _rows: number, h: TerminalHandlers): TerminalSession {
    const login = this.accounts.openTerminal(path, h);
    if (login) return login;
    const agentId = agentOfTerminalPath(path) ?? "";
    const agent = this.w.agents.get(agentId);
    if (agent && (agent.harness === "claude" || agent.harness === "codex")) return this.screen.open(agent, h);
    const prompt = "\x1b[38;5;245mbilling-portal\x1b[0m \x1b[38;5;110m❯\x1b[0m ";
    let line = "";
    const write = (s: string) => h.onData(s);
    setTimeout(() => {
      h.onOpen();
      write("\x1b[38;5;245mLast login: Tue Oct 21 14:02 on ttys004\x1b[0m\r\n");
      write(prompt + "pnpm dev\r\n\r\n");
      write("  \x1b[1m▲ Next.js 16.2.0\x1b[0m\r\n  - Local:   http://localhost:3000\r\n  - Network: disabled\r\n\r\n");
      write(" \x1b[32m✓\x1b[0m Ready in 1.4s\r\n");
      write(" \x1b[32m✓\x1b[0m Compiled /billing in 812ms\r\n");
      write(" \x1b[38;5;245mGET /billing 200 in 914ms\x1b[0m\r\n");
      write(" \x1b[38;5;245mPOST /api/webhooks/stripe 200 in 38ms\x1b[0m\r\n");
      write("^C\r\n" + prompt);
    }, 200);
    return {
      write: (data) => {
        for (const ch of data) {
          if (ch === "\r") {
            write("\r\n");
            const cmd = line.trim();
            if (cmd === "ls") write("README.md  next.config.ts  package.json  prisma  src  tsconfig.json\r\n");
            else if (cmd === "pwd") write(`${WORKSPACE}/billing-portal\r\n`);
            else if (cmd) write(`zsh: command not found in mock: ${cmd}\r\n`);
            line = "";
            write(prompt);
          } else if (ch === "\x7f") {
            if (line) {
              line = line.slice(0, -1);
              write("\b \b");
            }
          } else if (ch >= " ") {
            line += ch;
            write(ch);
          }
        }
      },
      resize: () => {},
      close: () => {},
    };
  }

  openExternal(url: string) {
    window.open(url, "_blank", "noopener");
  }

  get scenarioName() {
    return this.scenario;
  }
}

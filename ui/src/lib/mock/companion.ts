// Companion in the mock daemon (?mock=1): connected and reachable, a finished task, one asking a
// question (Needs you), an agent's hand-off waiting for the owner, and new tasks that run through
// queued → working → completed. ?mock=companion-off: nothing installed or connected.
// The 1:1 conversation (GET /api/companion/conversation) follows the tasks the way the daemon
// does; the owner's messages (POST /api/companion/messages) go queued → working → a reply, or a
// question back when they ask to book or schedule something. ?mock=companion-chat: a longer
// conversation (the head's messages, Companion's note to the head, an error, a message in flight),
// no live script. ?mock=companion-chat-off: installed, a little history, but no key (not connected).
// ?mock=companion-connect: installed and running, no key yet; Connect with Companion
// (POST /api/companion/open-connect) "opens" Companion, and ~3.5 s later Companion hands tmx its
// key (as the owner's click there would). ?mock=companion-missing: Companion never ran here.
import { ApiError } from "../transport";
import type { CompanionPreview, CompanionSpec, CompanionStatus, CompanionTask, Effects, OpenConnectResult } from "../companion";
import { inFlight, type CompanionConversation, type CompanionEntry, type EntryFrom } from "../companion-chat";
import type { Approval, DecideBody } from "../types";
import type { MockWorld } from "./data";

const rid = () => Math.random().toString(36).slice(2, 12);
const MIN = 60_000;

const WORDS: [Effects, RegExp][] = [
  ["money", /(^|[^\p{L}_-])(money|pay|payment|paid|invoice|iban|bank|price|pricing|refund|charge|purchase|buy|order|subscription|billing|budget|cost|eur|chf|usd|[$€£])([^\p{L}_-]|$)/u],
  ["outward", /(^|[^\p{L}_-])(e-?mail|send|message|reply|post|publish|deploy|tweet|share|invite|client|customer|outside|public|announce|submit|legal|tax|contract|sign)([^\p{L}_-]|$)/u],
  ["account", /(^|[^\p{L}_-])(account|password|passkey|key|token|secret|credential|login|log in|signin|2fa|permission|access|delete|remove|disable|revoke|admin|iam)([^\p{L}_-]|$)/u],
];
const RANK: Record<Effects, number> = { none: 0, account: 1, outward: 2, money: 3 };

/** Mirror of companion.ClassifyEffects. */
export function classify(s: CompanionSpec): Effects {
  const text = [s.goal, s.context, s.where, s.success, ...(s.steps ?? [])]
    .join(" \n ")
    .toLowerCase()
    .replace(/\bsign[ -]?(in|up)\b/g, " signin ")
    .replace(/\S*(?::\/\/|[./@\\])\S*\p{L}\S*/gu, " ");
  let found: Effects = "none";
  for (const [k, rx] of WORDS) if (RANK[k] > RANK[found] && rx.test(text)) found = k;
  const declared = (s.effects || "none") as Effects;
  const d = declared in RANK ? declared : "money";
  return RANK[d] >= RANK[found] ? d : found;
}

const LABEL: Record<Effects, string> = {
  money: "may spend or move money",
  outward: "may act outward (send, post, sign, contact people)",
  account: "may touch accounts or access (log in, keys, permissions, deleting)",
  none: "no money, accounts or outward actions found",
};

/** Mirror of companion.Render. */
export function render(s: CompanionSpec, effects: Effects, info: { machine: string; agent: string; origin: "owner" | "agent" }): string {
  const out: string[] = [];
  out.push(
    info.origin === "agent"
      ? `Task from tmx on ${info.machine}, asked by its agent "${info.agent}" and approved by the owner in tmx: ${s.goal}`
      : `Task from the owner, sent through tmx on ${info.machine}: ${s.goal}`,
  );
  if (s.context) out.push(`Context: ${s.context}`);
  if (s.where) out.push(`Where: ${s.where}`);
  if (s.steps?.length) out.push("Steps:", ...s.steps.map((x, i) => `${i + 1}. ${x}`));
  if (s.success) out.push(`Done when: ${s.success}`);
  if (s.do_not_touch?.length) out.push(`Do not touch: ${s.do_not_touch.join("; ")}`);
  if (effects !== "none") out.push(`Effects: ${effects} (tmx's classification). Ask your owner before any step your own rules gate.`);
  out.push("Report back what you did and what you found, or ask if anything is unclear.");
  return out.join("\n").replace(/(password|token|api[_ -]?key)\s*[:=]\s*\S+/gi, "$1: [removed by tmx]");
}

/** Companion's canned answer to an owner's message. */
function replyFor(text: string): string {
  if (/friday/i.test(text))
    return "Friday has two meetings:\n\n- **10:00** design review with Priya (45 min)\n- **15:30** call with the accountant\n\nThe rest of the day is free.";
  if (/calendar|meeting|monday|tomorrow|today/i.test(text)) return "Nothing before noon. Your first meeting is at **14:00**: the design review with Priya (45 min).";
  const short = text.length > 80 ? text.slice(0, 79) + "…" : text;
  return `I looked into “${short}”. All done; I didn't change anything else.`;
}

export class MockCompanion {
  private tasks = new Map<string, CompanionTask>();
  private status: CompanionStatus;
  /** the conversation, oldest first */
  private convo: CompanionEntry[] = [];
  /** tasks that are the owner's messages (POST …/messages), not tasks handed over */
  private messages = new Set<string>();

  constructor(
    private w: MockWorld,
    private emit: (type: string, data: unknown, extra?: Record<string, unknown>) => void,
    scenario: string,
  ) {
    const missing = scenario === "companion-missing"; // Companion never ran for this user
    const off = scenario === "companion-off" || missing;
    const refused = scenario === "companion-key-refused"; // the key was rotated or removed in Companion
    const keyless = scenario === "companion-chat-off" || scenario === "companion-connect"; // installed, but tmx holds no key
    this.status = {
      install: {
        aios_home: "/Users/you/.aios",
        companion_dir: !missing,
        mcp_path: "/Users/you/.aios/mcps/tmx/mcp.json",
        skill_path: "/Users/you/.aios/skills/tmx/SKILL.md",
        installed: !off,
        foreign: false,
        current: !off,
        enabled: true,
        skill: !off,
        skill_foreign: false,
        command: off ? undefined : "/Applications/tmx.app/Contents/MacOS/tmx",
      },
      link_token: !off,
      connected: !off && !keyless,
      key_set_at: off || keyless ? undefined : Date.now() - 3 * 24 * 60 * MIN,
      address: { url: "http://127.0.0.1:8010", source: off ? "default" : "engine.json" },
      checked: true,
      reachable: !off,
      version: off ? undefined : "0.17.1",
      key_ok: off || keyless ? undefined : !refused,
      label: off || refused || keyless ? undefined : "tmx",
      error: off
        ? "Companion is not running (nothing answers at its address)"
        : refused
          ? "Companion does not recognise this key, or it has been stopped — ask the person to connect you again"
          : undefined,
      link_used_at: off ? undefined : Date.now() - 4 * MIN,
      active: 0,
      awaiting: 0,
      checked_at: Date.now(),
    };
    if (scenario === "companion-chat-off") this.seedChat(true);
    else if (!off && !keyless && scenario !== "empty" && scenario !== "calm") this.seed();
    if (scenario === "companion-chat") this.seedChat(false);
  }

  /** The owner's and the head's earlier exchanges with Companion (?mock=companion-chat). */
  private seedChat(short: boolean) {
    const now = Date.now();
    const head = this.w.agents.get(this.w.headId)?.name ?? "head";
    const ago = (min: number) => now - min * MIN;
    const tid = () => "ct" + rid().slice(0, 8);
    const [m1, m2, m3, m4] = [tid(), tid(), tid(), tid()];
    this.entry({ kind: "message", from: "owner", text: "What's on my calendar this Friday?", state: "completed", task_id: m1, at: ago(52) });
    this.entry({ kind: "reply", from: "companion", text: replyFor("friday"), task_id: m1, at: ago(51) });
    this.entry({
      kind: "message",
      from: "head",
      agent_name: head,
      text: "Please check whether the Stripe test-mode webhook for billing-portal still points at the staging URL.",
      state: "completed",
      task_id: m2,
      at: ago(41),
    });
    this.entry({
      kind: "reply",
      from: "companion",
      text: "It points at `https://staging.billing-portal.dev/api/stripe/webhook`; the last delivery went through 12 minutes ago.",
      task_id: m2,
      at: ago(39),
    });
    if (short) return;
    this.entry({ kind: "message", from: "companion", text: "Heads-up: the staging certificate of billing-portal.dev expires in 6 days.", at: ago(31) });
    this.entry({ kind: "message", from: "head", agent_name: head, text: "Draft a short reply to the accountant: Friday 15:30 works.", state: "failed", task_id: m3, at: ago(9) });
    this.entry({ kind: "error", from: "tmx", text: "Companion did not take the message: Companion was not running.", task_id: m3, at: ago(9) + 4000 });
    this.entry({ kind: "message", from: "owner", text: "Find the PDF of the October Swisscom bill and put it in ~/Documents/Bills.", state: "working", task_id: m4, at: ago(1) });
  }

  private entry(e: Omit<CompanionEntry, "id">): CompanionEntry {
    const x: CompanionEntry = { id: "ce" + rid().slice(0, 8), ...e };
    this.convo.push(x);
    this.convo.sort((a, b) => a.at - b.at);
    return x;
  }

  /** A task's change, mirrored into the conversation the way the daemon does it. */
  private track(t: CompanionTask) {
    const now = Date.now();
    const msg = this.messages.has(t.id);
    const own = this.convo.filter((e) => e.task_id === t.id && (e.kind === "message" || e.kind === "task"));
    if (own.length === 0) {
      const agent = this.w.agents.get(t.agent_id);
      const from: EntryFrom = t.origin === "owner" ? "owner" : agent?.level === "head" ? "head" : "agent";
      this.entry({
        kind: msg ? "message" : "task",
        from,
        agent_name: from === "owner" ? undefined : agent?.name,
        text: t.spec.goal,
        state: t.state,
        task_id: t.id,
        at: t.created_at,
      });
    } else for (const e of own) e.state = t.state;
    const qs = this.convo.filter((e) => e.kind === "question" && e.task_id === t.id);
    if (t.state === "input_required" && t.question && !qs.some((q) => q.state === "input_required"))
      this.entry({ kind: "question", from: "companion", text: t.question, task_id: t.id, state: "input_required", at: t.updated_at || now });
    if (t.state !== "input_required") for (const q of qs) if (q.state === "input_required") q.state = "answered";
    if (t.result && this.convo.filter((e) => e.kind === "reply" && e.task_id === t.id).at(-1)?.text !== t.result)
      this.entry({ kind: "reply", from: "companion", text: t.result, task_id: t.id, at: t.finished_at || now });
    if (t.error && ["failed", "refused", "unconfirmed"].includes(t.state) && !this.convo.some((e) => e.kind === "error" && e.task_id === t.id))
      this.entry({ kind: "error", from: "tmx", text: msg ? `Companion did not take the message: ${t.error}` : t.error, task_id: t.id, at: now });
  }

  private conversation(): CompanionConversation {
    const entries = this.convo.slice(-100);
    return { connected: this.status.connected, busy: this.convo.some(inFlight), entries };
  }

  /** POST /api/companion/messages: the owner writes to Companion, or answers its question. */
  private message(body: { text?: string; reply_to?: string }): CompanionTask {
    const text = (body?.text ?? "").trim();
    if (!text) throw new ApiError(400, "the message is empty");
    if (text.length > 4000) throw new ApiError(400, "the message is too long (at most 4000 characters)");
    if (!this.status.connected) throw new ApiError(409, "Companion is not connected: add its key in Settings → Companion");
    if (body.reply_to) return this.answer(body.reply_to, text);
    const now = Date.now();
    const t: CompanionTask = {
      id: "ct" + rid().slice(0, 8),
      agent_id: this.w.headId,
      origin: "owner",
      spec: { goal: text },
      text,
      effects: classify({ goal: text }),
      state: "queued",
      attempts: 0,
      created_at: now,
      updated_at: now,
    };
    this.messages.add(t.id);
    this.put(t);
    setTimeout(() => {
      const cur = this.tasks.get(t.id);
      if (cur?.state === "queued") this.put({ ...cur, state: "working", attempts: 1, sent_at: Date.now(), remote_id: "a2a-" + rid().slice(0, 3) });
    }, 700);
    setTimeout(() => {
      const cur = this.tasks.get(t.id);
      if (cur?.state !== "working") return;
      if (/\b(book|reserve|schedule)\b/i.test(text)) this.put({ ...cur, state: "input_required", question: "For which day and time, and for how many people?" });
      else this.put({ ...cur, state: "completed", finished_at: Date.now(), result: replyFor(text) });
    }, 3200);
    return this.tasks.get(t.id)!;
  }

  private answer(taskId: string, text: string): CompanionTask {
    const t = this.tasks.get(taskId);
    const waiting = this.convo.some((e) => e.kind === "question" && e.task_id === taskId && e.state === "input_required");
    if (!t || t.state !== "input_required" || !waiting) throw new ApiError(409, "Companion is not waiting for an answer on this task");
    if (t.question_approval_id) {
      const ap = this.w.approvals.get(t.question_approval_id);
      if (ap) ap.answer = text;
      this.resolve(t.question_approval_id, "answered"); // its Needs-you item goes
    }
    this.entry({ kind: "message", from: "owner", text, state: "working", task_id: t.id, at: Date.now() });
    this.put({ ...t, state: "working", question: undefined, question_approval_id: undefined });
    setTimeout(() => {
      const cur = this.tasks.get(t.id);
      if (cur?.state !== "working") return;
      const result = /webhook/i.test(cur.spec.goal)
        ? `Thanks. That endpoint's signing secret was last rotated on 14 Oct 2026, 09:12. I did not reveal or roll it.`
        : `Thanks. Done: ${cur.spec.goal.replace(/\.$/, "")}. I didn't change anything else.`;
      this.put({ ...cur, state: "completed", finished_at: Date.now(), result });
    }, 3000);
    return this.tasks.get(t.id)!;
  }

  private seed() {
    const now = Date.now();
    const head = this.w.headId;
    const lead = [...this.w.agents.values()].find((a) => a.name === "billing-lead")?.id ?? head;
    const t = (p: Partial<CompanionTask> & { spec: CompanionSpec; agent_id: string; origin: "owner" | "agent" }): CompanionTask => {
      const eff = classify(p.spec);
      const agent = this.w.agents.get(p.agent_id)?.name ?? "head";
      const task: CompanionTask = {
        id: "ct" + rid().slice(0, 8),
        text: render(p.spec, eff, { machine: this.w.machine, agent, origin: p.origin }),
        effects: eff,
        state: "queued",
        attempts: 1,
        created_at: now,
        updated_at: now,
        ...p,
      };
      this.tasks.set(task.id, task);
      this.track(task);
      return task;
    };
    t({
      origin: "owner",
      agent_id: head,
      spec: {
        goal: "Download the October Stripe invoice and put the PDF in the billing-portal docs folder",
        steps: ["Open the Stripe dashboard (test mode is fine)", "Download the October invoice as PDF", "Save it to ~/Projects/billing-portal/docs/invoices/"],
        success: "The PDF is in docs/invoices",
      },
      state: "completed",
      remote_id: "a2a-71c",
      result: "Downloaded invoice INV-2026-10 (CHF 49.00) and saved it as ~/Projects/billing-portal/docs/invoices/2026-10.pdf. Nothing was paid or changed in Stripe.",
      created_at: now - 3 * 3600_000,
      updated_at: now - 3 * 3600_000 + 4 * MIN,
      sent_at: now - 3 * 3600_000 + 2_000,
      finished_at: now - 3 * 3600_000 + 4 * MIN,
    });
    const asking = t({
      origin: "agent",
      agent_id: lead,
      spec: {
        goal: "Read the webhook signing secret's last-rotated date in the Stripe dashboard",
        context: "api-worker's webhook tests fail with signature errors; we suspect the endpoint's secret was rotated.",
        steps: ["Open Developers → Webhooks in the Stripe dashboard", "Find the endpoint for billing-portal", "Report when its signing secret was last rotated"],
        do_not_touch: ["Do not roll or reveal the secret itself"],
        where: "dashboard.stripe.com (test mode)",
      },
      state: "input_required",
      remote_id: "a2a-72d",
      question: "There are two webhook endpoints named billing-portal: one for localhost:3000 and one for staging.billing-portal.dev. Which one?",
      created_at: now - 18 * MIN,
      updated_at: now - 2 * MIN,
      sent_at: now - 17 * MIN,
    });
    const qa: Approval = {
      id: "apcq3e5r7t",
      agent_id: lead,
      kind: "companion",
      tool: "companion_question",
      input: { task_id: asking.id, question: asking.question },
      description: `Companion asks about the task “${asking.spec.goal}”. Your answer goes to Companion; deny cancels the task.`,
      question: asking.question!,
      options: null,
      state: "pending",
      answer: "",
      created_at: now - 2 * MIN,
    };
    asking.question_approval_id = qa.id;
    this.w.approvals.set(qa.id, qa);
    const waiting = t({
      origin: "agent",
      agent_id: head,
      spec: {
        goal: "Renew the domain billing-portal.dev for one year at the registrar",
        context: "It expires in 9 days; the staging site and the Stripe webhooks use it.",
        steps: ["Log in to the registrar", "Renew billing-portal.dev for 1 year with the saved card", "Report the new expiry date and the price"],
        success: "The domain shows a new expiry date",
        do_not_touch: ["Other domains", "DNS records"],
        where: "Gandi",
        effects: "money",
      },
      state: "awaiting_approval",
      attempts: 0,
      created_at: now - 4 * MIN,
      updated_at: now - 4 * MIN,
    });
    const wa: Approval = {
      id: "apct4w6y8z",
      agent_id: head,
      kind: "companion",
      tool: "companion_task",
      input: { task_id: waiting.id, text: waiting.text, effects: waiting.effects, spec: waiting.spec },
      description: `Hand this task to Companion: ${LABEL[waiting.effects]}.`,
      question: waiting.spec.goal,
      options: null,
      state: "pending",
      answer: "",
      created_at: now - 4 * MIN,
    };
    waiting.approval_id = wa.id;
    this.w.approvals.set(wa.id, wa);
  }

  private put(t: CompanionTask) {
    t.updated_at = Date.now();
    this.tasks.set(t.id, t);
    this.track(t);
    this.emit("companion.task", t, { agent_id: t.agent_id });
  }

  private counts(): CompanionStatus {
    const all = [...this.tasks.values()];
    return {
      ...this.status,
      active: all.filter((t) => ["queued", "sending", "working", "input_required"].includes(t.state)).length,
      awaiting: all.filter((t) => t.state === "awaiting_approval").length,
      checked_at: Date.now(),
    };
  }

  /** Effects of what a request would create or allow (for the simulated owner gate). */
  gatedEffects(method: string, parts: string[], body: unknown): { effects: Effects; goal: string; agent?: string; message?: boolean } | null {
    if (parts[0] === "companion" && parts[1] === "tasks" && !parts[2] && method === "POST") {
      const b = body as { spec: CompanionSpec };
      return { effects: classify(b.spec), goal: b.spec.goal };
    }
    if (parts[0] === "companion" && parts[1] === "messages" && method === "POST") {
      const text = (body as { text?: string })?.text ?? "";
      return { effects: classify({ goal: text }), goal: text, message: true };
    }
    if (parts[0] === "approvals" && parts[1] && method === "POST" && (body as DecideBody)?.decision === "allow") {
      const ap = this.w.approvals.get(parts[1]);
      if (ap?.kind === "companion" && ap.tool === "companion_task") {
        const effects = ((ap.input as { effects?: Effects })?.effects ?? "money") as Effects;
        return { effects, goal: ap.question, agent: this.w.agents.get(ap.agent_id)?.name };
      }
    }
    return null;
  }

  static gateReason(g: { effects: Effects; goal: string; agent?: string; message?: boolean }): string {
    const what = `${g.message ? "send Companion a message" : "hand Companion a task"} that ${LABEL[g.effects]}: “${g.goal.slice(0, 60)}”`;
    return g.agent ? `let ${g.agent} ${what}` : what;
  }

  private preview(body: { spec: CompanionSpec; agent_id?: string; source_approval_id?: string }): CompanionPreview {
    if (!body.spec?.goal?.trim()) throw new ApiError(400, "the task needs a goal");
    let agentId = body.agent_id || "";
    if (!agentId && body.source_approval_id) agentId = this.w.approvals.get(body.source_approval_id)?.agent_id ?? "";
    if (!agentId) agentId = this.w.headId;
    const agent = this.w.agents.get(agentId);
    if (!agent) throw new ApiError(404, `no agent named "${agentId}"`);
    const effects = classify(body.spec);
    const text = render(body.spec, effects, { machine: this.w.machine, agent: agent.name, origin: "owner" });
    return { text, effects, effects_label: LABEL[effects], text_hash: "h" + text.length, agent: agent.name, agent_id: agent.id };
  }

  private run(t: CompanionTask) {
    setTimeout(() => this.put({ ...t, state: "working", sent_at: Date.now(), remote_id: "a2a-" + rid().slice(0, 3) }), 900);
    setTimeout(() => {
      const cur = this.tasks.get(t.id);
      if (!cur || cur.state !== "working") return;
      this.put({
        ...cur,
        state: "completed",
        finished_at: Date.now(),
        result: `Done: ${cur.spec.goal.replace(/\.$/, "")}. I did not change anything else.`,
      });
    }, 5200);
  }

  route(method: string, parts: string[], body: unknown): unknown {
    const [, sub, id, action] = parts; // ["companion", sub, id, action]
    if (!sub) return this.counts();
    if (sub === "connect") {
      if (method === "DELETE") Object.assign(this.status, { connected: false, key_ok: undefined, label: undefined });
      else {
        const b = body as { token: string; url?: string };
        if (!b.token?.trim())
          throw new ApiError(400, "give the agent key Companion made for tmx (one line, no spaces); or connect without one: Settings → Companion → Connect with Companion, or tmx companion connect");
        if (b.token.trim() === "wrong")
          throw new ApiError(
            400,
            "Companion does not accept this key (it says: Companion does not recognise this key, or it has been stopped — ask the person to connect you again). Connect again: Settings → Companion → Connect with Companion (or tmx companion connect), and click Connect next to tmx in Companion.",
          );
        Object.assign(this.status, { connected: true, key_set_at: Date.now(), checked: true, reachable: true, version: "0.17.1", key_ok: true, label: "tmx", error: undefined });
        if (b.url) this.status.address = { url: b.url, source: "config" };
      }
      return this.counts();
    }
    if (sub === "open-connect" && method === "POST") return this.openConnect();
    if (sub === "install") {
      const on = method !== "DELETE";
      Object.assign(this.status.install, { installed: on, current: on, skill: on });
      this.status.link_token = on;
      return this.counts();
    }
    if (sub === "link-token") return this.counts();
    if (sub === "conversation" && method === "GET") return this.conversation();
    if (sub === "messages" && method === "POST") return this.message(body as { text?: string; reply_to?: string });
    if (sub === "preview") return this.preview(body as { spec: CompanionSpec });
    if (sub === "tasks") {
      if (!id && method === "GET") return [...this.tasks.values()].sort((a, b) => b.created_at - a.created_at);
      if (!id && method === "POST") {
        const b = body as { spec: CompanionSpec; agent_id?: string; source_approval_id?: string };
        const p = this.preview(b);
        const now = Date.now();
        const t: CompanionTask = {
          id: "ct" + rid().slice(0, 8),
          agent_id: p.agent_id,
          origin: "owner",
          source_approval_id: b.source_approval_id,
          spec: b.spec,
          text: p.text,
          effects: p.effects,
          state: "queued",
          attempts: 0,
          created_at: now,
          updated_at: now,
        };
        this.put(t);
        this.run(t);
        return t;
      }
      const t = this.tasks.get(id);
      if (!t) throw new ApiError(404, `no Companion task "${id}"`);
      if (action === "cancel") {
        if (t.question_approval_id) this.resolve(t.question_approval_id, "expired");
        this.put({ ...t, state: "canceled", finished_at: Date.now(), error: "The owner cancelled the Companion task.", question_approval_id: undefined });
        return this.tasks.get(id);
      }
      return t;
    }
    throw new ApiError(404, "Not found.");
  }

  /** POST /api/companion/open-connect: install when needed, "open" Companion (it starts when it
   * was not running), and a moment later Companion hands tmx a key, as the owner's click would. */
  private openConnect(): OpenConnectResult {
    const i = this.status.install;
    if (!i.companion_dir) throw new ApiError(412, `Companion has not been set up for this user here (no ${i.aios_home}); open Companion once, then install`);
    const installed = !i.installed || !i.current || !i.skill || !this.status.link_token;
    Object.assign(i, { installed: true, current: true, skill: true, command: "/Applications/tmx.app/Contents/MacOS/tmx" });
    this.status.link_token = true;
    if (!this.status.reachable)
      setTimeout(() => Object.assign(this.status, { reachable: true, version: "0.17.1", error: undefined, address: { url: "http://127.0.0.1:8010", source: "engine.json" } }), 1500);
    setTimeout(() => {
      Object.assign(this.status, { connected: true, key_set_at: Date.now(), reachable: true, version: "0.17.1", key_ok: true, label: "tmx", error: undefined });
      this.status.address = { url: "http://127.0.0.1:8010", source: "engine.json" };
    }, 3500);
    return { installed, opened: true };
  }

  private resolve(apId: string, state: Approval["state"]) {
    const ap = this.w.approvals.get(apId);
    if (!ap) return;
    ap.state = state;
    ap.decided_at = Date.now();
    this.w.approvals.delete(apId);
    this.emit("approval.resolved", ap, { agent_id: ap.agent_id });
  }

  /** An owner decision on an Approval of kind companion. */
  decide(ap: Approval, d: DecideBody): Approval {
    const taskId = (ap.input as { task_id?: string })?.task_id ?? "";
    const t = this.tasks.get(taskId);
    if (!t) throw new ApiError(404, "the Companion task of this item is gone");
    if (ap.tool === "companion_task") {
      if (d.decision === "allow") {
        this.resolve(ap.id, "allowed");
        const next = { ...t, state: "queued" as const };
        this.put(next);
        this.run(next);
      } else {
        this.resolve(ap.id, "denied");
        this.put({ ...t, state: "denied", finished_at: Date.now(), error: "The owner declined to hand your task to Companion." });
      }
      return ap;
    }
    if (d.decision === "deny") {
      this.resolve(ap.id, "denied");
      this.put({ ...t, state: "canceled", finished_at: Date.now(), question_approval_id: undefined, error: "The owner did not answer Companion's question and cancelled the task." });
      return ap;
    }
    ap.answer = d.answer ?? d.message ?? "";
    this.resolve(ap.id, "answered");
    const next = { ...t, state: "working" as const, question: undefined, question_approval_id: undefined };
    this.put(next);
    setTimeout(() => {
      const cur = this.tasks.get(t.id);
      if (cur?.state === "working")
        this.put({ ...cur, state: "completed", finished_at: Date.now(), result: `The ${ap.answer} endpoint's signing secret was last rotated on 14 Oct 2026, 09:12. I did not reveal or roll it.` });
    }, 3500);
    return ap;
  }
}

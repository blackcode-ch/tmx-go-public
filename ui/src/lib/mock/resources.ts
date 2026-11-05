// Resource control in the mock daemon: a queued agent, one paused by the write budget (with
// its Needs-you item), live samples with history, slots, and the routes the UI calls.
import type { Agent, Approval, BusEvent } from "../types";
import type { AgentUsage, Point, ResourceConfig, ResourcesView, Sample, SlotsView } from "../resources";
import type { MockWorld } from "./data";

const MB = 1 << 20;
const GB = 1 << 30;
const HISTORY = 60;

export const NOT_MINE = Symbol("not-mine");

type Emit = (type: string, data: unknown, extra?: Partial<BusEvent>) => void;
type Patch = (id: string, p: Partial<Agent>) => void;

// Deterministic noise so ?mock=still screenshots are stable.
function wave(seed: number, i: number): number {
  return (Math.sin(seed * 12.9898 + i * 0.37) + Math.sin(seed * 3.1 + i * 0.11) * 0.6 + 1.6) / 3.2;
}

interface Profile {
  cpu: number;
  mem: number;
  wr: number;
}

export class MockResources {
  private w: MockWorld;
  private emit: Emit;
  private patch: Patch;
  private tick = 0;
  config: ResourceConfig = {
    max_agents: 8,
    min_free_ram_mb: 1500,
    priority: "low",
    sample_secs: 5,
    daily_write_gb: 20,
    rearm_gb: 2,
    memory_cap_mb: 0,
    disk_queue: 8,
    disk_busy_secs: 30,
    disk_agent_mb_s: 50,
    saturation_pause_secs: 60,
    slots: { browser: 2, heavy: 2 },
    exempt: { "docs-writer": Date.now() + 5 * 3600_000 },
  };
  private written: Record<string, number> = {};
  private profiles: Record<string, Profile> = {};
  private history: { machine: Point[]; agents: Record<string, Point[]> } = { machine: [], agents: {} };
  private slots: SlotsView;
  sample: Sample;

  constructor(w: MockWorld, emit: Emit, patch: Patch, scenario: string) {
    this.w = w;
    this.emit = emit;
    this.patch = patch;
    const now = Date.now();
    if (scenario !== "empty") this.shape(now, scenario);
    for (const a of w.agents.values()) {
      const heavy = a.name === "test-worker" ? 3 : a.name === "api-worker" ? 1.6 : a.level === "head" ? 0.6 : 1;
      this.profiles[a.id] = { cpu: 18 * heavy, mem: (a.mode === "terminal" ? 60 : 310) * MB * Math.min(heavy, 2), wr: 0.4 * MB * heavy };
      this.written[a.id] = Math.round((a.name === "test-worker" ? 20.4 : heavy * 0.9 + (a.id.charCodeAt(2) % 7) / 4) * GB);
    }
    this.slots = {
      kinds: [
        { kind: "browser", size: 2, held: scenario === "empty" ? 0 : 1, waiting: 0 },
        { kind: "heavy", size: 2, held: scenario === "empty" ? 0 : 2, waiting: scenario === "empty" ? 0 : 1 },
      ],
      leases: [],
    };
    for (let i = HISTORY - 1; i >= 0; i--) this.sample = this.measure(now - i * 5000, HISTORY - i);
    this.sample = this.measure(now, HISTORY + 1);
  }

  /** The default world: docs-writer queued behind the limit, test-worker paused by its budget. */
  private shape(now: number, scenario: string) {
    if (scenario === "head-paused") {
      const head = this.w.agents.get(this.w.headId);
      if (head) {
        Object.assign(head, { state: "paused", paused_reason: "write_budget", paused_at: now - 2 * 60_000,
          paused_detail: "The head wrote 20.1 GB today; its daily write budget is 20.0 GB." });
      }
    }
    const queued = [...this.w.agents.values()].find((a) => a.name === "docs-writer");
    if (queued) Object.assign(queued, { state: "queued", queue_pos: 1, queue_reason: "8 of 8 agents running", pid: 0 });
    const paused = [...this.w.agents.values()].find((a) => a.name === "test-worker");
    if (paused) {
      const detail = "test-worker wrote 20.4 GB today; its daily write budget is 20.0 GB.";
      Object.assign(paused, { state: "paused", paused_reason: "write_budget", paused_detail: detail, paused_at: now - 6 * 60_000 });
      const ap: Approval = {
        id: "apbrk7w2e9",
        agent_id: paused.id,
        kind: "brake",
        tool: "write_budget",
        input: null,
        description: detail,
        question: "test-worker was paused",
        options: ["Resume", "Raise limit"],
        state: "pending",
        answer: "",
        created_at: now - 6 * 60_000,
      };
      // Paused while it waited for a permission: keep the mock simple, one item for it.
      for (const [id, x] of this.w.approvals) if (x.agent_id === paused.id) this.w.approvals.delete(id);
      this.w.approvals.set(ap.id, ap);
    }
  }

  private live(a: Agent) {
    return a.state !== "stopped" && a.state !== "exited" && a.state !== "error" && a.state !== "queued" && a.pid > 0;
  }

  private measure(at: number, i: number): Sample {
    const agents: AgentUsage[] = [];
    let cpu = 6;
    let rate = 0;
    const counts = { running: 0, queued: 0, paused: 0 };
    for (const a of this.w.agents.values()) {
      if (a.state === "queued") counts.queued++;
      const p = this.profiles[a.id] ?? { cpu: 10, mem: 200 * MB, wr: 0.2 * MB };
      const seed = a.id.charCodeAt(0) + a.id.charCodeAt(3) * 7;
      if (!this.live(a)) {
        if (this.written[a.id]) agents.push({ agent_id: a.id, cpu: 0, mem: 0, write_rate: 0, written_today: this.written[a.id], budget: 20 * GB, procs: 0 });
        continue;
      }
      counts.running++;
      const paused = a.state === "paused";
      if (paused) counts.paused++;
      const busy = a.state === "working" || a.state === "waiting" ? 1 : 0.15;
      const c = paused ? 0 : p.cpu * busy * (0.4 + wave(seed, i) * 1.4);
      const wr = paused ? 0 : p.wr * busy * wave(seed + 1, i) * 2;
      this.written[a.id] = (this.written[a.id] ?? 0) + wr * 5;
      cpu += c / 8;
      rate += wr;
      const u: AgentUsage = {
        agent_id: a.id,
        cpu: c,
        mem: p.mem * (0.85 + wave(seed + 2, i * 0.3) * 0.3),
        write_rate: wr,
        written_today: Math.round(this.written[a.id]),
        budget: 20 * GB,
        procs: a.mode === "terminal" ? 2 : 3 + (seed % 3),
      };
      const until = this.config.exempt?.[a.name];
      if (until && until > Date.now()) u.exempt_until = until;
      agents.push(u);
      this.history.agents[a.id] = (this.history.agents[a.id] ?? []).concat({ t: at, c: u.cpu, m: u.mem, w: wr }).slice(-HISTORY);
    }
    const memTotal = 32 * GB;
    const memFree = memTotal * (0.36 + wave(5, i * 0.2) * 0.08);
    const s: Sample = {
      at,
      interval: 5,
      machine: {
        cpu: Math.min(100, cpu + wave(9, i) * 8),
        cores: 12,
        mem_total: memTotal,
        mem_free: memFree,
        disk_free: 412 * GB,
        disk_total: 994 * GB,
        disk_path: this.w.workspace,
        disk_queue: 0.6 + wave(3, i),
        disk_write_rate: rate + 2 * MB,
        agent_write_rate: rate,
        saturated: false,
        written_today: Object.values(this.written).reduce((x, y) => x + y, 0),
        ...counts,
      },
      agents,
    };
    this.history.machine = this.history.machine.concat({ t: at, c: s.machine.cpu, m: memFree, w: s.machine.disk_write_rate }).slice(-HISTORY);
    return s;
  }

  /** One live sample (the mock's ticker). */
  step() {
    this.tick++;
    this.sample = this.measure(Date.now(), HISTORY + 1 + this.tick);
    this.emit("resources.sampled", this.sample);
  }

  private view(): ResourcesView {
    return {
      config: this.config,
      sample: this.sample,
      history: this.history,
      slots: this.slots,
      cost: { ticks: 412, avg_ms: 0.21, max_ms: 1.4, cpu_pct: 0.0042 },
      support: { os: "darwin", per_process: true, disk_queue: true },
    };
  }

  private resume(id: string, raise: boolean) {
    const a = this.w.agents.get(id);
    if (!a || a.state !== "paused") return;
    if (raise) this.written[id] = Math.min(this.written[id] ?? 0, 20 * GB);
    for (const ap of [...this.w.approvals.values()]) {
      if (ap.agent_id === id && ap.kind === "brake") {
        this.w.approvals.delete(ap.id);
        this.emit("approval.resolved", { ...ap, state: "allowed" }, { agent_id: id });
      }
    }
    this.patch(id, { state: "working", paused_reason: undefined, paused_detail: undefined, paused_at: undefined });
  }

  /** Answers resource routes; NOT_MINE for everything else. */
  route(method: string, parts: string[], q: URLSearchParams, body: unknown): unknown {
    const [res, id, action] = parts;
    if (res === "resources" && !id) return this.view();
    if (res === "resources" && id === "config" && method === "PUT") {
      this.config = { ...(body as ResourceConfig) };
      this.slots.kinds = this.slots.kinds.map((k) => ({ ...k, size: this.config.slots[k.kind] ?? k.size }));
      this.emit("slots.updated", this.slots);
      return this.config;
    }
    if (res === "resources" && id === "exempt") {
      const b = body as { agent: string; hours: number };
      const name = this.w.agents.get(b.agent)?.name ?? b.agent;
      const exempt = { ...(this.config.exempt ?? {}) };
      if (b.hours > 0) exempt[name] = Date.now() + b.hours * 3600_000;
      else delete exempt[name];
      this.config = { ...this.config, exempt };
      return this.config;
    }
    if (res === "slots" && !id) return this.slots;
    if (res === "agents" && id && action === "resume") {
      this.resume(id, false);
      return this.w.agents.get(id);
    }
    if (res === "agents" && id && action === "start" && q.get("force") === "1") {
      this.patch(id, { state: "starting", desired: "running", queue_pos: undefined, queue_reason: undefined });
      setTimeout(() => this.patch(id, { state: "idle", pid: 42000 + Math.floor(Math.random() * 999) }), 1000);
      return this.w.agents.get(id);
    }
    if (res === "agents" && id && action === "stop" && this.w.agents.get(id)?.state === "queued") {
      this.patch(id, { state: "stopped", desired: "stopped", queue_pos: undefined, queue_reason: undefined });
      return undefined;
    }
    if (res === "approvals" && id && this.w.approvals.get(id)?.kind === "brake") {
      const ap = this.w.approvals.get(id)!;
      const d = body as { decision: string };
      if (d.decision === "deny") {
        this.w.approvals.delete(id);
        this.emit("approval.resolved", { ...ap, state: "denied" }, { agent_id: ap.agent_id });
      } else this.resume(ap.agent_id, d.decision === "raise");
      return ap;
    }
    return NOT_MINE;
  }
}

// Resource control (DESIGN.md "Resources"): GET /api/resources once per connection, then
// resources.sampled / slots.updated events. Kept apart from the fleet store: samples arrive
// every few seconds and only the views that show numbers subscribe to them.
import { create } from "zustand";
import { useStore } from "./store";
import type { Agent, BusEvent } from "./types";

export interface ResourceConfig {
  max_agents: number;
  min_free_ram_mb: number;
  priority: "low" | "normal" | string;
  sample_secs: number;
  daily_write_gb: number;
  rearm_gb: number;
  memory_cap_mb: number;
  disk_queue: number;
  disk_busy_secs: number;
  disk_agent_mb_s: number;
  saturation_pause_secs: number;
  slots: Record<string, number>;
  exempt?: Record<string, number> | null;
}

export interface MachineUsage {
  cpu: number;
  cores: number;
  mem_total: number;
  mem_free: number;
  disk_free: number;
  disk_total: number;
  disk_path: string;
  disk_queue: number;
  disk_write_rate: number;
  agent_write_rate: number;
  saturated: boolean;
  written_today: number;
  running: number;
  queued: number;
  paused: number;
}

export interface AgentUsage {
  agent_id: string;
  cpu: number;
  mem: number;
  write_rate: number;
  written_today: number;
  budget: number;
  procs: number;
  exempt_until?: number;
}

export interface Sample {
  at: number;
  interval: number;
  machine: MachineUsage;
  agents: AgentUsage[] | null;
}

export interface Point {
  t: number;
  c: number;
  m: number;
  w: number;
}

export interface SlotKind {
  kind: string;
  size: number;
  held: number;
  waiting: number;
}

export interface SlotLease {
  id: string;
  kind: string;
  holder: string;
  agent_id?: string;
  command: string;
  state: "held" | "waiting";
  position?: number;
  since: number;
  granted_at?: number;
}

export interface SlotsView {
  kinds: SlotKind[];
  leases: SlotLease[];
}

export interface ResourcesView {
  config: ResourceConfig;
  sample: Sample;
  history: { machine: Point[] | null; agents: Record<string, Point[] | null> | null };
  slots: SlotsView;
  cost: { ticks: number; avg_ms: number; max_ms: number; cpu_pct: number };
  support: { os: string; per_process: boolean; disk_queue: boolean };
}

const HISTORY = 60;

interface ResState {
  loaded: boolean;
  /** the daemon has no resource control (older build): views hide themselves */
  unavailable: boolean;
  config: ResourceConfig | null;
  sample: Sample | null;
  usage: Record<string, AgentUsage>;
  machineHistory: Point[];
  agentHistory: Record<string, Point[]>;
  slots: SlotsView;
  cost: ResourcesView["cost"] | null;
  support: ResourcesView["support"] | null;
}

export const useResources = create<ResState>(() => ({
  loaded: false,
  unavailable: false,
  config: null,
  sample: null,
  usage: {},
  machineHistory: [],
  agentHistory: {},
  slots: { kinds: [], leases: [] },
  cost: null,
  support: null,
}));

const set = useResources.setState;

function usageMap(s: Sample): Record<string, AgentUsage> {
  const out: Record<string, AgentUsage> = {};
  for (const u of s.agents ?? []) out[u.agent_id] = u;
  return out;
}

function transport() {
  const t = useStore.getState().transport;
  if (!t) throw new Error("Not connected yet.");
  return t;
}

/** Fetches the whole view (on connect and after a reconnect). */
export async function loadResources() {
  try {
    const v = await transport().request<ResourcesView>("GET", "/api/resources");
    const agents: Record<string, Point[]> = {};
    for (const [id, h] of Object.entries(v.history?.agents ?? {})) agents[id] = h ?? [];
    set({
      loaded: true,
      unavailable: false,
      config: v.config,
      sample: v.sample,
      usage: usageMap(v.sample),
      machineHistory: v.history?.machine ?? [],
      agentHistory: agents,
      slots: normSlots(v.slots),
      cost: v.cost,
      support: v.support,
    });
  } catch (e) {
    const status = (e as { status?: number }).status;
    if (status === 501 || status === 404) set({ loaded: true, unavailable: true });
  }
}

function normSlots(v: SlotsView | null | undefined): SlotsView {
  return { kinds: v?.kinds ?? [], leases: v?.leases ?? [] };
}

function push(h: Point[] | undefined, p: Point): Point[] {
  const out = (h ?? []).concat(p);
  return out.length > HISTORY ? out.slice(out.length - HISTORY) : out;
}

/** Applies resources.sampled and slots.updated events. */
export function onResourceEvent(ev: BusEvent) {
  if (ev.type === "slots.updated") {
    set({ slots: normSlots(ev.data as SlotsView) });
    return;
  }
  const s = ev.data as Sample;
  if (!s?.machine) return;
  set((st) => {
    const agentHistory: Record<string, Point[]> = {};
    for (const u of s.agents ?? []) {
      if (u.procs > 0) agentHistory[u.agent_id] = push(st.agentHistory[u.agent_id], { t: s.at, c: u.cpu, m: u.mem, w: u.write_rate });
    }
    return {
      sample: s,
      usage: usageMap(s),
      machineHistory: push(st.machineHistory, { t: s.at, c: s.machine.cpu, m: s.machine.mem_free, w: Math.max(0, s.machine.disk_write_rate) }),
      agentHistory,
    };
  });
}

// ---------------------------------------------------------------------------------------------
// actions

const enc = encodeURIComponent;

export const resourcesApi = {
  putConfig: async (c: ResourceConfig) => {
    const saved = await transport().request<ResourceConfig>("PUT", "/api/resources/config", c);
    set({ config: saved });
    return saved;
  },
  exempt: async (agent: string, hours: number) => {
    const saved = await transport().request<ResourceConfig>("POST", "/api/resources/exempt", { agent, hours });
    set({ config: saved });
    return saved;
  },
  resume: (id: string) => transport().request<Agent>("POST", `/api/agents/${enc(id)}/resume`),
  startAnyway: (id: string) => transport().request<Agent>("POST", `/api/agents/${enc(id)}/start?force=1`),
};

// ---------------------------------------------------------------------------------------------
// formatting

/** 1536 → "1.5 KB"; GB with one decimal under 100. */
export function bytes(n: number | undefined | null, opts: { zero?: string } = {}): string {
  const v = n ?? 0;
  if (v <= 0) return opts.zero ?? "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let x = v;
  while (x >= 1024 && i < units.length - 1) {
    x /= 1024;
    i++;
  }
  const digits = i >= 3 && x < 100 ? 1 : 0;
  return `${x.toFixed(digits)} ${units[i]}`;
}

export function pct(n: number | undefined | null): string {
  const v = n ?? 0;
  if (v > 0 && v < 1) return "<1%";
  return `${Math.round(v)}%`;
}

export const PAUSE_LABEL: Record<string, string> = {
  write_budget: "Write budget",
  memory: "Memory cap",
  disk: "Disk saturated",
};

export function pauseLabel(a: Pick<Agent, "paused_reason">): string {
  return PAUSE_LABEL[a.paused_reason ?? ""] ?? "Paused";
}

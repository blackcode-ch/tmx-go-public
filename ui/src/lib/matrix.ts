// The Matrix (DESIGN.md "Matrix"): the owner's threads with everything on them. The same
// /api/matrix calls serve this page and the agents' matrix tool.
import { useStore } from "./store";
import type { LinkAxis, Thread, ThreadEntry, ThreadLink } from "./types";

export const LINK_AXES: LinkAxis[] = [
  "agent",
  "shell",
  "tool",
  "memory",
  "doc",
  "file",
  "path",
  "key",
  "secret",
  "rule",
  "person",
  "story",
  "project",
  "machine",
];

/** A thread in a list, before it is opened. */
export interface ThreadCard {
  thread: Thread;
  /** open links per axis */
  counts: Partial<Record<LinkAxis, number>>;
  last_event_at?: number;
  /** > 0: active, and quiet for longer than its lifecycle allows */
  silent_hours?: number;
}

/** One thread with what is on it. */
export interface ThreadView {
  thread: Thread;
  links: ThreadLink[];
  /** newest last; info lines only with all */
  events: ThreadEntry[];
  more?: number;
}

/** One value of an axis with the threads it is on ("see by"). */
export interface PivotRow {
  ref: string;
  label?: string;
  threads: string[];
}

export interface NewThreadBody {
  title: string;
  intent_text?: string;
  intent_source?: string;
  story?: string;
  parent?: string;
  owner?: string;
  lifecycle?: "task" | "ongoing";
}

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

const enc = encodeURIComponent;

export const matrixApi = {
  mine: () => t().request<ThreadCard[]>("GET", "/api/matrix/mine"),
  view: (id: string, opts: { past?: boolean; all?: boolean } = {}) => {
    const q = new URLSearchParams();
    if (opts.past) q.set("past", "1");
    if (opts.all) q.set("all", "1");
    return t().request<ThreadView>("GET", `/api/matrix/threads/${enc(id)}?${q}`);
  },
  pivot: (axis: LinkAxis) => t().request<PivotRow[]>("GET", `/api/matrix/pivot?axis=${enc(axis)}`),
  create: (b: NewThreadBody) => t().request<Thread>("POST", "/api/matrix/threads", b),
  setState: (id: string, b: { status?: string; lifecycle?: string; why?: string }) =>
    t().request<Thread>("POST", `/api/matrix/threads/${enc(id)}/state`, b),
  attach: (b: { thread: string; axis: LinkAxis; ref: string; role?: string; label?: string; why?: string }) =>
    t().request<ThreadLink>("POST", "/api/matrix/links", b),
  detach: (id: string) => t().request<ThreadLink>("DELETE", `/api/matrix/links/${enc(id)}`),
};

/** The bus events after which the Matrix reloads. */
export const MATRIX_EVENTS = new Set(["thread.updated", "thread.removed", "thread.entry", "thread.link"]);

export function statusLabel(th: Thread): string {
  const s = th.status ?? "active";
  return s === "waiting_on_owner" ? "waiting on you" : s;
}

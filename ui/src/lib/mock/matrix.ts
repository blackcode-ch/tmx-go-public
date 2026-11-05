// The Matrix in the mock daemon (DESIGN.md "Matrix"): the board's threads with links, the way
// internal/fleet/matrixops.go serves them (mine, one thread, see by, new thread, state, links).
// Links start from what tmx would have made itself: each project's agents on its thread.
import { ApiError } from "../transport";
import type { PivotRow, ThreadCard, ThreadView } from "../matrix";
import type { LinkAxis, Thread, ThreadLink } from "../types";
import type { MockWorld } from "./data";

const STATUSES = ["active", "paused", "waiting_on_owner", "blocked", "done"];

export class MockMatrix {
  private links: ThreadLink[] = [];
  private seeded = false;

  constructor(
    private w: MockWorld,
    private emit: (type: string, data: unknown) => void,
  ) {}

  private seed() {
    if (this.seeded) return;
    this.seeded = true;
    const at = Date.now() - 3_600_000;
    for (const th of this.w.threads) {
      if (!th.project_id) continue;
      const p = this.w.projects.get(th.project_id);
      if (p) this.add(th.id, "project", p.id, { label: p.name, role: "", by: "tmx", how: "auto", at });
      for (const a of this.w.agents.values()) {
        if (a.project_id !== th.project_id) continue;
        this.add(th.id, "agent", a.id, { label: a.name, role: a.id === p?.lead_id ? "lead" : "worker", by: "tmx", how: "auto", at });
        if (a.cwd) this.add(th.id, "path", a.cwd, { by: "tmx", how: "auto", at });
      }
    }
  }

  private add(thread: string, axis: LinkAxis, ref: string, x: Partial<ThreadLink>): ThreadLink {
    const open = this.links.find((l) => !l.ended_at && l.thread_id === thread && l.axis === axis && l.ref === ref && (l.role ?? "") === (x.role ?? ""));
    if (open) return open;
    const l: ThreadLink = { id: "l-" + Math.random().toString(36).slice(2, 12), thread_id: thread, axis, ref, by: "owner", how: "hand", at: Date.now(), ...x };
    this.links.push(l);
    return l;
  }

  private live(): Thread[] {
    return this.w.threads.filter((t) => !t.archived && t.kind !== "chat");
  }

  private card(th: Thread): ThreadCard {
    const counts: ThreadCard["counts"] = {};
    for (const l of this.links) if (!l.ended_at && l.thread_id === th.id) counts[l.axis] = (counts[l.axis] ?? 0) + 1;
    const at = this.w.entries.filter((e) => e.thread_id === th.id).map((e) => e.at);
    return { thread: th, counts, last_event_at: at.length ? Math.max(...at) : undefined };
  }

  route(method: string, parts: string[], q: URLSearchParams, body: unknown): unknown {
    this.seed();
    const [, what, id, action] = parts;
    if (what === "mine" && method === "GET") return this.live().map((t) => this.card(t));
    if (what === "pivot" && method === "GET") {
      const axis = q.get("axis") as LinkAxis;
      const rows = new Map<string, PivotRow>();
      for (const l of this.links) {
        if (l.ended_at || l.axis !== axis) continue;
        const r = rows.get(l.ref) ?? { ref: l.ref, label: l.label, threads: [] };
        if (!r.threads.includes(l.thread_id)) r.threads.push(l.thread_id);
        rows.set(l.ref, r);
      }
      return [...rows.values()];
    }
    if (what === "threads" && !id && method === "POST") {
      const b = body as { title: string; intent_text?: string; intent_source?: string; story?: string; lifecycle?: Thread["lifecycle"] };
      const title = (b.title ?? "").trim();
      if (!title) throw new ApiError(400, "The thread needs a title.");
      const now = Date.now();
      const t: Thread = { ...b, id: "t-" + Math.random().toString(36).slice(2, 12), title, kind: "intent", created_at: now, updated_at: now };
      this.w.threads.push(t);
      this.emit("thread.updated", t);
      return t;
    }
    const th = id ? this.w.threads.find((t) => t.id === id) : undefined;
    if (what === "threads" && id && !th) throw new ApiError(404, `No thread "${id}".`);
    if (what === "threads" && th && !action && method === "GET") {
      const events = this.w.entries.filter((e) => e.thread_id === th.id && (q.get("all") === "1" || e.kind !== "info"));
      const view: ThreadView = { thread: th, links: this.links.filter((l) => l.thread_id === th.id && !l.ended_at), events };
      return view;
    }
    if (what === "threads" && th && action === "state" && method === "POST") {
      const b = body as { status?: string; lifecycle?: string };
      if (b.status !== undefined && !STATUSES.includes(b.status)) throw new ApiError(400, `Unknown status "${b.status}".`);
      if (b.status !== undefined) th.status = b.status as Thread["status"];
      if (b.lifecycle !== undefined) th.lifecycle = b.lifecycle as Thread["lifecycle"];
      th.updated_at = Date.now();
      this.emit("thread.updated", th);
      return th;
    }
    if (what === "links" && !id && method === "POST") {
      const b = body as { thread: string; axis: LinkAxis; ref: string; role?: string; label?: string; why?: string };
      if (!this.w.threads.some((t) => t.id === b.thread)) throw new ApiError(404, `No thread "${b.thread}".`);
      const l = this.add(b.thread, b.axis, b.ref, { role: b.role, label: b.label, why: b.why });
      this.emit("thread.link", l);
      return l;
    }
    if (what === "links" && id && method === "DELETE") {
      const l = this.links.find((x) => x.id === id && !x.ended_at) ?? (() => { throw new ApiError(404, `No link "${id}".`); })();
      l.ended_at = Date.now();
      this.emit("thread.link", l);
      return l;
    }
    throw new ApiError(404, "Not found.");
  }
}

// The board in the mock daemon: threads, and cards built from the head's timeline the way
// internal/fleet/board.go does (splitTurns, summarize).
import { ApiError } from "../transport";
import type { Board, BoardCard, Thread, ThreadEntry, TimelineEntry } from "../types";
import type { MockWorld } from "./data";

const COLORS = ["", "slate", "red", "orange", "amber", "green", "teal", "blue", "violet", "pink"];

function splitTurns(entries: TimelineEntry[]): TimelineEntry[][] {
  const turns: TimelineEntry[][] = [];
  let prevStart = false;
  for (const e of entries) {
    if (!["user", "message_in", "assistant", "tool_use", "result", "error"].includes(e.kind)) continue;
    const start = e.kind === "user" || e.kind === "message_in";
    if (start && !prevStart) turns.push([]);
    prevStart = start;
    if (turns.length) turns[turns.length - 1].push(e);
  }
  return turns;
}

function clip(s: string, n: number) {
  s = (s ?? "").trim();
  return s.length <= n ? s : s.slice(0, n).trimEnd() + "…";
}

function summarize(turn: TimelineEntry[]): BoardCard {
  const c: BoardCard = {
    id: turn[0].seq,
    thread_id: turn[0].thread_id || "general",
    to: turn[0].seq,
    at: turn[0].at,
    updated: turn[0].at,
    kind: "mail",
    sender: "",
    prompt: "",
    tools: 0,
    done: false,
  };
  const prompts: string[] = [];
  const senders: string[] = [];
  let kindSet = false;
  const add = (k: "notes" | "asked" | "launched" | "projects" | "sent_to", v: string, unique = true) => {
    const list = (c[k] ??= []);
    if (!unique || !list.includes(v)) list.push(v);
  };
  for (const e of turn) {
    c.to = e.seq;
    c.updated = e.at;
    const meta = (e.meta ?? {}) as { from?: string; from_name?: string };
    switch (e.kind) {
      case "user":
      case "message_in": {
        let kind: BoardCard["kind"] = "mail";
        let sender = meta.from_name || meta.from || "";
        if (meta.from === "owner") [kind, sender] = ["owner", "owner"];
        else if (e.kind === "user") [kind, sender] = ["start", "tmx"];
        if (!kindSet) [c.kind, kindSet] = [kind, true];
        if (!senders.includes(sender)) senders.push(sender);
        prompts.push(e.text);
        break;
      }
      case "assistant":
        if (e.text.trim()) c.reply = e.text;
        break;
      case "tool_use": {
        c.tools++;
        const inp = (e.input ?? {}) as Record<string, string>;
        if (e.tool === "mcp__tmxg__send") {
          if (inp.to === "owner") add("notes", clip(inp.body, 300), false);
          else if (inp.to) add("sent_to", inp.to);
        } else if (e.tool === "mcp__tmxg__report") add("notes", clip(inp.body, 300), false);
        else if (e.tool === "mcp__tmxg__agent_launch" && inp.name) add("launched", inp.name);
        else if (e.tool === "mcp__tmxg__project_create" && inp.name) add("projects", inp.name);
        else if (e.tool === "mcp__tmxg__ask_owner" && inp.question) add("asked", clip(inp.question, 300), false);
        break;
      }
      case "result":
        c.done = true;
        c.error = c.error || e.is_error;
        break;
      case "error":
        c.error = true;
        break;
    }
  }
  c.sender = senders.join(", ");
  c.prompt = clip(prompts.join("\n\n"), 600);
  if (c.reply) c.reply = clip(c.reply, 600);
  return c;
}

export class MockBoard {
  constructor(
    private w: MockWorld,
    private emit: (type: string, data: unknown) => void,
  ) {}

  private head(): TimelineEntry[] {
    return this.w.timelines.get(this.w.headId)?.entries ?? [];
  }

  board(): Board {
    const known = new Set(this.w.threads.map((t) => t.id));
    const threads = this.w.threads.map((t) => {
      const p = t.project_id ? this.w.projects.get(t.project_id) : undefined;
      return t.kind === "project" ? { ...t, title: p?.name ?? t.title, archived: t.archived || !p || p.status === "archived" } : t;
    });
    const cards = splitTurns(this.head()).map(summarize);
    for (const c of cards) if (!known.has(c.thread_id)) c.thread_id = "general";
    const last = new Map<string, number>();
    for (const c of cards) last.set(c.thread_id, Math.max(last.get(c.thread_id) ?? 0, c.updated));
    for (const e of this.w.entries) last.set(e.thread_id, Math.max(last.get(e.thread_id) ?? 0, e.at));
    const ordered = threads.some((t) => (t.position ?? 0) > 0);
    threads.sort((a, b) => {
      if (ordered) {
        const pa = a.position || Infinity;
        const pb = b.position || Infinity;
        return pa - pb || a.created_at - b.created_at;
      }
      if ((a.kind === "general") !== (b.kind === "general")) return a.kind === "general" ? -1 : 1;
      return Math.max(b.created_at, last.get(b.id) ?? 0) - Math.max(a.created_at, last.get(a.id) ?? 0);
    });
    return { head_id: this.w.headId, threads, ordered, cards, entries: [...this.w.entries] };
  }

  /** The thread an owner message to the head goes to (and the head's next entries). */
  setHeadThread(id: string | undefined) {
    const b = this.w.timelines.get(this.w.headId);
    if (b) b.thread = id || "general";
  }

  route(method: string, parts: string[], body: unknown): unknown {
    const [res, id] = parts;
    if (res === "board" && !id && method === "GET") return this.board();
    if (res === "board" && id === "order" && method === "PUT") {
      const ids = (body as { ids: string[] }).ids;
      ids.forEach((tid, i) => {
        const t = this.w.threads.find((x) => x.id === tid);
        if (t) t.position = i + 1;
      });
      this.emit("board.changed", { order: "changed" });
      return { ok: true };
    }
    if (res === "board" && id === "log" && method === "POST") {
      const b = body as { thread: string; kind: ThreadEntry["kind"]; text: string; link?: string; card?: number; corrects?: string };
      const th = this.w.threads.find((x) => x.id === b.thread || x.title.toLowerCase() === (b.thread ?? "").toLowerCase());
      if (!th) throw new ApiError(404, `No topic "${b.thread}".`);
      const text = (b.text ?? "").trim();
      if (!text) throw new ApiError(400, "The entry is empty.");
      if (text.length > 400) throw new ApiError(400, "Keep it to 400 characters (one to three lines); put the details in a document and link it.");
      if (/(password|token|api[_ -]?key)\s*[:=]|\bsk-[A-Za-z0-9]{16,}/i.test(text)) throw new ApiError(400, "This looks like it contains a secret; logs never hold secrets.");
      const day = new Date().toISOString().slice(0, 10);
      const eid = btoa(unescape(encodeURIComponent(th.id + b.kind + text.toLowerCase().replace(/\s+/g, " ") + day))).replace(/[^a-z0-9]/gi, "").slice(-16);
      const have = this.w.entries.find((e) => e.id === eid);
      if (have) return have;
      const e: ThreadEntry = { id: eid, thread_id: th.id, kind: b.kind, text, link: b.link || undefined, card: b.card || undefined, by: "owner", at: Date.now(), corrects: b.corrects || undefined };
      this.w.entries.push(e);
      this.emit("thread.entry", e);
      return e;
    }
    if (res === "board" && id === "move" && method === "POST") {
      const b = body as { card: number; thread_id: string };
      const th = this.w.threads.find((t) => t.id === b.thread_id);
      if (!th) throw new ApiError(404, `No topic "${b.thread_id}".`);
      const turns = splitTurns(this.head());
      const i = turns.findIndex((t) => t[0].seq === b.card);
      if (i < 0) throw new ApiError(404, `No card ${b.card}.`);
      const to = i + 1 < turns.length ? turns[i + 1][0].seq - 1 : Infinity;
      for (const e of this.head()) if (e.seq >= b.card && e.seq <= to) e.thread_id = th.id;
      this.emit("board.changed", { thread_id: th.id });
      return { ok: true };
    }
    if (res === "threads" && !id && method === "POST") {
      const b = body as { title: string; color?: string };
      const title = (b.title ?? "").trim().replace(/\s+/g, " ");
      if (!title) throw new ApiError(400, "The topic needs a title.");
      if (this.w.threads.some((t) => !t.archived && t.title.toLowerCase() === title.toLowerCase()))
        throw new ApiError(409, `A topic "${title}" already exists.`);
      const now = Date.now();
      const t: Thread = { id: "t-" + Math.random().toString(36).slice(2, 12), title, kind: "topic", color: b.color || "", created_at: now, updated_at: now };
      this.w.threads.push(t);
      this.emit("thread.updated", t);
      return t;
    }
    if (res === "threads" && id && method === "DELETE") {
      const t = this.w.threads.find((x) => x.id === id);
      if (!t) throw new ApiError(404, `No topic "${id}".`);
      if (t.kind !== "topic") throw new ApiError(400, `"${t.title}" belongs to a ${t.kind}; archive it instead.`);
      for (const e of this.head()) if (e.thread_id === id) e.thread_id = "general";
      this.w.threads = this.w.threads.filter((x) => x.id !== id);
      this.emit("thread.removed", { id });
      return t;
    }
    if (res === "threads" && id && method === "PATCH") {
      const t = this.w.threads.find((x) => x.id === id);
      if (!t) throw new ApiError(404, `No topic "${id}".`);
      const p = body as { title?: string; color?: string; archived?: boolean };
      if (p.title !== undefined) {
        if (t.kind !== "topic") throw new ApiError(400, "Only your own topics can be renamed (rename the project instead).");
        t.title = p.title.trim();
      }
      if (p.color !== undefined) {
        if (!COLORS.includes(p.color)) throw new ApiError(400, `Unknown colour "${p.color}".`);
        t.color = p.color;
      }
      if (p.archived !== undefined) {
        if (t.kind === "general" && p.archived) throw new ApiError(400, "General cannot be archived.");
        t.archived = p.archived;
      }
      t.updated_at = Date.now();
      this.emit("thread.updated", t);
      return t;
    }
    throw new ApiError(404, "Not found.");
  }
}

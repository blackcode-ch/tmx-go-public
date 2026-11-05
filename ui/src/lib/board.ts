// The board (DESIGN.md "Board"): the head's turns as cards hanging on threads (topics). The
// daemon builds the cards (GET /api/board); this keeps them current from the event stream.
import { create } from "zustand";
import { onBusEvent, useStore } from "./store";
import type { Approval, Board, BoardCard, BusEvent, LogKind, Thread, ThreadEntry, TimelineEntry } from "./types";

interface BoardState {
  board: Board | null;
  loading: boolean;
  error: string | null;
}

export const useBoard = create<BoardState>(() => ({ board: null, loading: false, error: null }));

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

let inflight: Promise<void> | null = null;
let again = false;

/** Fetches the board; a call while one is running fetches once more after it. */
export function loadBoard(): Promise<void> {
  if (inflight) {
    again = true;
    return inflight;
  }
  useBoard.setState((s) => ({ loading: !s.board }));
  inflight = (async () => {
    try {
      const board = await t().request<Board>("GET", "/api/board");
      useBoard.setState({ board, loading: false, error: null });
    } catch (e) {
      useBoard.setState({ loading: false, error: (e as Error).message });
    } finally {
      inflight = null;
      if (again) {
        again = false;
        void loadBoard();
      }
    }
  })();
  return inflight;
}

let timer: ReturnType<typeof setTimeout> | undefined;
function refreshSoon(ms: number) {
  clearTimeout(timer);
  timer = setTimeout(() => void loadBoard(), ms);
}

const QUIET: TimelineEntry["kind"][] = ["thinking", "tool_result", "system"];

onBusEvent((ev: BusEvent) => {
  const b = useBoard.getState().board;
  if (!b) return; // never opened: nothing to keep current
  switch (ev.type) {
    case "thread.updated":
    case "thread.removed":
    case "thread.entry":
    case "board.changed":
    case "project.updated":
    case "project.removed":
      refreshSoon(120);
      break;
    case "timeline.appended": {
      const e = ev.data as TimelineEntry;
      if ((e?.agent_id || ev.agent_id) === b.head_id && !QUIET.includes(e?.kind)) refreshSoon(500);
      break;
    }
  }
});

export const boardApi = {
  createThread: (title: string, color = "") =>
    t()
      .request<Thread>("POST", "/api/threads", { title, color })
      .then(async (th) => {
        await loadBoard();
        return th;
      }),
  patchThread: (id: string, p: { title?: string; color?: string; archived?: boolean }) =>
    t()
      .request<Thread>("PATCH", `/api/threads/${encodeURIComponent(id)}`, p)
      .then(async (th) => {
        await loadBoard();
        return th;
      }),
  deleteThread: (id: string) =>
    t()
      .request<Thread>("DELETE", `/api/threads/${encodeURIComponent(id)}`)
      .then(() => loadBoard()),
  addEntry: (e: { thread: string; kind: LogKind; text: string; link?: string; card?: number; corrects?: string }) =>
    t()
      .request<ThreadEntry>("POST", "/api/board/log", e)
      .then(async (x) => {
        await loadBoard();
        return x;
      }),
  /** The owner's urgency order, most urgent first; shown at once, saved in the daemon. */
  setOrder: (ids: string[]) => {
    const b = useBoard.getState().board;
    if (b) {
      const rank = new Map(ids.map((id, i) => [id, i + 1]));
      const threads = b.threads.map((th) => (rank.has(th.id) ? { ...th, position: rank.get(th.id) } : th));
      threads.sort((x, y) => (x.position ?? Infinity) - (y.position ?? Infinity) || x.created_at - y.created_at);
      useBoard.setState({ board: { ...b, threads, ordered: true } });
    }
    return t()
      .request<void>("PUT", "/api/board/order", { ids })
      .catch((e) => {
        void loadBoard();
        throw e;
      });
  },
  moveCard: (card: number, threadId: string) =>
    t()
      .request<void>("POST", "/api/board/move", { card, thread_id: threadId })
      .then(() => loadBoard()),
};

// ---------------------------------------------------------------------------------------------
// colours

export const THREAD_COLORS = ["slate", "red", "orange", "amber", "green", "teal", "blue", "violet", "pink"] as const;

const HUES: Record<string, string> = {
  slate: "oklch(0.6 0.025 258)",
  red: "oklch(0.62 0.19 25)",
  orange: "oklch(0.68 0.16 55)",
  amber: "oklch(0.74 0.15 80)",
  green: "oklch(0.63 0.14 150)",
  teal: "oklch(0.64 0.11 190)",
  blue: "oklch(0.58 0.15 258)",
  violet: "oklch(0.58 0.17 295)",
  pink: "oklch(0.64 0.18 350)",
};

/** The thread's colour: its own, General's neutral one, or one picked from its id. */
export function threadColor(th: Pick<Thread, "id" | "kind" | "color">): string {
  if (th.color && HUES[th.color]) return HUES[th.color];
  if (th.kind === "general") return HUES.slate;
  const auto = THREAD_COLORS.filter((c) => c !== "slate");
  let h = 0;
  for (const ch of th.id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return HUES[auto[h % auto.length]];
}

export function colorValue(name: string): string {
  return HUES[name] ?? "";
}

// ---------------------------------------------------------------------------------------------
// shaping the board for the canvas

export interface Lane {
  thread: Thread;
  cards: BoardCard[];
  /** the head's open questions in this thread */
  questions: Approval[];
  /** its log, oldest first (corrected lines included; see liveEntries) */
  entries: ThreadEntry[];
  last: number;
}

/** The lines no later line corrects (a thread's or a chat topic's log). */
export function liveEntries<T extends Pick<ThreadEntry, "id" | "corrects">>(all: T[]): T[] {
  const gone = new Set(all.map((e) => e.corrects).filter(Boolean));
  return all.filter((e) => !gone.has(e.id));
}

export const LOG_KINDS: { kind: LogKind; label: string }[] = [
  { kind: "decision", label: "Decision" },
  { kind: "action", label: "Action" },
  { kind: "info", label: "Info" },
  { kind: "link", label: "Link" },
];

/** Lanes in the daemon's board order (the owner's urgency order, else General first and then
 * the most recently active). */
export function lanes(b: Board, approvals: Record<string, Approval>, showArchived: boolean): Lane[] {
  const byThread = new Map<string, BoardCard[]>();
  for (const c of b.cards) {
    const list = byThread.get(c.thread_id) ?? [];
    list.push(c);
    byThread.set(c.thread_id, list);
  }
  const logs = new Map<string, ThreadEntry[]>();
  for (const e of b.entries ?? []) {
    const list = logs.get(e.thread_id) ?? [];
    list.push(e);
    logs.set(e.thread_id, list);
  }
  const pending = Object.values(approvals).filter((a) => a.state === "pending" && a.agent_id === b.head_id);
  const out: Lane[] = [];
  for (const th of b.threads) {
    if (th.archived && !showArchived) continue;
    const cards = byThread.get(th.id) ?? [];
    const entries = logs.get(th.id) ?? [];
    const questions = pending.filter((a) => (a.thread_id || "general") === th.id);
    // A project's or a chat's string shows once there is something on it.
    if ((th.kind === "project" || th.kind === "chat") && !cards.length && !questions.length && !entries.length) continue;
    out.push({ thread: th, cards, questions, entries, last: Math.max(th.created_at, ...cards.map((c) => c.updated), ...entries.map((e) => e.at)) });
  }
  return out;
}

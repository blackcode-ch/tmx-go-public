// The head on the Matrix (DESIGN.md "Matrix", "The head's view"): what used to be the Board, folded
// into the Matrix on 3 Nov. Pure shaping only, so node can test it: the owner's urgency order over
// the Matrix's threads, archived threads when asked for, and the head's open questions per thread.
import type { Approval, Board, BoardCard } from "./types";
import type { ThreadCard } from "./matrix";

/** The Matrix's threads in the owner's urgency order (the Board's), plus the archived ones when
 * asked for. Threads the order doesn't know keep their place after the ordered ones. */
export function withBoard(cards: ThreadCard[], board: Board | null, showArchived: boolean): ThreadCard[] {
  if (!board) return cards;
  const out = [...cards];
  if (showArchived) {
    const have = new Set(cards.map((c) => c.thread.id));
    for (const th of board.threads) if (th.archived && !have.has(th.id)) out.push({ thread: th, counts: {} });
  }
  if (!board.ordered) return out;
  const rank = new Map(board.threads.map((th, i) => [th.id, i]));
  return out
    .map((c, i) => ({ c, i }))
    .sort((a, b) => (rank.get(a.c.thread.id) ?? Infinity) - (rank.get(b.c.thread.id) ?? Infinity) || a.i - b.i)
    .map((x) => x.c);
}

/** The head's open questions by thread (a question asked outside any thread is General's). */
export function questionsByThread(approvals: Record<string, Approval>, headId: string | undefined): Map<string, Approval[]> {
  const m = new Map<string, Approval[]>();
  if (!headId) return m;
  for (const a of Object.values(approvals)) {
    if (a.state !== "pending" || a.agent_id !== headId) continue;
    const id = a.thread_id || "general";
    m.set(id, [...(m.get(id) ?? []), a]);
  }
  return m;
}

/** The head's turn cards by thread, oldest first. */
export function cardsByThread(board: Board | null): Map<string, BoardCard[]> {
  const m = new Map<string, BoardCard[]>();
  for (const c of board?.cards ?? []) m.set(c.thread_id, [...(m.get(c.thread_id) ?? []), c]);
  return m;
}

/** The urgency order after moving one thread up (delta < 0, more urgent) or down; null when it
 * can't move that way. */
export function reorder(ids: string[], id: string, delta: number): string[] | null {
  const from = ids.indexOf(id);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= ids.length) return null;
  const out = [...ids];
  out.splice(from, 1);
  out.splice(to, 0, id);
  return out;
}

/** Old addresses of the Board now open the Matrix (bookmarks, notifications, the phone). */
export function legacyPath(path: string): string | null {
  const [p, q] = path.split("?");
  return p.replace(/\/+$/, "") === "/board" ? "/matrix" + (q ? "?" + q : "") : null;
}

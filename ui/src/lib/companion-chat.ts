// Companion 1:1 (DESIGN.md "Companion"): the conversation between the owner, the head and
// Companion (GET /api/companion/conversation): the owner's and the head's messages, Companion's
// replies, questions and messages to the head, tasks handed to it, and tmx's error notes.
// This file holds the logic only (no React, no transport): the store's shape, the event-driven
// refresh, reply mode and the unread mark, so test/companion-chat.test.mjs can drive it in node.
// lib/companion.ts wires it to zustand, the daemon and the event stream.
import type { CompanionTaskState } from "./companion";

export type EntryKind = "message" | "task" | "reply" | "question" | "error";
export type EntryFrom = "owner" | "head" | "agent" | "companion" | "tmx";
/** a task's state on messages and tasks; a question is input_required while pending, else answered */
export type EntryState = CompanionTaskState | "answered";

export interface CompanionEntry {
  /** stable across calls */
  id: string;
  kind: EntryKind;
  from: EntryFrom;
  /** the head's or an agent's name */
  agent_name?: string;
  text: string;
  state?: EntryState;
  task_id?: string;
  /** Unix ms */
  at: number;
}

export interface CompanionConversation {
  /** Companion is connected (tmx holds its key) */
  connected: boolean;
  /** something is in flight */
  busy: boolean;
  /** oldest first */
  entries: CompanionEntry[] | null;
}

/** What the owner is answering: a question of Companion's, by its task. */
export interface ReplyTarget {
  taskId: string;
  text: string;
}

export interface ConvState {
  connected: boolean;
  busy: boolean;
  entries: CompanionEntry[];
  /** a fetch has answered (or failed) at least once */
  loaded: boolean;
  loading: boolean;
  /** false: this daemon has no conversation (an older tmx); null until known */
  available: boolean | null;
  /** the last fetch's error, "" when it worked */
  error: string;
  replyTo: ReplyTarget | null;
  /** the newest Companion entry the owner has seen (its `at`), kept per viewer */
  seenAt: number;
}

/** What POST /api/companion/messages answers with (the task); only these fields are read. */
export interface SentTask {
  id?: string;
  state?: CompanionTaskState;
}

/** The part of a zustand store the controller needs. */
export interface StoreApi<T> {
  getState(): T;
  setState(partial: Partial<T> | ((s: T) => Partial<T>)): void;
}

type KV = Pick<Storage, "getItem" | "setItem">;

export const SEEN_KEY = "tmx.companion.seen";
export const CONVERSATION_LIMIT = 100;
export const REFRESH_DEBOUNCE_MS = 250;

const IN_FLIGHT: EntryState[] = ["queued", "sending", "working"];

/** A message or task still on its way or being worked on. */
export function inFlight(e: Pick<CompanionEntry, "kind" | "state">): boolean {
  return (e.kind === "message" || e.kind === "task") && !!e.state && IN_FLIGHT.includes(e.state);
}

/** A question of Companion's still waiting for an answer. */
export function pendingQuestion(e: Pick<CompanionEntry, "kind" | "state" | "task_id">): boolean {
  return e.kind === "question" && e.state === "input_required" && !!e.task_id;
}

/** Entries from Companion newer than what the owner has seen. */
export function unreadCount(entries: readonly CompanionEntry[], seenAt: number): number {
  let n = 0;
  for (const e of entries) if (e.from === "companion" && e.at > seenAt) n++;
  return n;
}

/** The newest `at` among Companion's entries (0 when none): server times only, so a clock that
 * runs ahead on this device never hides a later reply. */
export function newestCompanionAt(entries: readonly CompanionEntry[]): number {
  let at = 0;
  for (const e of entries) if (e.from === "companion" && e.at > at) at = e.at;
  return at;
}

/** Whether a bus event means the conversation changed. */
export function refreshesOn(ev: { type: string; data?: unknown }): boolean {
  if (ev.type === "companion.task") return true;
  if (ev.type === "message.created") return (ev.data as { from?: string } | null)?.from === "companion";
  return false;
}

/** The message or task a reply or question belongs to (by task), when it is in the list. */
export function originOf(entries: readonly CompanionEntry[], e: CompanionEntry): CompanionEntry | undefined {
  if (!e.task_id) return undefined;
  return entries.find((x) => x.task_id === e.task_id && (x.kind === "message" || x.kind === "task"));
}

export function readSeen(storage: () => KV | null | undefined): number {
  try {
    const v = Number(storage()?.getItem(SEEN_KEY) ?? 0);
    return Number.isFinite(v) && v > 0 ? v : 0;
  } catch {
    return 0;
  }
}

export function initialConvState(seenAt = 0): ConvState {
  return { connected: false, busy: false, entries: [], loaded: false, loading: false, available: null, error: "", replyTo: null, seenAt };
}

/** The reply chip stays while its question still waits; it goes once the list shows the question
 * answered (in Needs you, or by the task ending). A question older than the list keeps it. */
function keepReply(replyTo: ReplyTarget | null, entries: readonly CompanionEntry[]): ReplyTarget | null {
  if (!replyTo) return null;
  const qs = entries.filter((e) => e.kind === "question" && e.task_id === replyTo.taskId);
  return qs.length === 0 || qs.some(pendingQuestion) ? replyTo : null;
}

function statusOf(e: unknown): number {
  return typeof (e as { status?: unknown })?.status === "number" ? (e as { status: number }).status : 0;
}

export interface CompanionChatDeps {
  store: StoreApi<ConvState>;
  fetch: () => Promise<CompanionConversation>;
  post: (text: string, replyTo?: string) => Promise<SentTask | null | undefined>;
  /** where the seen mark is kept (localStorage); may throw or be missing */
  storage?: () => KV | null | undefined;
  debounceMs?: number;
  now?: () => number;
}

export function createCompanionChat(d: CompanionChatDeps) {
  const { store } = d;
  const storage = d.storage ?? (() => null);
  const now = d.now ?? Date.now;
  let seq = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;

  /** Fetches the conversation; only the newest call's answer lands. */
  async function load(): Promise<void> {
    const mine = ++seq;
    store.setState({ loading: true });
    try {
      const c = await d.fetch();
      if (mine !== seq) return;
      const entries = c?.entries ?? [];
      store.setState((s) => ({
        connected: !!c?.connected,
        busy: !!c?.busy,
        entries,
        loaded: true,
        loading: false,
        available: true,
        error: "",
        replyTo: keepReply(s.replyTo, entries),
      }));
    } catch (e) {
      if (mine !== seq) return;
      const gone = statusOf(e) === 404;
      store.setState((s) => ({
        loaded: true,
        loading: false,
        available: gone ? false : s.available,
        error: gone ? "" : (e as Error)?.message || "Couldn't load the conversation.",
      }));
    }
  }

  /** Several events in a row cause one fetch. */
  function refreshSoon(ms = d.debounceMs ?? REFRESH_DEBOUNCE_MS) {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      void load();
    }, ms);
  }

  function onEvent(ev: { type: string; data?: unknown }) {
    if (ev.type === "snapshot") void load(); // every (re)connect: whatever was missed meanwhile
    else if (refreshesOn(ev)) refreshSoon();
  }

  /** Sends the owner's message, or an answer to the question of task replyTo. Throws on failure
   * (the composer keeps the text and shows the error). */
  async function send(text: string, replyTo?: string): Promise<SentTask | null | undefined> {
    const body = text.trim();
    if (!body) throw new Error("Write something first.");
    let task: SentTask | null | undefined;
    try {
      task = await d.post(body, replyTo || undefined);
    } catch (e) {
      if (statusOf(e) === 409) void load(); // the question was answered meanwhile: the list shows it
      throw e;
    }
    const at = now();
    store.setState((s) => ({
      // shown at once; the next fetch replaces it with the daemon's entry
      entries: [
        ...s.entries.map((e) => (replyTo && e.task_id === replyTo && pendingQuestion(e) ? { ...e, state: "answered" as const } : e)),
        { id: `local:${task?.id ?? at}`, kind: "message" as const, from: "owner" as const, text: body, state: task?.state ?? "queued", task_id: task?.id ?? replyTo, at },
      ],
      busy: true,
      replyTo: replyTo && s.replyTo?.taskId === replyTo ? null : s.replyTo,
    }));
    void load();
    return task;
  }

  /** Puts the composer into reply mode for a waiting question. */
  function answer(e: CompanionEntry) {
    if (!pendingQuestion(e)) return;
    store.setState({ replyTo: { taskId: e.task_id!, text: e.text } });
  }

  function clearReply() {
    store.setState({ replyTo: null });
  }

  /** The owner has seen everything in the list (the page calls it while visible). */
  function markSeen() {
    const s = store.getState();
    const at = newestCompanionAt(s.entries);
    if (at <= s.seenAt) return;
    store.setState({ seenAt: at });
    try {
      storage()?.setItem(SEEN_KEY, String(at));
    } catch {
      /* private window or blocked storage: the mark lasts for this page only */
    }
  }

  /** Another window of this viewer moved the mark. */
  function syncSeen() {
    const at = readSeen(storage);
    if (at > store.getState().seenAt) store.setState({ seenAt: at });
  }

  return { load, refreshSoon, onEvent, send, answer, clearReply, markSeen, syncSeen };
}

export type CompanionChat = ReturnType<typeof createCompanionChat>;

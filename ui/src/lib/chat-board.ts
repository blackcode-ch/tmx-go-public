// Chat boards (DESIGN.md "Chat boards"): each chat's topics, their logs and each topic's latest
// messages (GET /api/chats/{id}/board), kept current from the chat.* events. Topics, moves and
// logs are shared with the chat's members; the order of the strings is yours alone.
import { create } from "zustand";
import { onBusEvent, useStore } from "./store";
import type { BusEvent, LogKind } from "./types";
import type { ChatBoard, ChatMessage, ChatTopic, ChatTopicEntry } from "./chat-types";
import { CHAT_GENERAL_TOPIC, CHAT_SYSTEM } from "./chat-types";

interface ChatBoardsState {
  boards: Record<string, ChatBoard>;
  errors: Record<string, string>;
}

export const useChatBoards = create<ChatBoardsState>(() => ({ boards: {}, errors: {} }));

const set = useChatBoards.setState;
const get = useChatBoards.getState;
const enc = encodeURIComponent;

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

const inflight = new Map<string, Promise<void>>();
const again = new Set<string>();

/** Fetches a chat's board; a call while one is running fetches once more after it. */
export function loadChatBoard(chatId: string): Promise<void> {
  const cur = inflight.get(chatId);
  if (cur) {
    again.add(chatId);
    return cur;
  }
  const p = (async () => {
    try {
      const b = await t().request<ChatBoard>("GET", `/api/chats/${enc(chatId)}/board`);
      set((s) => ({ boards: { ...s.boards, [chatId]: b }, errors: { ...s.errors, [chatId]: "" } }));
    } catch (e) {
      set((s) => ({ errors: { ...s.errors, [chatId]: (e as Error).message } }));
    } finally {
      inflight.delete(chatId);
      if (again.delete(chatId)) void loadChatBoard(chatId);
    }
  })();
  inflight.set(chatId, p);
  return p;
}

const timers = new Map<string, ReturnType<typeof setTimeout>>();
function refreshSoon(chatId: string, ms: number) {
  clearTimeout(timers.get(chatId));
  timers.set(
    chatId,
    setTimeout(() => void loadChatBoard(chatId), ms),
  );
}

function patchBoard(chatId: string, fn: (b: ChatBoard) => ChatBoard) {
  const b = get().boards[chatId];
  if (b) set((s) => ({ boards: { ...s.boards, [chatId]: fn(b) } }));
}

function upsertTopic(b: ChatBoard, x: ChatTopic): ChatBoard {
  const i = b.topics.findIndex((y) => y.id === x.id);
  const topics = [...b.topics];
  if (i >= 0) topics[i] = { ...topics[i], ...x, messages: topics[i].messages, position: topics[i].position };
  else topics.push({ ...x, messages: x.messages ?? 0 });
  return { ...b, topics };
}

onBusEvent((ev: BusEvent) => {
  switch (ev.type) {
    case "snapshot": // (re)connected: catch up, and retry the ones that failed
      for (const id of new Set([...Object.keys(get().boards), ...Object.keys(get().errors).filter((k) => get().errors[k])])) void loadChatBoard(id);
      break;
    case "chat.topic": {
      const x = ev.data as ChatTopic;
      if (!x?.chat_id || !get().boards[x.chat_id]) break;
      patchBoard(x.chat_id, (b) => upsertTopic(b, x));
      refreshSoon(x.chat_id, 300);
      break;
    }
    case "chat.topic.entry": {
      const e = ev.data as ChatTopicEntry;
      if (!e?.chat_id) break;
      patchBoard(e.chat_id, (b) => (b.entries.some((y) => y.id === e.id) ? b : { ...b, entries: [...b.entries, e] }));
      break;
    }
    case "chat.board": {
      const id = (ev.data as { chat_id?: string })?.chat_id;
      if (id && get().boards[id]) refreshSoon(id, 100);
      break;
    }
    case "chat.message": {
      const m = ev.data as ChatMessage;
      if (!m?.id || m.sender === CHAT_SYSTEM || !get().boards[m.chat_id]) break;
      patchBoard(m.chat_id, (b) => {
        const i = b.messages.findIndex((y) => y.id === m.id);
        const messages = [...b.messages];
        if (i >= 0) messages[i] = m;
        else messages.push(m);
        messages.sort((x, y) => x.seq - y.seq);
        return { ...b, messages };
      });
      refreshSoon(m.chat_id, 800); // the counts
      break;
    }
    case "chat.removed": {
      const id = (ev.data as { id?: string })?.id;
      if (!id) break;
      set((s) => {
        const boards = { ...s.boards };
        delete boards[id];
        return { boards };
      });
      break;
    }
  }
});

export const chatBoardApi = {
  createTopic: async (chatId: string, title: string, color = "") => {
    const x = await t().request<ChatTopic>("POST", `/api/chats/${enc(chatId)}/topics`, { title, color });
    patchBoard(chatId, (b) => upsertTopic(b, x));
    void loadChatBoard(chatId);
    return x;
  },
  patchTopic: async (chatId: string, id: string, p: { title?: string; color?: string; archived?: boolean }) => {
    const x = await t().request<ChatTopic>("PATCH", `/api/chats/${enc(chatId)}/topics/${enc(id)}`, p);
    patchBoard(chatId, (b) => upsertTopic(b, x));
    return x;
  },
  addEntry: async (chatId: string, e: { topic: string; kind: LogKind; text: string; link?: string; corrects?: string }) => {
    const x = await t().request<ChatTopicEntry>("POST", `/api/chats/${enc(chatId)}/board/log`, e);
    patchBoard(chatId, (b) => (b.entries.some((y) => y.id === x.id) ? b : { ...b, entries: [...b.entries, x] }));
    return x;
  },
  /** Your own order of the strings, most urgent first; shown at once, saved on this machine. */
  setOrder: (chatId: string, ids: string[]) => {
    patchBoard(chatId, (b) => {
      const rank = new Map(ids.map((id, i) => [id, i + 1]));
      const topics = b.topics.map((x) => (rank.has(x.id) ? { ...x, position: rank.get(x.id) } : x));
      topics.sort((x, y) => (x.position ?? Infinity) - (y.position ?? Infinity) || x.created_at - y.created_at);
      return { ...b, topics, ordered: true };
    });
    return t()
      .request<void>("PUT", `/api/chats/${enc(chatId)}/board/order`, { ids })
      .catch((e) => {
        void loadChatBoard(chatId);
        throw e;
      });
  },
  /** Files messages under a topic ("general" = General) for everyone in the chat. */
  move: async (chatId: string, messages: string[], topic: string) => {
    await t().request<void>("POST", `/api/chats/${enc(chatId)}/board/move`, { messages, topic });
    patchBoard(chatId, (b) => ({ ...b, messages: b.messages.map((m) => (messages.includes(m.id) ? { ...m, topic: topic === CHAT_GENERAL_TOPIC ? "" : topic } : m)) }));
    refreshSoon(chatId, 200);
  },
};

// ---------------------------------------------------------------------------------------------
// shaping a board for the canvas

/** A card: one message, or a run of one sender's messages close in time. */
export interface ChatCard {
  /** its first message's id */
  id: string;
  messages: ChatMessage[];
  at: number;
  last: number;
}

export interface ChatLane {
  topic: ChatTopic;
  cards: ChatCard[];
  /** its log, oldest first (corrected lines included) */
  entries: ChatTopicEntry[];
  /** messages on it here, and how many of them the board loaded */
  total: number;
  loaded: number;
}

/** The topic a message hangs on: its own if the board knows it, else General. */
export function laneOf(m: Pick<ChatMessage, "topic">, known: Set<string>): string {
  return m.topic && known.has(m.topic) ? m.topic : CHAT_GENERAL_TOPIC;
}

const RUN_GAP = 5 * 60_000;
const RUN_MAX = 6;

function sameRun(a: ChatMessage, b: ChatMessage): boolean {
  return a.sender === b.sender && a.sender_agent === b.sender_agent && b.received_at - a.received_at < RUN_GAP;
}

/** Lanes in your order (else General first, then the most recently active), archived ones
 * only when asked. */
export function chatLanes(b: ChatBoard, showArchived: boolean): ChatLane[] {
  const known = new Set(b.topics.filter((x) => x.kind !== "general").map((x) => x.id));
  const byLane = new Map<string, ChatMessage[]>();
  for (const m of b.messages) {
    if (m.sender === CHAT_SYSTEM) continue;
    const id = laneOf(m, known);
    const list = byLane.get(id) ?? [];
    list.push(m);
    byLane.set(id, list);
  }
  const logs = new Map<string, ChatTopicEntry[]>();
  for (const e of b.entries) {
    const list = logs.get(e.topic_id) ?? [];
    list.push(e);
    logs.set(e.topic_id, list);
  }
  const out: ChatLane[] = [];
  for (const topic of b.topics) {
    if (topic.archived && !showArchived) continue;
    const msgs = byLane.get(topic.id) ?? [];
    const cards: ChatCard[] = [];
    for (const m of msgs) {
      const cur = cards[cards.length - 1];
      const prev = cur?.messages[cur.messages.length - 1];
      if (cur && prev && cur.messages.length < RUN_MAX && sameRun(prev, m)) {
        cur.messages.push(m);
        cur.last = m.received_at;
      } else cards.push({ id: m.id, messages: [m], at: m.received_at, last: m.received_at });
    }
    out.push({ topic, cards, entries: logs.get(topic.id) ?? [], total: Math.max(topic.messages, msgs.length), loaded: msgs.length });
  }
  return out;
}

/** Who filed a log line: "you", "Alex", "Alex's scout". */
export function entryBy(e: ChatTopicEntry): string {
  const who = e.by === "me" ? "you" : e.by_name || "someone";
  return e.by_agent ? `${who === "you" ? "your" : who + "'s"} ${e.by_agent}` : who;
}

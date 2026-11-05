// Chats with other people's tmx installs: contacts, DMs and groups, kept current by the
// chat.* and contact.* BusEvents (DESIGN.md "Chats"). Separate from the fleet store; it hooks
// into the same event stream and reloads on every (re)connect.
import { create } from "zustand";
import { ApiError } from "./transport";
import { onBusEvent, useStore } from "./store";
import { ownerAction, withConfirmation } from "./owner-gate";
import type { BusEvent } from "./types";
import type { Chat, ChatFile, ChatMessage, ChatPage, Contact, Invite, InviteKind, InvitePreview, JoinResult, Mentionable, Unread } from "./chat-types";
import { CHAT_SYSTEM } from "./chat-types";

export interface MessageList {
  list: ChatMessage[];
  hasMore: boolean;
  loading: boolean;
  loaded: boolean;
}

interface ChatState {
  /** null until the first answer; false = peer-to-peer is off (503) */
  enabled: boolean | null;
  offMessage: string;
  chats: Record<string, Chat>;
  contacts: Record<string, Contact>;
  messages: Record<string, MessageList>;
  invite: { open: boolean; kind: InviteKind; groupId?: string };
  join: { open: boolean; link?: string };
}

export const useChats = create<ChatState>(() => ({
  enabled: null,
  offMessage: "",
  chats: {},
  contacts: {},
  messages: {},
  invite: { open: false, kind: "dm" },
  join: { open: false },
}));

const set = useChats.setState;
const get = useChats.getState;

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}
const enc = encodeURIComponent;

export const chatApi = {
  createInvite: (kind: InviteKind, groupId?: string) => t().request<Invite>("POST", "/api/invites", { kind, group_id: groupId }),
  invites: () => t().request<Invite[]>("GET", "/api/invites"),
  revokeInvite: (id: string) => t().request<void>("DELETE", `/api/invites/${enc(id)}`),
  preview: (link: string) => t().request<InvitePreview>("POST", "/api/invites/preview", { link }),
  join: (link: string) => t().request<JoinResult>("POST", "/api/invites/join", { link }),

  patchContact: (peer: string, b: { name?: string; note?: string }) => t().request<Contact>("PATCH", `/api/contacts/${enc(peer)}`, b),
  removeContact: (peer: string) => t().request<void>("DELETE", `/api/contacts/${enc(peer)}`),

  createGroup: (name: string) => t().request<Chat>("POST", "/api/chats", { kind: "group", name }),
  openDM: (peer: string) => t().request<Chat>("POST", "/api/chats", { kind: "dm", peer_id: peer }),
  patchChat: (id: string, b: { agent_id?: string; name?: string }) => t().request<Chat>("PATCH", `/api/chats/${enc(id)}`, b),
  deleteChat: (id: string) => t().request<void>("DELETE", `/api/chats/${enc(id)}`),
  removeMember: (id: string, peer: string) => t().request<Chat>("DELETE", `/api/chats/${enc(id)}/members/${enc(peer)}`),
  leave: (id: string) => t().request<Chat>("POST", `/api/chats/${enc(id)}/leave`),
  /** Offers a member the admin role: the daemon always asks for Touch ID / Windows Hello first (202). */
  offerAdmin: (id: string, peer: string, thenLeave: boolean) =>
    ownerAction((c) => t().request<Chat>("POST", withConfirmation(`/api/chats/${enc(id)}/admin`, c), { peer_id: peer, then_leave: thenLeave })),
  cancelAdmin: (id: string) => t().request<Chat>("DELETE", `/api/chats/${enc(id)}/admin`),
  read: (id: string) => t().request<Chat>("POST", `/api/chats/${enc(id)}/read`),
  unread: () => t().request<Unread>("GET", "/api/chats/unread"),
  messages: (id: string, before = 0, limit = 100) =>
    t().request<ChatPage>("GET", `/api/chats/${enc(id)}/messages?limit=${limit}${before ? `&before=${before}` : ""}`),
  /** topic: a board topic's id ("" = General) */
  send: (id: string, body: string, files?: string[], topic?: string) =>
    t().request<ChatMessage>("POST", `/api/chats/${enc(id)}/messages`, { body, ...(files?.length ? { files } : {}), ...(topic ? { topic } : {}) }),
  /** Uploads a file for the next message (data = base64). */
  uploadFile: (id: string, f: { name: string; type: string; data: string }) =>
    t().request<ChatFile>("POST", `/api/chats/${enc(id)}/files`, f),
  file: (id: string, file: string, data = false) =>
    t().request<ChatFile & { data?: string }>("GET", `/api/chats/${enc(id)}/files/${enc(file)}${data ? "?data=1" : ""}`),
  mentionable: (id: string) => t().request<{ mentionable: Mentionable[] }>("GET", `/api/chats/${enc(id)}/mentionable`),
  p2p: () => t().request<{ name?: string; enabled: boolean; peer_id: string }>("GET", "/api/p2p"),
  setName: (name: string) => t().request<{ name: string }>("PUT", "/api/p2p/name", { name }),
};

// ---------------------------------------------------------------------------------------------
// loading

function offFrom(e: unknown): boolean {
  if (e instanceof ApiError && e.status === 503) {
    set({ enabled: false, offMessage: e.message });
    return true;
  }
  return false;
}

export async function loadChats() {
  try {
    const [chats, contacts] = await Promise.all([
      t().request<Chat[]>("GET", "/api/chats"),
      t().request<Contact[]>("GET", "/api/contacts"),
    ]);
    const byId: Record<string, Chat> = {};
    for (const c of chats ?? []) byId[c.id] = c;
    const byPeer: Record<string, Contact> = {};
    for (const c of contacts ?? []) byPeer[c.peer_id] = c;
    set({ enabled: true, offMessage: "", chats: byId, contacts: byPeer });
    // Catch up open chats (messages that arrived while the stream was down).
    for (const id of Object.keys(get().messages)) if (byId[id]) void loadMessages(id);
  } catch (e) {
    offFrom(e);
  }
}

export async function loadMessages(chatId: string) {
  set((s) => ({ messages: { ...s.messages, [chatId]: { ...(s.messages[chatId] ?? empty()), loading: true } } }));
  try {
    const page = await chatApi.messages(chatId, 0, 100);
    set((s) => {
      const prev = s.messages[chatId] ?? empty();
      return {
        messages: {
          ...s.messages,
          [chatId]: { list: merge(prev.list, page.messages ?? []), hasMore: prev.loaded ? prev.hasMore : page.has_more, loading: false, loaded: true },
        },
      };
    });
  } catch (e) {
    if (!offFrom(e)) set((s) => ({ messages: { ...s.messages, [chatId]: { ...(s.messages[chatId] ?? empty()), loading: false, loaded: true } } }));
  }
}

export async function loadEarlierMessages(chatId: string) {
  const cur = get().messages[chatId];
  if (!cur?.list.length || cur.loading) return;
  set((s) => ({ messages: { ...s.messages, [chatId]: { ...cur, loading: true } } }));
  try {
    const page = await chatApi.messages(chatId, cur.list[0].seq, 100);
    set((s) => {
      const prev = s.messages[chatId] ?? empty();
      return { messages: { ...s.messages, [chatId]: { list: merge(prev.list, page.messages ?? []), hasMore: page.has_more, loading: false, loaded: true } } };
    });
  } catch {
    set((s) => ({ messages: { ...s.messages, [chatId]: { ...cur, loading: false } } }));
  }
}

function empty(): MessageList {
  return { list: [], hasMore: false, loading: false, loaded: false };
}

function merge(a: ChatMessage[], b: ChatMessage[]): ChatMessage[] {
  const byId = new Map<string, ChatMessage>();
  for (const m of a) byId.set(m.id, m);
  for (const m of b) byId.set(m.id, m);
  return [...byId.values()].sort((x, y) => x.seq - y.seq);
}

// ---------------------------------------------------------------------------------------------
// events

function handle(ev: BusEvent) {
  switch (ev.type) {
    case "snapshot":
      void loadChats();
      break;
    case "chat.message": {
      const m = ev.data as ChatMessage;
      if (!m?.id) break;
      set((s) => {
        const cur = s.messages[m.chat_id];
        const chat = s.chats[m.chat_id];
        const chats = chat && (!chat.last_message || chat.last_message.seq <= m.seq) ? { ...s.chats, [m.chat_id]: { ...chat, last_message: m } } : s.chats;
        if (!cur?.loaded) return { chats };
        return { chats, messages: { ...s.messages, [m.chat_id]: { ...cur, list: merge(cur.list, [m]) } } };
      });
      break;
    }
    case "chat.updated": {
      const c = ev.data as Chat;
      if (c?.id) set((s) => ({ chats: { ...s.chats, [c.id]: c } }));
      break;
    }
    case "chat.removed": {
      const id = (ev.data as { id?: string })?.id;
      if (!id) break;
      set((s) => {
        const chats = { ...s.chats };
        delete chats[id];
        const messages = { ...s.messages };
        delete messages[id];
        return { chats, messages };
      });
      break;
    }
    case "contact.updated": {
      const c = ev.data as Contact;
      if (c?.peer_id) set((s) => ({ contacts: { ...s.contacts, [c.peer_id]: { ...s.contacts[c.peer_id], ...c } } }));
      break;
    }
    case "contact.removed": {
      const peer = (ev.data as { peer_id?: string })?.peer_id;
      if (!peer) break;
      set((s) => {
        const contacts = { ...s.contacts };
        delete contacts[peer];
        return { contacts };
      });
      break;
    }
  }
}

onBusEvent(handle);

// ---------------------------------------------------------------------------------------------
// dialogs

export const chatUI = {
  openInvite: (kind: InviteKind = "dm", groupId?: string) => set({ invite: { open: true, kind, groupId } }),
  closeInvite: () => set((s) => ({ invite: { ...s.invite, open: false } })),
  openJoin: (link?: string) => set({ join: { open: true, link } }),
  closeJoin: () => set({ join: { open: false } }),
};

// ---------------------------------------------------------------------------------------------
// selectors

/** Chats by latest activity. */
export function sortedChats(chats: Record<string, Chat>): Chat[] {
  return Object.values(chats).sort((a, b) => Math.max(b.last_at, b.created_at) - Math.max(a.last_at, a.created_at) || a.name.localeCompare(b.name));
}

export function totalUnread(chats: Record<string, Chat>): number {
  return Object.values(chats).reduce((n, c) => n + (c.unread || 0), 0);
}

/** One line for a chat list: "Alex's head: on my way". */
export function previewLine(m: ChatMessage | undefined): string {
  if (!m) return "";
  let body = m.body.replace(/\s+/g, " ").trim();
  const n = m.files?.length ?? 0;
  if (n) body = [body, n === 1 ? `📎 ${m.files![0].name}` : `📎 ${n} files`].filter(Boolean).join(" · ");
  if (m.sender === CHAT_SYSTEM) return body;
  if (m.sender === "me") return (m.sender_agent ? `${m.sender_agent}: ` : "You: ") + body;
  return `${m.sender_agent ? `${m.sender_name}'s ${m.sender_agent}` : m.sender_name}: ${body}`;
}

/** "Alex" for a person, "Alex's head" for their agent, "You" / "Your head" here. */
export function senderName(m: ChatMessage): string {
  if (m.sender === "me") return m.sender_agent ? `Your ${m.sender_agent}` : "You";
  return m.sender_agent ? `${m.sender_name}'s ${m.sender_agent}` : m.sender_name;
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return "?";
  if (parts.length === 1) return parts[0].slice(0, 1).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

/** "in 23 h", "in 6 days", "expired". */
export function expiresIn(ms: number, now = Date.now()): string {
  const d = ms - now;
  if (d <= 0) return "expired";
  const m = Math.round(d / 60000);
  if (m < 60) return `in ${m} min`;
  const h = Math.round(m / 60);
  if (h < 48) return `in ${h} h`;
  return `in ${Math.round(h / 24)} days`;
}

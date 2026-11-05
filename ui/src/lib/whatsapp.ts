// WhatsApp in the UI (DESIGN.md "WhatsApp"): the link state, the chats and their messages, kept
// current by the whatsapp.* BusEvents. Like lib/chats.ts it is its own store on the same event
// stream and reloads on every (re)connect. Messages are other people's words: the UI shows
// them as text, never as markup.
import { create } from "zustand";
import { ApiError } from "./transport";
import { onBusEvent, useStore } from "./store";
import { ownerAction, withConfirmation } from "./owner-gate";
import type { Approval, BusEvent } from "./types";
import type { WaChat, WaMediaData, WaMessage, WaQR, WaSettings, WaStatus, WaUpload } from "./wa-types";
import { WA_PAGE } from "./wa-types";

export interface WaMessageList {
  list: WaMessage[];
  hasMore: boolean;
  loading: boolean;
  loaded: boolean;
}

interface WaStore {
  /** null until the first answer; false: this daemon has no WhatsApp (an older release) */
  available: boolean | null;
  /** the WhatsApp service is off on this daemon (503): why */
  offMessage: string;
  status: WaStatus | null;
  /** the code on screen while linking */
  qr: WaQR | null;
  chats: Record<string, WaChat>;
  chatsLoaded: boolean;
  messages: Record<string, WaMessageList>;
}

export const useWa = create<WaStore>(() => ({
  available: null,
  offMessage: "",
  status: null,
  qr: null,
  chats: {},
  chatsLoaded: false,
  messages: {},
}));

const set = useWa.setState;
const get = useWa.getState;

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}
const enc = encodeURIComponent;
const base = "/api/whatsapp";

export const waApi = {
  status: () => t().request<WaStatus>("GET", base),
  /** 202; the code comes as "whatsapp.qr" events (a pairing code may also come in the answer) */
  link: (method: "qr" | "phone", phone?: string) =>
    t().request<{ pairing_code?: string } | null>("POST", `${base}/link`, { method, ...(phone ? { phone } : {}) }),
  cancelLink: () => t().request<void>("POST", `${base}/link/cancel`),
  /** Logs the companion device out: the owner gate asks for Touch ID / Windows Hello when it is on. */
  unlink: () => ownerAction((c) => t().request<void>("POST", withConfirmation(`${base}/unlink`, c))),
  saveSettings: (s: WaSettings) => t().request<WaSettings>("PUT", `${base}/settings`, s),
  chats: (q = "", limit = 500) => t().request<WaChat[] | null>("GET", `${base}/chats?limit=${limit}${q ? `&q=${enc(q)}` : ""}`),
  messages: (jid: string, before = "", limit = WA_PAGE) =>
    t().request<WaMessage[] | null>("GET", `${base}/chats/${enc(jid)}/messages?limit=${limit}${before ? `&before=${enc(before)}` : ""}`),
  send: (jid: string, b: { text?: string; reply_to?: string; uploads?: WaUpload[] }) =>
    t().request<WaMessage>("POST", `${base}/chats/${enc(jid)}/messages`, b),
  read: (jid: string) => t().request<void>("POST", `${base}/chats/${enc(jid)}/read`),
  media: (jid: string, id: string) => t().request<WaMediaData>("GET", `${base}/media/${enc(jid)}/${enc(id)}?data=1`),
  ticket: (jid: string, id: string) => t().request<{ ticket: string; expires_at: number }>("POST", `${base}/media/${enc(jid)}/${enc(id)}/ticket`),
};

// ---------------------------------------------------------------------------------------------
// loading

/** Linked (or on its way back): chats and messages can be asked for. */
export function isLinked(s: WaStatus | null | undefined): boolean {
  return !!s && (s.state === "connected" || s.state === "connecting" || (s.state === "error" && !!s.phone));
}

export async function loadWhatsApp() {
  try {
    const s = await waApi.status();
    const was = get().status;
    set({ available: true, offMessage: "", status: s, ...(s.state === "linking" ? {} : { qr: null }) });
    if (isLinked(s)) void loadWaChats();
    else if (was && isLinked(was)) set({ chats: {}, messages: {}, chatsLoaded: true });
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 501)) set({ available: false });
    else if (e instanceof ApiError && e.status === 503) set({ available: true, offMessage: e.message, status: null });
  }
}

export async function loadWaChats() {
  try {
    const list = await waApi.chats();
    const byJid: Record<string, WaChat> = {};
    for (const c of list ?? []) byJid[c.jid] = c;
    set({ chats: byJid, chatsLoaded: true });
    // Catch up open chats (messages that arrived while the stream was down).
    for (const jid of Object.keys(get().messages)) if (byJid[jid]) void loadWaMessages(jid);
  } catch {
    set({ chatsLoaded: true });
  }
}

function empty(): WaMessageList {
  return { list: [], hasMore: false, loading: false, loaded: false };
}

function merge(a: WaMessage[], b: WaMessage[]): WaMessage[] {
  const byId = new Map<string, WaMessage>();
  for (const m of a) byId.set(m.id, m);
  for (const m of b) byId.set(m.id, { ...byId.get(m.id), ...m });
  return [...byId.values()].sort((x, y) => x.at - y.at || x.id.localeCompare(y.id));
}

function patchList(jid: string, fn: (l: WaMessageList) => WaMessageList) {
  set((s) => ({ messages: { ...s.messages, [jid]: fn(s.messages[jid] ?? empty()) } }));
}

export async function loadWaMessages(jid: string) {
  patchList(jid, (l) => ({ ...l, loading: true }));
  try {
    const page = (await waApi.messages(jid)) ?? [];
    patchList(jid, (l) => ({ list: merge(l.list, page), hasMore: l.loaded ? l.hasMore : page.length >= WA_PAGE, loading: false, loaded: true }));
  } catch {
    patchList(jid, (l) => ({ ...l, loading: false, loaded: true }));
  }
}

export async function loadEarlierWaMessages(jid: string) {
  const cur = get().messages[jid];
  if (!cur?.list.length || cur.loading) return;
  patchList(jid, (l) => ({ ...l, loading: true }));
  try {
    const page = (await waApi.messages(jid, cur.list[0].id)) ?? [];
    patchList(jid, (l) => ({ list: merge(l.list, page), hasMore: page.length >= WA_PAGE, loading: false, loaded: true }));
  } catch {
    patchList(jid, (l) => ({ ...l, loading: false }));
  }
}

/** Puts a message the daemon answered with (a send) into its chat at once. */
export function upsertWaMessage(m: WaMessage) {
  set((s) => {
    const cur = s.messages[m.chat_jid];
    const chat = s.chats[m.chat_jid];
    const chats = chat && (!chat.last || chat.last.at <= m.at) ? { ...s.chats, [m.chat_jid]: { ...chat, last: m } } : s.chats;
    if (!cur?.loaded) return { chats };
    return { chats, messages: { ...s.messages, [m.chat_jid]: { ...cur, list: merge(cur.list, [m]) } } };
  });
}

/** Marks a chat read here at once; the daemon confirms with a whatsapp.chat event. */
export async function markWaRead(jid: string) {
  set((s) => (s.chats[jid] ? { chats: { ...s.chats, [jid]: { ...s.chats[jid], unread: 0 } } } : {}));
  try {
    await waApi.read(jid);
  } catch {
    /* the next whatsapp.chat event has the truth */
  }
}

export async function saveWaSettings(next: WaSettings) {
  const prev = get().status;
  if (prev) set({ status: { ...prev, settings: next } });
  try {
    const s = await waApi.saveSettings(next);
    const cur = get().status;
    if (cur && s) set({ status: { ...cur, settings: s } });
  } catch (e) {
    if (prev) set({ status: { ...get().status!, settings: prev.settings } });
    throw e;
  }
}

/** Adds or removes a chat from one of the per-chat lists in the settings. */
export function toggleWaChatSetting(key: "allowed_chats" | "notify_head_chats", jid: string, on: boolean) {
  const s = get().status?.settings;
  if (!s) return Promise.resolve();
  const list = (s[key] ?? []).filter((x) => x !== jid);
  return saveWaSettings({ ...s, [key]: on ? [...list, jid] : list });
}

// ---------------------------------------------------------------------------------------------
// events (owner clients only: they carry message text)

function handle(ev: BusEvent) {
  switch (ev.type) {
    case "snapshot":
      void loadWhatsApp();
      break;
    case "whatsapp.status": {
      const s = ev.data as WaStatus;
      if (!s?.state) break;
      const was = get().status;
      set({ available: true, offMessage: "", status: s, ...(s.state === "linking" ? {} : { qr: null }) });
      if (isLinked(s) && (!was || !isLinked(was) || (was.history?.syncing && !s.history?.syncing))) void loadWaChats();
      if (!isLinked(s) && was && isLinked(was)) set({ chats: {}, messages: {} });
      break;
    }
    case "whatsapp.qr": {
      const q = ev.data as WaQR;
      if (q && (q.png || q.pairing_code)) set({ qr: q });
      break;
    }
    case "whatsapp.chat": {
      const c = ev.data as WaChat;
      if (c?.jid) set((s) => ({ chats: { ...s.chats, [c.jid]: { ...s.chats[c.jid], ...c } } }));
      break;
    }
    case "whatsapp.message": {
      const m = ev.data as WaMessage;
      if (m?.id && m.chat_jid) upsertWaMessage(m);
      break;
    }
  }
}

onBusEvent(handle);

// ---------------------------------------------------------------------------------------------
// media: through the API as base64 (every transport carries that), cached as object URLs for the
// session; video over http plays from the daemon's URL with a short-lived ticket (seeking works).

const blobs = new Map<string, Promise<string>>();

export function waMediaBlobURL(jid: string, id: string): Promise<string> {
  const key = jid + "/" + id;
  let p = blobs.get(key);
  if (!p) {
    p = waApi.media(jid, id).then((f) => {
      const bin = atob(f.data ?? "");
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      return URL.createObjectURL(new Blob([bytes], { type: f.type }));
    });
    p.catch(() => blobs.delete(key));
    blobs.set(key, p);
  }
  return p;
}

/** A URL a <video>/<audio> can stream from (http transport), or null where only blobs work. */
export async function waMediaStreamURL(jid: string, id: string): Promise<string | null> {
  const tr = t();
  if (tr.kind !== "http") return null;
  const tk = await waApi.ticket(jid, id);
  return `${tr.baseURL}${base}/media/${enc(jid)}/${enc(id)}?ticket=${enc(tk.ticket)}`;
}

/** Saves a document: the browser downloads it; the desktop app hands the daemon's URL to the system browser. */
export async function saveWaMedia(m: WaMessage) {
  const tr = t();
  const name = m.media?.name || `${m.type}-${m.id}`;
  if (tr.kind === "http" && tr.desktop) {
    const url = await waMediaStreamURL(m.chat_jid, m.id);
    if (url) return tr.openExternal(url);
  }
  const url = await waMediaBlobURL(m.chat_jid, m.id);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// ---------------------------------------------------------------------------------------------
// selectors and wording

/** Pinned first, then by latest activity; archived chats only when asked for. */
export function sortedWaChats(chats: Record<string, WaChat>, opts: { archived?: boolean } = {}): WaChat[] {
  return Object.values(chats)
    .filter((c) => !!c.archived === !!opts.archived)
    .sort((a, b) => Number(b.pinned) - Number(a.pinned) || (b.last?.at ?? 0) - (a.last?.at ?? 0) || a.name.localeCompare(b.name));
}

/** Unread messages in chats that aren't muted or archived. */
export function waUnread(chats: Record<string, WaChat>): number {
  return Object.values(chats).reduce((n, c) => n + (c.muted || c.archived ? 0 : c.unread || 0), 0);
}

/** "+41 79 123 45 67"-ish from a JID ("41791234567@s.whatsapp.net"); "" for groups. */
export function waPhone(jid: string): string {
  const [user, server] = jid.split("@");
  if (server !== "s.whatsapp.net" || !/^\d{6,}$/.test(user)) return "";
  return "+" + user;
}

export function clock(ms: number | undefined): string {
  const s = Math.max(0, Math.round((ms ?? 0) / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** What a message is, in a few words, for lists and replies. */
export function waSummary(m: Pick<WaMessage, "type" | "text" | "caption" | "media" | "deleted">): string {
  if (m.deleted) return "This message was deleted";
  const cap = (m.caption || m.text || "").replace(/\s+/g, " ").trim();
  const withCap = (label: string) => (cap ? `${label} · ${cap}` : label);
  switch (m.type) {
    case "image":
      return withCap("📷 Photo");
    case "video":
      return withCap(`🎥 Video${m.media?.duration_ms ? ` (${clock(m.media.duration_ms)})` : ""}`);
    case "voice":
      return `🎤 Voice message${m.media?.duration_ms ? ` (${clock(m.media.duration_ms)})` : ""}`;
    case "audio":
      return withCap(`🎵 ${m.media?.name || "Audio"}`);
    case "document":
      return withCap(`📄 ${m.media?.name || "Document"}`);
    case "sticker":
      return "Sticker";
    case "location":
      return `📍 ${cap || "Location"}`;
    case "contact":
      return `👤 ${cap || "Contact"}`;
    case "reaction":
      return cap ? `Reacted ${cap}` : "Reacted";
    case "unsupported":
      return "A message tmx can't show";
    default:
      return cap;
  }
}

/** One line for a chat list: "You: on my way", "Alex: 📷 Photo" in groups. */
export function waPreview(c: WaChat): string {
  const m = c.last;
  if (!m) return "";
  const body = waSummary(m);
  if (m.type === "system") return body;
  if (m.from_me) return "You: " + body;
  if (c.kind === "group" && m.sender_name) return `${m.sender_name.split(" ")[0]}: ${body}`;
  return body;
}

const SEND_TOOLS = new Set(["mcp__tmxg__whatsapp_send", "whatsapp_send"]);

/** An agent's send that the open WhatsApp chat already shows inline (no toast needed). */
export function waSendShownAt(a: Approval, hash: string): boolean {
  if (!SEND_TOOLS.has(a.tool) || !hash.startsWith("#/whatsapp/")) return false;
  const jid = decodeURIComponent(hash.slice("#/whatsapp/".length).split(/[/?]/)[0]);
  const chat = get().chats[jid];
  return !!chat && waSendApprovals({ [a.id]: a }, chat).length > 0;
}

/** Pending agent sends waiting for the owner (whatsapp_send approvals), optionally for one chat. */
export function waSendApprovals(approvals: Record<string, Approval>, chat?: Pick<WaChat, "jid" | "name">): Approval[] {
  return Object.values(approvals).filter((a) => {
    if (a.state !== "pending" || !SEND_TOOLS.has(a.tool)) return false;
    if (!chat) return true;
    const i = (a.input ?? {}) as Record<string, unknown>;
    const targets = [i.chat, i.jid, i.chat_jid, i.to].filter((v): v is string => typeof v === "string");
    return targets.some((v) => v === chat.jid || v.toLowerCase() === chat.name.toLowerCase());
  });
}

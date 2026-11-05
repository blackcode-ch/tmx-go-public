// WhatsApp in tmx (DESIGN.md "WhatsApp"): the owner's own account linked as a companion device,
// the way WhatsApp Web is. These mirror the daemon's JSON (internal/whatsapp), same names.

export type WaState = "unlinked" | "linking" | "connecting" | "connected" | "logged_out" | "error";

/** PUT /api/whatsapp/settings and Status.settings. */
export interface WaSettings {
  /** reading a chat in tmx also sends read receipts (blue ticks); default off */
  read_receipts: boolean;
  /** "ask": an agent's message waits for the owner's Allow, except in allowed_chats; "allowed": agents send without asking */
  agents_send: "ask" | "allowed";
  /** chats (JIDs) where agents may send without asking */
  allowed_chats: string[];
  /** chats (JIDs) whose new messages reach the head as a short mail (framed as data) */
  notify_head_chats: string[];
}

/** GET /api/whatsapp and the "whatsapp.status" event. */
export interface WaStatus {
  state: WaState;
  /** the linked number, e.g. "+41791234567" */
  phone?: string;
  /** the account's push name */
  name?: string;
  linked_at?: number;
  error?: string;
  history: { syncing: boolean; progress?: number };
  settings: WaSettings;
}

/** The "whatsapp.qr" event: a QR code to scan (renewed every ~20 s) or a phone pairing code. */
export interface WaQR {
  code?: string;
  /** data:image/png;base64,…, rendered by the daemon */
  png?: string;
  expires_at?: number;
  pairing_code?: string;
}

export type WaMessageType =
  | "text"
  | "image"
  | "video"
  | "audio"
  | "voice"
  | "document"
  | "sticker"
  | "location"
  | "contact"
  | "reaction"
  | "system"
  | "unsupported";

export type WaMessageStatus = "pending" | "sent" | "delivered" | "read" | "failed";

export interface WaMedia {
  mime: string;
  size: number;
  name?: string;
  duration_ms?: number;
  width?: number;
  height?: number;
  /** already in the state dir; otherwise the first GET downloads it */
  downloaded: boolean;
}

export interface WaMessage {
  id: string;
  chat_jid: string;
  sender_jid: string;
  sender_name: string;
  from_me: boolean;
  /** ms */
  at: number;
  type: WaMessageType;
  text?: string;
  caption?: string;
  media?: WaMedia;
  quoted?: { id: string; text?: string; sender_name?: string };
  reactions?: { emoji: string; by: string }[];
  status: WaMessageStatus;
  error?: string;
  edited?: boolean;
  deleted?: boolean;
}

export interface WaChat {
  jid: string;
  name: string;
  kind: "dm" | "group";
  unread: number;
  pinned: boolean;
  muted: boolean;
  archived: boolean;
  last?: WaMessage | null;
  /** a data URL, or null */
  avatar?: string | null;
}

/** GET /api/whatsapp/media/{jid}/{id}?data=1 */
export interface WaMediaData {
  type: string;
  size: number;
  name?: string;
  data: string;
}

/** One file sent with a message (POST …/messages uploads). */
export interface WaUpload {
  name: string;
  type: string;
  /** base64 */
  data: string;
}

/** What the composer accepts per file and per message. */
export const WA_MAX_FILE_BYTES = 16 << 20;
export const WA_MAX_FILES = 5;
/** Images and stickers up to this size load by themselves; bigger media wait for a click. */
export const WA_AUTOLOAD_BYTES = 4 << 20;
/** Messages per page. */
export const WA_PAGE = 50;

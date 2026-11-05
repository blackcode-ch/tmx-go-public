// Mirror of internal/model/chat.go and the chat API (DESIGN.md "Chats"); JSON names are the
// contract. Chats connect this tmx with other people's tmx installs (peer to peer).

export type Reachability = "direct" | "relayed" | "offline";

export interface Contact {
  peer_id: string;
  name: string;
  remote_name: string;
  note: string;
  safety_code: string;
  added_at: number;
  updated_at: number;
  reachability: Reachability | "";
}

export type ChatKind = "dm" | "group";
export type ChatStatus = "active" | "pending" | "left" | "removed";
export type ChatRole = "admin" | "member";

export interface ChatMember {
  chat_id: string;
  peer_id: string;
  name: string;
  role: ChatRole;
  joined_at: number;
  left_at?: number;
  me?: boolean;
  /** their tmx said it understands admin handovers (an older one keeps its member list as it is) */
  handover?: boolean;
}

/** We (the admin) offered a member the admin role; their tmx takes over by itself when it gets it. */
export interface ChatAdminOffer {
  peer_id: string;
  name: string;
  at: number;
  /** we leave the group once they have taken over */
  then_leave?: boolean;
  /** the offer reached their tmx (no longer queued); an up-to-date one answers at once */
  sent?: boolean;
}

export type ChatMessageState = "queued" | "sent" | "delivered" | "failed";

/** Sender values besides a peer ID. */
export const CHAT_ME = "me";
export const CHAT_SYSTEM = "system";

export interface ChatMessage {
  seq: number;
  id: string;
  chat_id: string;
  /** peer ID, "me" or "system" */
  sender: string;
  /** the person */
  sender_name: string;
  /** the agent's name when an agent wrote it; "" = the person */
  sender_agent: string;
  body: string;
  at: number;
  state: ChatMessageState;
  received_at: number;
  recipients?: number;
  delivered?: number;
  /** who the author @mentioned (label = the text after "@" in body) */
  mentions?: ChatMention[];
  /** the message @mentions you */
  mentions_me?: boolean;
  files?: ChatFile[];
  /** the board topic it hangs on ("" or absent = General); see ChatTopic */
  topic?: string;
}

export interface ChatMention {
  peer_id: string;
  agent?: string;
  label: string;
}

export type ChatFileState = "pending" | "ready" | "failed";

export interface ChatFile {
  id: string;
  chat_id: string;
  message_id?: string;
  sender: string;
  name: string;
  type: string;
  size: number;
  sha256: string;
  state: ChatFileState;
  error?: string;
  created_at: number;
  /** where it is stored on this machine (ready files) */
  path?: string;
  image?: boolean;
}

/** Someone a message can @mention (GET /api/chats/{id}/mentionable). */
export interface Mentionable {
  label: string;
  peer_id: string;
  agent?: string;
  kind: "person" | "agent";
  /** this install's own chat agent */
  mine?: boolean;
}

/** The largest file a chat message carries (internal/chat MaxFileBytes). */
export const CHAT_MAX_FILE_BYTES = 25 << 20;
export const CHAT_MAX_FILES = 10;

export interface Chat {
  id: string;
  kind: ChatKind;
  name: string;
  peer_id?: string;
  created_by: string;
  /** the local agent taking part; "" = people only */
  agent_id: string;
  status: ChatStatus;
  version: number;
  read_seq: number;
  created_at: number;
  updated_at: number;
  last_at: number;
  unread: number;
  /** unread messages that @mention you */
  mentions?: number;
  admin: boolean;
  members?: ChatMember[];
  last_message?: ChatMessage;
  agent_name?: string;
  /** an open offer of the admin role (only while we are the admin) */
  admin_offer?: ChatAdminOffer;
}

export type InviteKind = "dm" | "group";

export interface Invite {
  id: string;
  kind: InviteKind;
  /** tmx://join/<payload>; the QR code encodes this */
  link: string;
  group_id?: string;
  group_name?: string;
  expires_at: number;
  uses: number;
  max_uses: number;
  revoked: boolean;
}

export interface InvitePreview {
  kind: InviteKind;
  peer_id: string;
  name: string;
  group_id?: string;
  group_name?: string;
  expires_at: number;
}

export interface JoinResult {
  chat: Chat;
  contact: Contact;
}

export interface ChatPage {
  messages: ChatMessage[];
  has_more: boolean;
}

export interface Unread {
  total: number;
  chats: Record<string, number>;
  mentions?: number;
}

export function currentMembers(c: Pick<Chat, "members">): ChatMember[] {
  return (c.members ?? []).filter((m) => !m.left_at);
}

// ---------------------------------------------------------------------------------------------
// chat boards (DESIGN.md "Chat boards"; Go internal/model/chattopic.go)

/** The catch-all topic every chat board has (not stored; messages without a topic). */
export const CHAT_GENERAL_TOPIC = "general";

/** A string on a chat's board, shared by the chat's members. Never deleted: archived ones leave the board. */
export interface ChatTopic {
  chat_id: string;
  id: string;
  kind: "general" | "topic";
  title: string;
  color?: string;
  archived?: boolean;
  /** peer ID of the member who opened it */
  created_by: string;
  created_at: number;
  updated_at: number;
  /** messages on it */
  messages: number;
  /** its place in your own order (1 = most urgent) */
  position?: number;
}

/** A line in a chat topic's log (append-only; a correction names the line it replaces). */
export interface ChatTopicEntry {
  id: string;
  chat_id: string;
  topic_id: string;
  kind: "decision" | "action" | "info" | "link";
  text: string;
  link?: string;
  /** the member's peer ID, or "me" */
  by: string;
  by_name: string;
  by_agent?: string;
  at: number;
  corrects?: string;
}

/** GET /api/chats/{id}/board: topics in your order (General included), logs, each topic's latest messages. */
export interface ChatBoard {
  chat_id: string;
  topics: ChatTopic[];
  ordered: boolean;
  entries: ChatTopicEntry[];
  messages: ChatMessage[];
}

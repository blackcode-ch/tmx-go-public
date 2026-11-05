// @mentions in chat messages: the daemon resolves "@label" when a message is sent and lists
// who was meant (ChatMessage.mentions). For display, each "@label" in the body becomes a
// Markdown link that the Markdown component renders as a mention pill ("#@me" when it is you
// or your agent).
import type { Chat, ChatMention, ChatMessage, Mentionable } from "./chat-types";

function escapeRe(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function escapeMd(s: string) {
  return s.replace(/([[\]\\*_`])/g, "\\$1");
}

/** This install's peer ID as the chat knows it ("" if unknown). */
export function selfPeer(chat: Chat): string {
  return chat.members?.find((m) => m.me)?.peer_id ?? "";
}

export function mentionIsMe(chat: Chat, m: ChatMention): boolean {
  const self = selfPeer(chat);
  if (self) return m.peer_id === self;
  // A DM: everyone who is not the other side is us.
  return chat.kind === "dm" && !!chat.peer_id && m.peer_id !== chat.peer_id;
}

/** The body with its @mentions turned into mention links. */
export function withMentionLinks(chat: Chat, msg: ChatMessage): string {
  const list = [...(msg.mentions ?? [])].sort((a, b) => b.label.length - a.label.length);
  if (!list.length) return msg.body;
  let body = msg.body;
  const marks: string[] = [];
  for (const m of list) {
    const re = new RegExp(`(^|[^\\p{L}\\p{N}_])@${escapeRe(m.label)}(?![\\p{L}\\p{N}_])`, "giu");
    body = body.replace(re, (_all, pre: string) => {
      const href = msg.sender !== "me" && mentionIsMe(chat, m) ? "#@me" : "#@mention";
      marks.push(`[@${escapeMd(m.label)}](${href})`);
      return `${pre}\uE000${marks.length - 1}\uE000`;
    });
  }
  return body.replace(/\uE000(\d+)\uE000/g, (_a, i: string) => marks[Number(i)]);
}

/** The "@query" being typed just before the caret, or null. */
export function mentionQuery(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret);
  const m = /(^|[\s(])@([^@\n]{0,40})$/u.exec(before);
  if (!m) return null;
  return { start: before.length - m[2].length - 1, query: m[2] };
}

export function matchMentionables(list: Mentionable[], query: string): Mentionable[] {
  const q = query.toLowerCase();
  const hits = list.filter((x) => x.label.toLowerCase().startsWith(q) || (q.length > 0 && x.label.toLowerCase().includes(q)));
  // A query with a space must still match a label (names have spaces; prose does not continue a mention).
  if (!hits.length) return [];
  return hits.slice(0, 7);
}

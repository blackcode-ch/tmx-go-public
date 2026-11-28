import type { Message, TimelineEntry } from "./types";

/** Delivery means the process accepted/submitted it, not that the agent read it. */
export function ownerDeliveryLabel(message: Message, time: (at: number) => string): string {
  if (message.receipt === "stalled") return "not delivered, retry";
  if (message.receipt === "unconfirmed") return "delivery uncertain";
  if (message.receipt === "menu_wait") return "waiting for menu to close";
  if (message.receipt === "dispatching") return "sending";
  if (message.state === "queued") return "queued";
  if (message.state === "failed") return "failed";
  if (message.receipt === "submitted") return `delivered ${time(message.receipt_at ?? message.delivered_at ?? message.created_at)}`;
  if (message.receipt === "read") return "read";
  if (message.receipt === "accepted") return "accepted";
  return "sending";
}

/** Pending mail belongs in the conversation, deduplicated against its durable entry. */
export function withPendingOwnerMessages(entries: TimelineEntry[], messages: Message[], agentId: string): TimelineEntry[] {
  const shown = new Set(entries.map((e) => e.meta?.message_id));
  const pending: TimelineEntry[] = messages.filter((m) => m.from === "owner" && m.to === agentId && !shown.has(m.id) &&
    (m.state === "queued" || m.state === "sending" || m.state === "failed" || ["dispatching", "menu_wait", "stalled", "unconfirmed"].includes(m.receipt ?? "")))
    .map((m, i) => ({ seq: -i - 1, agent_id: agentId, at: m.created_at, kind: "user", text: m.body, meta: { from: "owner", message_id: m.id }, thread_id: m.thread_id }));
  return pending.length ? [...entries, ...pending].sort((a, b) => a.at - b.at) : entries;
}

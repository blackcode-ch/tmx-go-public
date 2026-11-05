import type { TimelineEntry } from "@/lib/types";

export type Item =
  | { type: "user"; key: string; e: TimelineEntry }
  | { type: "assistant"; key: string; e: TimelineEntry }
  | { type: "thinking"; key: string; e: TimelineEntry }
  | { type: "tools"; key: string; tools: ToolPair[] }
  | { type: "mail"; key: string; e: TimelineEntry }
  | { type: "result"; key: string; e: TimelineEntry }
  | { type: "system"; key: string; e: TimelineEntry }
  | { type: "error"; key: string; e: TimelineEntry }
  | { type: "time"; key: string; at: number };

export interface ToolPair {
  key: string;
  use?: TimelineEntry;
  result?: TimelineEntry;
}

const GAP = 15 * 60_000;

/** Harness plumbing, not work: Claude loading its deferred tool definitions. */
const HIDDEN_TOOLS = new Set(["ToolSearch"]);

/** Pairs tool results with their calls, groups consecutive tool calls, adds time breaks. */
export function buildItems(entries: TimelineEntry[]): Item[] {
  const results = new Map<string, TimelineEntry>();
  const hidden = new Set<string>();
  for (const e of entries) {
    if (e.kind === "tool_result" && e.tool_use_id) results.set(e.tool_use_id, e);
    if (e.kind === "tool_use" && e.tool && HIDDEN_TOOLS.has(e.tool) && e.tool_use_id) hidden.add(e.tool_use_id);
  }
  const used = new Set<string>();
  const items: Item[] = [];
  let prevAt = 0;
  for (const e of entries) {
    if ((e.kind === "tool_use" || e.kind === "tool_result") && e.tool_use_id && hidden.has(e.tool_use_id)) continue;
    if (prevAt && e.at - prevAt > GAP) items.push({ type: "time", key: `t${e.seq}`, at: e.at });
    prevAt = e.at;
    const key = String(e.seq);
    switch (e.kind) {
      case "tool_use":
      case "tool_result": {
        let pair: ToolPair;
        if (e.kind === "tool_use") {
          const r = e.tool_use_id ? results.get(e.tool_use_id) : undefined;
          if (r) used.add(r.tool_use_id!);
          pair = { key, use: e, result: r };
        } else {
          if (e.tool_use_id && used.has(e.tool_use_id)) continue;
          pair = { key, result: e };
        }
        const last = items[items.length - 1];
        if (last?.type === "tools") last.tools.push(pair);
        else items.push({ type: "tools", key: "g" + key, tools: [pair] });
        break;
      }
      case "user":
        items.push({ type: "user", key, e });
        break;
      case "assistant":
        if (e.text.trim()) items.push({ type: "assistant", key, e });
        break;
      case "thinking":
        if (e.text.trim()) items.push({ type: "thinking", key, e });
        break;
      case "message_in":
        items.push({ type: "mail", key, e });
        break;
      case "result":
        items.push({ type: "result", key, e });
        break;
      case "error":
        items.push({ type: "error", key, e });
        break;
      case "system": {
        if (!e.text.trim()) break;
        // Several restarts in a row leave identical markers ("stopped"): show the latest once.
        const last = items[items.length - 1];
        if (last?.type === "system" && last.e.text === e.text) items[items.length - 1] = { type: "system", key, e };
        else items.push({ type: "system", key, e });
        break;
      }
      default:
        if (e.text?.trim()) items.push({ type: "system", key, e });
    }
  }
  return items;
}

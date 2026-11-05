import { create } from "zustand";
import { useStore } from "./store";

export interface MessageSelection {
  source: string;
  label: string;
  href: string;
  agent_id?: string;
  text: string;
  message_at: number;
}
export interface MessagePin extends MessageSelection {
  id: string;
  pinned_at: number;
  state: "active" | "done";
}
export const useMessagePins = create<{ pins: MessagePin[]; error: string; loaded: boolean }>(() => ({ pins: [], error: "", loaded: false }));
let generation = 0;
export async function loadMessagePins() {
  const tr = useStore.getState().transport;
  if (!tr) return;
  const request = ++generation;
  try {
    const pins = await tr.request<MessagePin[]>("GET", "/api/message-pins");
    if (request === generation && tr === useStore.getState().transport) useMessagePins.setState({ pins, error: "", loaded: true });
  } catch (e) {
    if (request === generation && tr === useStore.getState().transport) useMessagePins.setState({ error: (e as Error).message });
  }
}
export async function pinMessage(selection: MessageSelection) {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  await tr.request("POST", "/api/message-pins", selection);
  await loadMessagePins();
}
export async function changeMessagePin(id: string, state: "active" | "done" | "unpinned") {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  await tr.request("PATCH", `/api/message-pins/${encodeURIComponent(id)}`, { state });
  await loadMessagePins();
}

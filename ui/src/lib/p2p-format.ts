// Pure text logic for the Peer-to-peer settings panel (components/p2p-settings.tsx) and the
// Chats empty state (pages/chats.tsx). No runtime project imports, so this stays trivially
// unit-testable (test/p2p-format.test.mjs) without a daemon connection.
import type { P2PStatus } from "./types";

export function reachabilityLabel(st: Pick<P2PStatus, "enabled" | "reachability">): string {
  if (!st.enabled) return "Off";
  switch (st.reachability) {
    case "direct":
      return "Connected (direct)";
    case "relayed":
      return "Connected (through a relay)";
    default:
      return "Not reachable yet";
  }
}

/** "on"/"off", for a compact "saved: on, live: off" readout. */
export function onOff(v: boolean): string {
  return v ? "on" : "off";
}

/**
 * A banner explaining a saved/live mismatch, or null when there is nothing to say (they agree).
 * Peer-to-peer only ever takes effect at the next daemon start (there is no live hot-toggle), so
 * whenever restart_to_apply is true the owner just changed it and needs to know it hasn't taken
 * effect yet.
 */
export function savedVsLiveBanner(st: Pick<P2PStatus, "enabled" | "saved_enabled" | "restart_to_apply">): string | null {
  if (!st.restart_to_apply) return null;
  return `Saved: ${onOff(st.saved_enabled)} — NOT live until restart (currently ${onOff(st.enabled)}).`;
}

/** Whether a view showing P2P status should keep polling GET /api/p2p every 5s: a restart was
 * asked for (or is pending) and the running state hasn't caught up with the saved one yet. */
export function shouldPollP2P(st: Pick<P2PStatus, "restart_to_apply"> | null): boolean {
  return st?.restart_to_apply ?? false;
}

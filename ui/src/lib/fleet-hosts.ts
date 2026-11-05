// Fleet-wide Overview aggregation (WP4, internal/api/fleethosts.go): other local daemons
// configured in config.json "hosts", read over loopback only - no p2p, no new network
// exposure (DESIGN.md plan phase 10, "multi-machine", is not built; issue #90 keeps p2p off).
// Line-building and status logic is pure, in fleet-hosts-format.ts; this file is the zustand
// store and the fetch, kept apart so the pure logic stays unit-testable without a connection.
import { create } from "zustand";
import { fleetHostsResult, hidesOnStatus } from "./fleet-hosts-format";
import { useStore } from "./store";

/** "host" = a loopback daemon on this machine (CPU/mem/disk/head activity, precise). "peer" =
 * a real other machine (Python fleet interop) - only what GET /api/sessions carries, so no
 * port/cpu/mem/disk, and running/paused/needs_you are a best-effort read of each session's
 * display state, not the precise enum a same-process host gets. */
export type HostKind = "host" | "peer";

export interface HostSummary {
  kind: HostKind;
  name: string;
  port?: number;
  reachable: boolean;
  error?: string;
  machine?: string;
  agents: number;
  projects: number;
  running: number;
  paused: number;
  needs_you: number;
  cpu?: number;
  mem_free_mb?: number;
  disk_free_gb?: number;
  head_name?: string;
  head_state?: string;
  head_line?: string;
  /** "peer" only: this machine and that peer have each other's pair token (can be Reachable
   * without being Paired, or the reverse, if a token just rotated). */
  paired?: boolean;
}

interface FleetHostsState {
  loaded: boolean;
  hosts: HostSummary[];
}

export const useFleetHosts = create<FleetHostsState>(() => ({ loaded: false, hosts: [] }));

function transport() {
  const t = useStore.getState().transport;
  if (!t) throw new Error("Not connected yet.");
  return t;
}

/** Fetches the current merge. The owner only (an agent gets 403); an older daemon, 404/501. */
export async function loadFleetHosts() {
  try {
    const v = await transport().request<{ hosts: HostSummary[] | null }>("GET", "/api/fleet/hosts");
    useFleetHosts.setState(fleetHostsResult(v.hosts));
  } catch (e) {
    const status = (e as { status?: number }).status;
    if (hidesOnStatus(status)) useFleetHosts.setState({ loaded: true, hosts: [] });
  }
}

// Updates from GitHub releases (DESIGN.md "Updates"): the daemon checks, downloads, verifies and
// installs; the UI shows the state ("update.status" events) and lets the owner steer it.
import { create } from "zustand";
import { ownerAction, withConfirmation } from "./owner-gate";
import { onBusEvent, useStore } from "./store";
import type { BusEvent } from "./types";

export type UpdateState = "off" | "idle" | "checking" | "available" | "downloading" | "ready" | "installing" | "error";

export interface UpdateStatus {
  current: string;
  state: UpdateState;
  /** state off: why this build does not update itself */
  reason?: string;
  auto: boolean;
  channel: "stable" | "latest";
  repo: string;
  latest?: string;
  latest_url?: string;
  available: boolean;
  /** state ready: what the install waits for */
  waiting?: string[];
  error?: string;
  needs_token?: boolean;
  has_token: boolean;
  checked_at?: number;
}

export const useUpdate = create<{ status: UpdateStatus | null }>(() => ({ status: null }));

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

function keep(s: UpdateStatus) {
  useUpdate.setState({ status: s });
  return s;
}

export const updateApi = {
  get: () => t().request<UpdateStatus>("GET", "/api/update").then(keep),
  check: () => t().request<UpdateStatus>("POST", "/api/update/check").then(keep),
  // Installing by hand and changing the settings or the token are behind the owner gate: a 428
  // asks for the owner's confirmation, then retries (the token is never in the prompt).
  install: (now: boolean) =>
    ownerAction((c) => t().request<UpdateStatus>("POST", withConfirmation("/api/update/install", c), { now })).then(keep),
  configure: (b: { auto?: boolean; channel?: "stable" | "latest" }) =>
    ownerAction((c) => t().request<UpdateStatus>("PUT", withConfirmation("/api/update", c), b)).then(keep),
  setToken: (token: string) =>
    ownerAction((c) => t().request<UpdateStatus>("PUT", withConfirmation("/api/update/token", c), { token })).then(keep),
};

/** Loads the status once connected (older daemons answer 404/501: then the UI shows nothing). */
export async function loadUpdate() {
  try {
    await updateApi.get();
  } catch {
    useUpdate.setState({ status: null });
  }
}

onBusEvent((ev: BusEvent) => {
  if (ev.type === "update.status" && ev.data) useUpdate.setState({ status: ev.data as UpdateStatus });
});

/** The desktop window's own build (it keeps the UI it started with until it restarts). */
export async function windowVersion(): Promise<string | null> {
  try {
    const cfg = (await window.go?.desktop?.App?.Config()) as { version?: string } | undefined;
    return cfg?.version ?? null;
  } catch {
    return null;
  }
}

/** Restarts the desktop window onto the installed tmx. */
export async function relaunchWindow(): Promise<void> {
  const app = window.go?.desktop?.App as { Relaunch?: () => Promise<void> } | undefined;
  if (!app?.Relaunch) throw new Error("Close and open tmx again to finish the update.");
  await app.Relaunch();
}

// The owner gate in the UI (DESIGN.md "Safety"). A gated request answers 428 with a
// confirmation while the daemon itself asks the OS (Touch ID or the Mac password, Windows Hello); the UI only
// shows "Confirm on this Mac", waits for the daemon's verdict and retries the same request
// naming the confirmation. On a paired browser (method "pin") the owner types the owner PIN
// instead: one short code, the same on every machine and in the Python tmx; never a passkey.
//
// The grace window: after a proof the daemon stops asking for grace_minutes (Settings →
// Safety's dial; it only skips the OS prompt, the owner still decides each action). The daemon
// keeps it in memory and announces every change as "gate.updated" (no data), so this re-reads
// GET /api/gate.
import { useEffect, useState } from "react";
import { create } from "zustand";
import { allowForeground, ApiError } from "./transport";
import { onBusEvent, useStore } from "./store";
import type { BusEvent, Confirmation, GateStatus, SafetyInfo } from "./types";

interface GateState {
  /** the confirmation the owner is being asked for right now */
  confirming: Confirmation | null;
  /** from GET /api/safety; null until loaded */
  safety: SafetyInfo | null;
  /** a longer window being asked for (the confirm sheet names it instead of the current one) */
  graceAsked: number | null;
  /** the last PIN answer's problem ("Wrong PIN.", "Wait 60 s.") for the PIN sheet */
  pinError: string | null;
}

export const useGate = create<GateState>(() => ({ confirming: null, safety: null, graceAsked: null, pinError: null }));

// The PIN sheet's pending answer (method "pin"): submitPin resolves it once the daemon took the PIN.
// local: a gate confirmation at this machine (POST /api/gate/confirmations/{id}/pin); otherwise a
// paired browser's (POST /api/gate/approvals/{id}/pin).
let pinWaiter: { id: string; local: boolean; resolve: (c: Confirmation) => void; reject: (e: unknown) => void } | null = null;

/** Sends the owner PIN for the confirmation on screen. A wrong PIN keeps the sheet open. */
export async function submitPin(pin: string): Promise<void> {
  const w = pinWaiter;
  if (!w) return;
  try {
    const path = w.local ? `/api/gate/confirmations/${encodeURIComponent(w.id)}/pin` : `/api/gate/approvals/${encodeURIComponent(w.id)}/pin`;
    const c = await transport().request<Confirmation>("POST", path, { pin });
    useGate.setState({ pinError: null });
    pinWaiter = null;
    w.resolve(c);
  } catch (e) {
    if (e instanceof ApiError && e.status === 429) {
      const wait = Number((e.data as { wait_s?: number } | undefined)?.wait_s) || 60;
      useGate.setState({ pinError: `Too many wrong PINs: wait ${wait} s, or clear it at the machine.` });
    } else if (e instanceof ApiError && e.status === 403 && /wrong PIN/i.test(e.message)) {
      useGate.setState({ pinError: "Wrong PIN. Try again." });
    } else {
      pinWaiter = null;
      w.reject(e);
    }
  }
}

/** The dial's lengths in minutes when the daemon sends none (0 = off). */
export const GRACE_STEPS = [0, 5, 15, 30, 60, 120, 240, 480, 1440];

/** "Off", "5 min", "1 h", "24 h". */
export function graceLabel(min: number): string {
  if (min <= 0) return "Off";
  return min < 60 ? `${min} min` : `${min / 60} h`;
}

/** The dial's tick labels: "Off", "5m", "1h". */
export function graceTick(min: number): string {
  if (min <= 0) return "Off";
  return min < 60 ? `${min}m` : `${min / 60}h`;
}

/** "15:42", or "tomorrow 09:10" (a window is at most 24 h). */
export function graceUntilLabel(ms: number): string {
  const d = new Date(ms);
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === new Date().toDateString() ? time : `tomorrow ${time}`;
}

/** The open window's end, or 0; re-renders when it ends. */
export function useGraceUntil(): number {
  const until = useGate((s) => s.safety?.gate.grace_until ?? 0);
  const [over, setOver] = useState(0); // the last window seen to end
  useEffect(() => {
    if (!until) return;
    const left = Math.max(0, until - Date.now());
    const t = setTimeout(() => setOver(until), Math.min(left + 50, 2 ** 31 - 1));
    return () => clearTimeout(t);
  }, [until]);
  return until && over !== until ? until : 0;
}

/** Whether a gated action would show the OS prompt now: the gate is on and no window is open. */
export function useGateAsks(): boolean {
  const on = useGate((s) => !!s.safety?.gate.enabled);
  const until = useGraceUntil();
  return on && !until;
}

async function refreshGate() {
  try {
    setGateStatus(await transport().request<GateStatus>("GET", "/api/gate"));
  } catch {
    /* an older daemon, or offline: Settings shows what it has */
  }
}

onBusEvent((ev: BusEvent) => {
  if (ev.type === "gate.updated") void refreshGate();
  else if (ev.type === "snapshot" && useGate.getState().safety) void refreshGate(); // a reconnect may have missed one
});

function transport() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

export async function loadSafety(): Promise<SafetyInfo | null> {
  try {
    const s = await transport().request<SafetyInfo>("GET", "/api/safety");
    useGate.setState({ safety: s });
    return s;
  } catch {
    return null;
  }
}

export function setGateStatus(gate: GateStatus) {
  useGate.setState((st) => (st.safety ? { safety: { ...st.safety, gate } } : {}));
}

/** Appends ?confirmation=ID (the desktop bridge cannot set headers; the daemon accepts both). */
export function withConfirmation(path: string, id?: string): string {
  if (!id) return path;
  return path + (path.includes("?") ? "&" : "?") + "confirmation=" + encodeURIComponent(id);
}

function confirmationOf(e: unknown): Confirmation | null {
  if (!(e instanceof ApiError) || e.status !== 428) return null;
  const c = e.data?.confirmation as Confirmation | undefined;
  return c?.id ? c : null;
}

let cancelled = new Set<string>();

/** Withdraws the prompt on screen (the request then fails with "Not confirmed"). */
export async function cancelConfirmation() {
  const c = useGate.getState().confirming;
  if (!c) return;
  cancelled = new Set([...cancelled, c.id]);
  useGate.setState({ confirming: null, pinError: null });
  if (pinWaiter?.id === c.id) {
    pinWaiter.reject(new ApiError(0, "Cancelled."));
    pinWaiter = null;
    return; // a PIN confirmation lives only in the browser's device session
  }
  try {
    await transport().request("DELETE", `/api/gate/confirmations/${encodeURIComponent(c.id)}`);
  } catch {
    /* already over */
  }
}

/** Resolves once the daemon has a verdict on c that came some other way (the OS prompt). */
async function settledElsewhere(c: Confirmation): Promise<Confirmation> {
  for (;;) {
    if (cancelled.has(c.id)) throw new ApiError(0, "Cancelled.");
    const next: Confirmation = await transport().request("GET", `/api/gate/confirmations/${encodeURIComponent(c.id)}?wait=25000`);
    if (next.state !== "pending") return next;
  }
}

/**
 * Runs a request that the owner gate may guard. run gets the confirmation ID on the retry;
 * on a 428 the "Confirm on this Mac" sheet shows until the daemon has the owner's answer.
 */
export async function ownerAction<T>(run: (confirmation?: string) => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (e) {
    let c = confirmationOf(e);
    if (!c) throw e;
    if (c.method !== "pin" || c.hello) allowForeground(); // the OS prompt may open
    useGate.setState({ confirming: c, pinError: null });
    try {
      if (c.method === "pin") {
        const id = c.id;
        const local = !!c.pin;
        const byPin = new Promise<Confirmation>((resolve, reject) => {
          pinWaiter = { id, local, resolve, reject };
        });
        // At the machine with Windows Hello open too: whichever answers first.
        c = c.hello ? await Promise.race([byPin, settledElsewhere(c)]) : await byPin;
        if (pinWaiter?.id === id) pinWaiter = null;
      }
      while (c.state === "pending") {
        if (cancelled.has(c.id)) throw new ApiError(0, "Cancelled.");
        const next: Confirmation = await transport().request("GET", `/api/gate/confirmations/${encodeURIComponent(c.id)}?wait=25000`);
        c = next;
        if (useGate.getState().confirming?.id === c.id) useGate.setState({ confirming: c });
      }
      if (cancelled.has(c.id)) throw new ApiError(0, "Cancelled.");
      if (c.state !== "confirmed") throw new ApiError(403, c.error ? `Not confirmed: ${c.error}.` : "Not confirmed.");
      const result = await run(c.id);
      await refreshGate(); // the transport now holds this UI's grace credential
      return result;
    } finally {
      if (useGate.getState().confirming?.id === c.id) useGate.setState({ confirming: null, pinError: null });
    }
  }
}

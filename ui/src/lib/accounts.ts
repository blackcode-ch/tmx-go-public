// AI accounts (the Usage page, #/usage): which harness accounts are connected and how much of
// their quota is used, as the harnesses report it. GET /api/accounts on each connection, then
// accounts.updated events carry the whole payload. The sign-in runs the harness's own login
// in a terminal on the daemon (loginTerminalPath); its progress arrives as login.state.
import { useMemo } from "react";
import { create } from "zustand";
import { DEFAULT_ACCOUNT, normalizeAccounts, type AccountsPayload, type AccountStatus } from "./accounts-format";
import { ownerAction, withConfirmation } from "./owner-gate";
import { onBusEvent, useStore } from "./store";
import type { BusEvent } from "./types";

export * from "./accounts-format";

interface AccountsState {
  payload: AccountsPayload | null;
  /** the first answer (or failure) is in */
  loaded: boolean;
  /** the last GET failed; the page shows it only when it has nothing else to show */
  error: { status: number; message: string } | null;
}

export const useAccounts = create<AccountsState>(() => ({ payload: null, loaded: false, error: null }));

function transport() {
  const t = useStore.getState().transport;
  if (!t) throw new Error("Not connected yet.");
  return t;
}

function put(p: Partial<AccountsPayload> | null | undefined): AccountsPayload | null {
  if (!p || !Array.isArray(p.accounts)) return useAccounts.getState().payload;
  const payload = normalizeAccounts(p);
  useAccounts.setState({ payload, loaded: true, error: null });
  return payload;
}

export async function loadAccounts(): Promise<void> {
  try {
    put(await transport().request<AccountsPayload>("GET", "/api/accounts"));
  } catch (e) {
    const err = e as { status?: number; message?: string };
    useAccounts.setState({ loaded: true, error: { status: err.status ?? 0, message: err.message || "The accounts could not be read." } });
  }
}

onBusEvent((ev: BusEvent) => {
  if (ev.type === "snapshot") void loadAccounts(); // on every (re)connection: the sidebar shows a full quota
  if (ev.type === "accounts.updated") put(ev.data as AccountsPayload);
});

const enc = encodeURIComponent;

/** ?account= of a harness's sign-in routes ("" for the default account). */
export function accountQuery(name?: string): string {
  return name && name !== DEFAULT_ACCOUNT ? `?account=${enc(name)}` : "";
}

export const accountsApi = {
  /** Asks the harnesses again (all of them without a harness; every account of harness without
   * a name); the answer says refreshing. */
  refresh: (harness?: string, account?: string) =>
    transport()
      .request<AccountsPayload>("POST", "/api/accounts/refresh", harness ? { harness, account: account ?? "" } : {})
      .then(put),
  /** Starts the account's own sign-in on the daemon; its terminal is loginTerminalPath(harness, account). */
  login: (harness: string, account?: string) =>
    ownerAction((c) =>
      transport().request<{ session: string }>("POST", withConfirmation(`/api/accounts/${enc(harness)}/login${accountQuery(account)}`, c)),
    ),
  cancelLogin: (harness: string, account?: string) =>
    transport().request<unknown>("DELETE", `/api/accounts/${enc(harness)}/login${accountQuery(account)}`),
  logout: (harness: string, account?: string) =>
    ownerAction((c) =>
      transport().request<AccountsPayload>("POST", withConfirmation(`/api/accounts/${enc(harness)}/logout${accountQuery(account)}`, c)),
    ).then(put),
  /** Adds a signed-out account to harness (its own private folder on the daemon). */
  add: (harness: string, name: string) =>
    transport().request<AccountsPayload>("POST", `/api/accounts/${enc(harness)}/accounts`, { name }).then(put),
  /** Takes an account off the list; its folder, and any sign-in in it, stay on the daemon. */
  remove: (harness: string, name: string) =>
    transport().request<AccountsPayload>("DELETE", `/api/accounts/${enc(harness)}/accounts/${enc(name)}`).then(put),
  /** The account agents of harness use when their launch names none. */
  setLaunchDefault: (harness: string, name: string) =>
    transport().request<AccountsPayload>("PUT", `/api/accounts/${enc(harness)}/launch-default`, { name }).then(put),
};

export function useAccount(harness: string, account: string = DEFAULT_ACCOUNT): AccountStatus | undefined {
  return useAccounts((s) => s.payload?.accounts.find((a) => a.harness === harness && a.name === account));
}

/** harness's accounts (the agents' harness name "agy" is "antigravity" here). */
export function useHarnessAccounts(harness: string): AccountStatus[] {
  const h = harness === "agy" ? "antigravity" : harness || "claude";
  const all = useAccounts((s) => s.payload?.accounts);
  return useMemo(() => (all ?? []).filter((a) => a.harness === h), [all, h]);
}

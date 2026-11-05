// AI accounts (the Usage page): the payload of GET /api/accounts and accounts.updated, and the
// pure helpers that word it. No imports beyond types, so node's test runner can load it.

export type AccountMethod = "subscription" | "api_key" | "unknown";
export type LoginState = "idle" | "running" | "succeeded" | "failed";

export interface AccountIdentity {
  email?: string;
  plan?: string;
  method?: AccountMethod | string;
  org?: string;
}

export interface AccountLimit {
  id: string;
  label: string;
  /** 0–100 */
  used_percent: number;
  /** Unix ms; 0 when only resets_text is known */
  resets_at: number;
  resets_text: string;
}

export interface AccountStatus {
  harness: string;
  /** the account's name among the harness's; "default" is the harness's usual one */
  name: string;
  /** agents of this harness launched without an account use this one */
  launch_default: boolean;
  label: string;
  installed: boolean;
  connected: boolean;
  account?: AccountIdentity | null;
  limits?: AccountLimit[] | null;
  /** why limits is empty */
  limits_note?: string;
  checked_at: number;
  refreshing: boolean;
  error?: string;
  login: { state: LoginState; message?: string };
  /** how to install the harness (a command, or a link) */
  install?: string;
}

export interface AccountsPayload {
  accounts: AccountStatus[];
  updated_at: number;
}

export type LimitTone = "ok" | "warn" | "hot";

/** used_percent as a whole number in 0–100. */
export function usedPct(p: number | undefined | null): number {
  const v = Number(p ?? 0);
  if (!Number.isFinite(v)) return 0;
  return Math.max(0, Math.min(100, Math.round(v)));
}

/** green under 70 %, amber from 70 %, red from 90 %. */
export function limitTone(p: number): LimitTone {
  const v = usedPct(p);
  return v >= 90 ? "hot" : v >= 70 ? "warn" : "ok";
}

/** The words that go with the colour, so it never carries the meaning alone. */
export function toneWords(p: number): string {
  const v = usedPct(p);
  if (v >= 100) return "Used up";
  if (v >= 90) return "Almost used up";
  if (v >= 70) return "Getting close";
  return "";
}

/** 3h 12m, 47m, 2d 5h, under a minute. */
export function untilWords(ms: number): string {
  if (ms < 60_000) return "under a minute";
  const m = Math.floor(ms / 60_000);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

/** "Resets in 3h 12m", "Resets Nov 3 at 6am" (the harness's words), or "". */
export function resetLabel(l: Pick<AccountLimit, "resets_at" | "resets_text">, now = Date.now()): string {
  if (l.resets_at > 0) return l.resets_at > now ? `Resets in ${untilWords(l.resets_at - now)}` : "Resets any moment";
  return l.resets_text ? `Resets ${l.resets_text}` : "";
}

export function methodLabel(m: string | undefined | null): string {
  if (m === "subscription") return "Subscription";
  if (m === "api_key") return "API key";
  return "";
}

/** A short mark for a harness (no logos): Claude → Cl, Codex → Cx, Antigravity → Ag, Gemini CLI → Gm. */
export function harnessMark(harness: string, label?: string): string {
  const known: Record<string, string> = { claude: "Cl", codex: "Cx", antigravity: "Ag", gemini: "Gm" };
  if (known[harness]) return known[harness];
  const s = (label || harness).replace(/[^\p{L}\p{N}]/gu, "");
  return s ? s[0].toUpperCase() + (s[1] ?? "").toLowerCase() : "?";
}

/** "2 of 3 AI accounts connected"; none installed and none known say so. */
export function connectedSummary(accounts: AccountStatus[]): string {
  if (accounts.length === 0) return "No AI tools found";
  const installed = accounts.filter((a) => a.installed).length;
  if (installed === 0) return "No AI tools installed yet";
  const n = accounts.filter((a) => a.connected).length;
  return `${n} of ${accounts.length} AI account${accounts.length === 1 ? "" : "s"} connected`;
}

/** The fullest limit of the connected accounts, for the header and the sidebar. */
export function fullestLimit(accounts: AccountStatus[] | undefined | null): { account: AccountStatus; limit: AccountLimit } | null {
  let best: { account: AccountStatus; limit: AccountLimit } | null = null;
  for (const a of accounts ?? []) {
    if (!a.connected) continue;
    for (const l of a.limits ?? []) if (!best || usedPct(l.used_percent) > usedPct(best.limit.used_percent)) best = { account: a, limit: l };
  }
  return best;
}

/** The newest check of any account (0 when none was checked). */
export function lastChecked(accounts: AccountStatus[] | undefined | null): number {
  return Math.max(0, ...(accounts ?? []).map((a) => a.checked_at || 0));
}

/** A daemon message ("the accounts could not be read: …") as a sentence. */
export function sentence(s: string | undefined | null): string {
  const t = (s ?? "").trim();
  return t ? t[0].toUpperCase() + t.slice(1) : "";
}

/** "Claude", "Claude and Codex", "Claude, Codex and Antigravity" (or "or"). */
export function listWords(words: string[], last = "and"): string {
  return words.length <= 1 ? words.join("") : `${words.slice(0, -1).join(", ")} ${last} ${words[words.length - 1]}`;
}

/** An install hint that is a link rather than a command. */
export function isLink(s: string | undefined | null): boolean {
  return !!s && /^https?:\/\/\S+$/.test(s.trim());
}

/** The account every harness has: its usual home. */
export const DEFAULT_ACCOUNT = "default";

/** An account's key among all of them: "codex/work". */
export function accountKey(a: Pick<AccountStatus, "harness" | "name">): string {
  return `${a.harness}/${a.name || DEFAULT_ACCOUNT}`;
}

/** "Codex", or "Codex · work" for a harness's other accounts. */
export function accountTitle(a: Pick<AccountStatus, "label" | "name">): string {
  return !a.name || a.name === DEFAULT_ACCOUNT ? a.label : `${a.label} · ${a.name}`;
}

/** An account name the daemon accepts (it is also a folder name). */
export function validAccountName(s: string): boolean {
  return /^[a-z0-9][a-z0-9-]{0,31}$/.test(s) && s !== DEFAULT_ACCOUNT;
}

/** Fills in what an older or partial answer leaves out, so views need no null checks. */
export function normalizeAccounts(p: Partial<AccountsPayload> | null | undefined): AccountsPayload {
  const accounts = (p?.accounts ?? []).map((a) => ({
    ...a,
    name: a.name || DEFAULT_ACCOUNT,
    launch_default: a.launch_default ?? (a.name || DEFAULT_ACCOUNT) === DEFAULT_ACCOUNT,
    limits: a.limits ?? [],
    limits_note: a.limits_note ?? "",
    error: a.error ?? "",
    login: { state: a.login?.state ?? "idle", message: a.login?.message ?? "" },
  }));
  return { accounts, updated_at: p?.updated_at ?? 0 };
}

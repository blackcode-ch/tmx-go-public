// AI accounts in the mock daemon (the Usage page): GET /api/accounts, refresh, sign-in, cancel
// and sign-out. A sign-in is a fake process with a terminal (replay first, then live, as the
// daemon's socket does) that "succeeds" after a few seconds, or soon after a pasted code.
// ?mock=usage: the default data with no live script; ?mock=usage-empty: nothing installed;
// ?mock=usage-fail: every sign-in fails; ?mock=usage-down: GET /api/accounts answers 500.
import type { TerminalHandlers, TerminalSession } from "../transport";
import { ApiError } from "../transport";
import type { AccountsPayload, AccountStatus } from "../accounts-format";
import { NOT_MINE } from "./resources";

type Emit = (type: string, data: unknown) => void;

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const INSTALL: Record<string, string> = {
  claude: "curl -fsSL https://claude.ai/install.sh | bash",
  codex: "npm i -g @openai/codex",
  antigravity: "https://antigravity.google/download",
};

const LABEL: Record<string, string> = { claude: "Claude", codex: "Codex", antigravity: "Antigravity" };

function blank(harness: string, installed: boolean): AccountStatus {
  return {
    harness,
    name: "default",
    launch_default: true,
    label: LABEL[harness] ?? harness,
    installed,
    connected: false,
    account: null,
    limits: [],
    limits_note: "",
    checked_at: 0,
    refreshing: false,
    error: "",
    login: { state: "idle", message: "" },
    install: INSTALL[harness] ?? "",
  };
}

/** What each harness reports once it is signed in. */
function signedIn(harness: string, now: number): Partial<AccountStatus> {
  switch (harness) {
    case "claude":
      return {
        account: { email: "me@example.com", plan: "Max", method: "subscription", org: "" },
        limits: [
          { id: "session", label: "Current session", used_percent: 8, resets_at: now + 3 * HOUR + 12 * MIN, resets_text: "" },
          { id: "week", label: "Current week (all models)", used_percent: 54, resets_at: 0, resets_text: "Nov 3 at 6am" },
          { id: "week_opus", label: "Current week (Opus)", used_percent: 76, resets_at: 0, resets_text: "Nov 3 at 6am" },
        ],
        limits_note: "",
      };
    case "codex":
      return {
        account: { email: "me@example.com", plan: "Plus", method: "subscription" },
        limits: [
          { id: "primary", label: "5-hour limit", used_percent: 91, resets_at: now + 47 * MIN, resets_text: "" },
          { id: "secondary", label: "Weekly limit", used_percent: 41, resets_at: now + 2 * DAY + 5 * HOUR, resets_text: "" },
        ],
        limits_note: "",
      };
    default:
      return {
        account: { email: "me@example.com", plan: "Pro", method: "subscription" },
        limits: [],
        limits_note: `${LABEL[harness] ?? harness} does not report its quota outside the app yet.`,
      };
  }
}

class LoginProc {
  out: string[] = [];
  subs = new Set<TerminalHandlers>();
  ended = false;
  timers: ReturnType<typeof setTimeout>[] = [];
  write(s: string) {
    if (this.ended) return;
    this.out.push(s);
    for (const h of this.subs) h.onData(s);
  }
  end() {
    if (this.ended) return;
    this.ended = true;
    this.timers.forEach(clearTimeout);
    for (const h of this.subs) h.onClose();
    this.subs.clear();
  }
}

export class MockAccounts {
  private accounts: AccountStatus[];
  private emit: Emit;
  private scenario: string;
  private procs = new Map<string, LoginProc>();

  constructor(scenario: string, emit: Emit) {
    this.scenario = scenario;
    this.emit = emit;
    const now = Date.now();
    if (scenario === "usage-empty") {
      this.accounts = ["claude", "codex", "antigravity"].map((h) => blank(h, false));
    } else {
      this.accounts = [
        { ...blank("claude", true), connected: true, checked_at: now - 2 * MIN, ...signedIn("claude", now) },
        { ...blank("codex", true), connected: true, checked_at: now - 2 * MIN, ...signedIn("codex", now) },
        blank("antigravity", true),
      ];
    }
  }

  private payload(): AccountsPayload {
    return { accounts: structuredClone(this.accounts), updated_at: Date.now() };
  }

  private changed() {
    this.emit("accounts.updated", this.payload());
  }

  private get(harness: string): AccountStatus {
    const a = this.accounts.find((x) => x.harness === harness);
    if (!a) throw new ApiError(404, `no harness "${harness}"`);
    return a;
  }

  route(method: string, parts: string[], body: unknown): unknown {
    if (parts[0] !== "accounts") return NOT_MINE;
    const [, harness, action] = parts;
    if (!harness) {
      if (this.scenario === "usage-down") throw new ApiError(500, "the accounts could not be read: the daemon's account checker is not running");
      return this.payload();
    }
    if (harness === "refresh" && method === "POST") {
      const only = (body as { harness?: string } | undefined)?.harness;
      this.refresh(only);
      return this.payload();
    }
    const a = this.get(harness);
    if (action === "login" && method === "POST") return this.login(a);
    if (action === "login" && method === "DELETE") {
      this.procs.get(a.harness)?.end();
      this.procs.delete(a.harness);
      a.login = { state: "idle", message: "" };
      this.changed();
      return undefined;
    }
    if (action === "logout" && method === "POST") {
      Object.assign(a, { connected: false, account: null, limits: [], limits_note: "", error: "", checked_at: Date.now() });
      this.changed();
      return this.payload();
    }
    throw new ApiError(404, "Not found.");
  }

  private refresh(only?: string) {
    const list = this.accounts.filter((a) => a.installed && (!only || a.harness === only));
    for (const a of list) a.refreshing = true;
    this.changed();
    setTimeout(() => {
      const now = Date.now();
      for (const a of list) {
        a.refreshing = false;
        a.checked_at = now;
        // a little more used than before, as a busy afternoon would show (tones stay as they are)
        if (a.connected) for (const l of a.limits ?? []) if (l.used_percent < 60) l.used_percent += 1;
      }
      this.changed();
    }, 1400);
  }

  private login(a: AccountStatus): { session: string } {
    if (!a.installed) throw new ApiError(409, `${a.label} is not installed on this machine`);
    this.procs.get(a.harness)?.end();
    const p = new LoginProc();
    this.procs.set(a.harness, p);
    a.login = { state: "running", message: "" };
    this.changed();
    const fail = this.scenario === "usage-fail";
    const dim = (s: string) => `\x1b[38;5;245m${s}\x1b[0m`;
    const at = (ms: number, f: () => void) => p.timers.push(setTimeout(f, ms));
    at(250, () => {
      p.write(`${dim(`$ ${a.harness} login`)}\r\n\r\n`);
      p.write(`Opening your browser to sign in to ${a.label}…\r\n\r\n`);
      p.write(`If the browser did not open, visit:\r\n`);
      p.write(`\x1b[4mhttps://auth.example.com/oauth/authorize?client=${a.harness}&state=mock-${Math.random().toString(36).slice(2, 10)}\x1b[24m\r\n\r\n`);
      p.write(`Paste the code here if prompted > `);
    });
    at(fail ? 3200 : 4600, () => (fail ? this.loginFailed(a, p) : this.loginDone(a, p)));
    return { session: "ls" + Math.random().toString(36).slice(2, 10) };
  }

  private loginDone(a: AccountStatus, p: LoginProc) {
    if (p.ended) return;
    p.write(`\r\n\r\n\x1b[32m✓\x1b[0m Signed in as me@example.com\r\n`);
    Object.assign(a, { connected: true, checked_at: Date.now(), login: { state: "succeeded", message: "" }, ...signedIn(a.harness, Date.now()) });
    this.changed();
    p.timers.push(setTimeout(() => p.end(), 500));
  }

  private loginFailed(a: AccountStatus, p: LoginProc) {
    if (p.ended) return;
    const msg = "The sign-in was not completed: the browser window was closed before you approved it.";
    p.write(`\r\n\r\n\x1b[31m✗\x1b[0m ${msg}\r\n`);
    a.login = { state: "failed", message: msg };
    this.changed();
    p.timers.push(setTimeout(() => p.end(), 300));
  }

  /** The sign-in's terminal; null when the path is not one. */
  openTerminal(path: string, h: TerminalHandlers): TerminalSession | null {
    const m = /^\/api\/accounts\/([^/]+)\/login\/terminal$/.exec(path);
    if (!m) return null;
    const harness = decodeURIComponent(m[1]);
    const p = this.procs.get(harness);
    setTimeout(() => {
      if (!p || p.ended) {
        // the daemon replays what the process wrote, then closes
        h.onOpen();
        for (const s of p?.out ?? []) h.onData(s);
        h.onClose();
        return;
      }
      h.onOpen();
      for (const s of p.out) h.onData(s);
      p.subs.add(h);
    }, 120);
    let line = "";
    return {
      write: (data) => {
        if (!p || p.ended) return;
        for (const ch of data) {
          if (ch === "\r") {
            const code = line.trim();
            line = "";
            if (!code) continue;
            p.write("\r\n" + "\x1b[38;5;245mChecking the code…\x1b[0m");
            const a = this.accounts.find((x) => x.harness === harness);
            p.timers.forEach(clearTimeout);
            p.timers = [];
            if (a) p.timers.push(setTimeout(() => (this.scenario === "usage-fail" ? this.loginFailed(a, p) : this.loginDone(a, p)), 900));
          } else if (ch === "\x7f") {
            if (line) {
              line = line.slice(0, -1);
              p.write("\b \b");
            }
          } else if (ch >= " ") {
            line += ch;
            p.write(ch);
          }
        }
      },
      resize: () => {},
      close: () => void p?.subs.delete(h),
    };
  }
}

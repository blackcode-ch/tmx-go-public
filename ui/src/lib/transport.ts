// How the UI reaches the daemon. One interface, three implementations:
//
//  - http:   fetch + WebSocket. Browser mode (the daemon serves the page; token from #token=)
//            and desktop mode (Wails window; URL + token from the Go binding). Verified on macOS:
//            WKWebView at wails://wails may fetch http://127.0.0.1:8787 (CORS preflight passes
//            when the daemon allows the Wails origin) and open its WebSockets.
//  - bridge: desktop fallback when the webview blocks direct access: every call goes through the
//            bound Go struct (App.Request), events are relayed as Wails events ("tmx:event").
//  - mock:   in-memory fake fleet with simulated live activity (?mock=1 or VITE_MOCK=1).
import type { BusEvent } from "./types";

export type TransportKind = "http" | "bridge" | "mock";

export interface EventHandlers {
  onOpen(): void;
  onMessage(ev: BusEvent): void;
  onClose(): void;
}

export interface EventStream {
  close(): void;
}

export interface TerminalHandlers {
  onOpen(): void;
  onData(chunk: Uint8Array | string): void;
  onClose(): void;
}

export interface TerminalSession {
  write(data: string): void;
  resize(cols: number, rows: number): void;
  close(): void;
}

// Terminal sockets share one framing (binary output, replay first; text JSON input/resize), so
// a terminal is named by its socket path: an agent's pty, or a harness's sign-in.
const AGENT_TERMINAL = /^\/api\/agents\/([^/?]+)\/terminal$/;

/** The socket of an agent's terminal. */
export function agentTerminalPath(agentId: string): string {
  return `/api/agents/${encodeURIComponent(agentId)}/terminal`;
}

/** The socket of a harness account's sign-in (the AI accounts page's Connect). */
export function loginTerminalPath(harness: string, account?: string): string {
  const q = account && account !== "default" ? `?account=${encodeURIComponent(account)}` : "";
  return `/api/accounts/${encodeURIComponent(harness)}/login/terminal${q}`;
}

/** The agent whose terminal a socket path names, or null for any other terminal. */
export function agentOfTerminalPath(path: string): string | null {
  const m = AGENT_TERMINAL.exec(path);
  return m ? decodeURIComponent(m[1]) : null;
}

export interface Transport {
  readonly kind: TransportKind;
  /** Daemon base URL, e.g. http://127.0.0.1:8787 */
  readonly baseURL: string;
  readonly token: string;
  readonly desktop: boolean;
  readonly platform: string;
  request<T = unknown>(method: string, path: string, body?: unknown): Promise<T>;
  openEvents(h: EventHandlers): EventStream;
  /** path: a terminal socket (agentTerminalPath, loginTerminalPath) */
  openTerminal(path: string, cols: number, rows: number, h: TerminalHandlers): TerminalSession;
  openExternal(url: string): void;
}

export class ApiError extends Error {
  status: number;
  /** the parsed JSON body of the error answer, when there was one (e.g. a 428's confirmation) */
  data?: Record<string, unknown>;
  constructor(status: number, message: string, data?: Record<string, unknown>) {
    super(message);
    this.status = status;
    this.data = data;
  }
}

function errorFromBody(status: number, text: string): ApiError {
  let msg = text.trim();
  let data: Record<string, unknown> | undefined;
  try {
    const j = JSON.parse(text);
    if (j && typeof j === "object") data = j;
    if (j && typeof j.error === "string") msg = j.error;
  } catch {
    /* plain text */
  }
  if (!msg) msg = status === 401 ? "Not authorized." : `Request failed (HTTP ${status}).`;
  return new ApiError(status, msg, data);
}

function parseJSON<T>(text: string): T {
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

// ---------------------------------------------------------------------------------------------
// HTTP + WebSocket

class GateSession {
  readonly nonce = Array.from(crypto.getRandomValues(new Uint8Array(32)), b => b.toString(16).padStart(2, "0")).join("");
  credential = "";
  receive(path: string, text: string) {
    if (path === "/api/gate/lock") this.credential = "";
    if (!path.startsWith("/api/gate/confirmations/")) return;
    try { const v = JSON.parse(text); if (typeof v.grace_credential === "string") this.credential = v.grace_credential; } catch { /* no credential */ }
  }
}

export class HttpTransport implements Transport {
  private readonly gateSession = new GateSession();
  readonly kind = "http" as const;
  readonly baseURL: string;
  readonly token: string;
  readonly desktop: boolean;
  readonly platform: string;
  private readonly apiBase: string;

  constructor(opts: { baseURL: string; apiBase?: string; token: string; desktop?: boolean; platform?: string }) {
    this.baseURL = opts.baseURL.replace(/\/$/, "");
    this.apiBase = (opts.apiBase ?? opts.baseURL).replace(/\/$/, "");
    this.token = opts.token;
    this.desktop = !!opts.desktop;
    this.platform = opts.platform ?? guessPlatform();
  }

  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    let res: Response;
    try {
      res = await fetch(this.apiBase + path, {
        method,
        headers: {
          ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}),
          "X-Tmx-UI-Session": this.gateSession.nonce,
          "X-Tmx-Grace": this.gateSession.credential,
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
      });
    } catch {
      throw new ApiError(0, "The daemon is not reachable.");
    }
    const text = await res.text();
    if (!res.ok) throw errorFromBody(res.status, text);
    this.gateSession.receive(path, text);
    return parseJSON<T>(text);
  }

  private wsURL(path: string, q: Record<string, string | number> = {}): string {
    const base = this.apiBase.startsWith("http") ? this.apiBase : window.location.origin + this.apiBase;
    const u = new URL(base.replace(/^http/, "ws") + path);
    if (u.protocol !== "wss:") u.searchParams.set("token", this.token);
    for (const [k, v] of Object.entries(q)) u.searchParams.set(k, String(v));
    return u.toString();
  }

  private socket(path: string, q: Record<string, string | number> = {}): WebSocket {
    const url = this.wsURL(path, q);
    // A proxy may log request URLs. Keep the credential in a header over HTTPS;
    // the daemon selects only "tmx", never echoes the bearer protocol.
    return url.startsWith("wss:")
      ? new WebSocket(url, this.token ? ["tmx", `bearer.${this.token}`] : ["tmx"])
      : new WebSocket(url);
  }

  openEvents(h: EventHandlers): EventStream {
    let closed = false;
    const ws = this.socket("/api/events");
    ws.onopen = () => !closed && h.onOpen();
    ws.onmessage = (m) => {
      if (closed || typeof m.data !== "string") return;
      try {
        h.onMessage(JSON.parse(m.data));
      } catch {
        /* ignore malformed frames */
      }
    };
    ws.onclose = () => !closed && h.onClose();
    return {
      close() {
        closed = true;
        ws.close();
      },
    };
  }

  openTerminal(path: string, cols: number, rows: number, h: TerminalHandlers): TerminalSession {
    const ws = this.socket(path, { cols, rows });
    ws.binaryType = "arraybuffer";
    ws.onopen = () => h.onOpen();
    ws.onmessage = (m) => h.onData(typeof m.data === "string" ? m.data : new Uint8Array(m.data as ArrayBuffer));
    ws.onclose = () => h.onClose();
    const send = (o: unknown) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(o));
    return {
      write: (data) => send({ type: "input", data }),
      resize: (c, r) => send({ type: "resize", cols: c, rows: r }),
      close: () => ws.close(),
    };
  }

  openExternal(url: string) {
    const app = wailsApp();
    if (this.desktop && app) void app.OpenURL(url);
    else window.open(url, "_blank", "noopener");
  }
}

// ---------------------------------------------------------------------------------------------
// Wails bridge (desktop fallback)

interface WailsApp {
  Config(): Promise<{ url: string; token: string; platform: string }>;
  Request(method: string, path: string, body: string): Promise<string>;
  RequestWithSession?(method: string, path: string, body: string, session: string, grace: string): Promise<string>;
  StartEvents(): Promise<void>;
  StopEvents(): Promise<void>;
  TerminalOpen(agentId: string, cols: number, rows: number): Promise<string>;
  /** Any terminal socket by path, e.g. a sign-in's (newer builds; older ones relay agents' only). */
  TerminalOpenPath?(path: string, cols: number, rows: number): Promise<string>;
  TerminalSend(handle: string, frame: string): Promise<void>;
  TerminalClose(handle: string): Promise<void>;
  OpenURL(url: string): Promise<void>;
  /** Windows: lets the daemon bring the Windows Hello dialog to the front (older builds lack it). */
  AllowForeground?(): Promise<void>;
}

interface WailsRuntime {
  EventsOn(name: string, cb: (...data: unknown[]) => void): () => void;
}

declare global {
  interface Window {
    go?: { desktop?: { App?: WailsApp } };
    runtime?: WailsRuntime;
  }
}

function wailsApp(): WailsApp | undefined {
  return window.go?.desktop?.App;
}

/** In the desktop app on Windows, lets the daemon's confirmation dialog come to the front. */
export function allowForeground(): void {
  try {
    void wailsApp()?.AllowForeground?.()?.catch(() => {});
  } catch {
    /* not the desktop app */
  }
}

function b64ToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export class BridgeTransport implements Transport {
  private readonly gateSession = new GateSession();
  readonly kind = "bridge" as const;
  readonly desktop = true;
  readonly baseURL: string;
  readonly token: string;
  readonly platform: string;
  private app: WailsApp;
  private rt: WailsRuntime;

  constructor(app: WailsApp, rt: WailsRuntime, cfg: { url: string; token: string; platform: string }) {
    this.app = app;
    this.rt = rt;
    this.baseURL = cfg.url;
    this.token = cfg.token;
    this.platform = cfg.platform;
  }

  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    try {
      const text = this.app.RequestWithSession
        ? await this.app.RequestWithSession(method, path, body !== undefined ? JSON.stringify(body) : "", this.gateSession.nonce, this.gateSession.credential)
        : await this.app.Request(method, path, body !== undefined ? JSON.stringify(body) : "");
      this.gateSession.receive(path, text);
      return parseJSON<T>(text);
    } catch (e) {
      const s = String(e instanceof Error ? e.message : e);
      const m = /^HTTP (\d+): ([\s\S]*)$/.exec(s);
      if (m) throw errorFromBody(Number(m[1]), m[2]);
      throw new ApiError(0, s || "The daemon is not reachable.");
    }
  }

  openEvents(h: EventHandlers): EventStream {
    let closed = false;
    const offEv = this.rt.EventsOn("tmx:event", (d) => {
      if (closed) return;
      try {
        h.onMessage(JSON.parse(String(d)));
      } catch {
        /* ignore */
      }
    });
    const offState = this.rt.EventsOn("tmx:events-state", (s) => {
      if (closed) return;
      if (s === "open") h.onOpen();
      else h.onClose();
    });
    void this.app.StartEvents();
    return {
      close: () => {
        closed = true;
        offEv();
        offState();
        void this.app.StopEvents();
      },
    };
  }

  openTerminal(path: string, cols: number, rows: number, h: TerminalHandlers): TerminalSession {
    let handle = "";
    let closed = false;
    const offs: (() => void)[] = [];
    const agentId = agentOfTerminalPath(path);
    const open =
      agentId !== null
        ? this.app.TerminalOpen(agentId, cols, rows)
        : this.app.TerminalOpenPath
          ? this.app.TerminalOpenPath(path, cols, rows)
          : Promise.reject(new Error("this desktop build relays agents' terminals only"));
    open
      .then((hd) => {
        if (closed) return void this.app.TerminalClose(hd);
        handle = hd;
        offs.push(this.rt.EventsOn("tmx:term:" + hd, (d) => h.onData(b64ToBytes(String(d)))));
        offs.push(this.rt.EventsOn("tmx:term-closed:" + hd, () => h.onClose()));
        h.onOpen();
      })
      .catch(() => h.onClose());
    const send = (o: unknown) => handle && void this.app.TerminalSend(handle, JSON.stringify(o));
    return {
      write: (data) => send({ type: "input", data }),
      resize: (c, r) => send({ type: "resize", cols: c, rows: r }),
      close: () => {
        closed = true;
        offs.forEach((f) => f());
        if (handle) void this.app.TerminalClose(handle);
      },
    };
  }

  openExternal(url: string) {
    void this.app.OpenURL(url);
  }
}

// ---------------------------------------------------------------------------------------------
// Selection

const TOKEN_KEY = "tmx.token";

function guessPlatform(): string {
  const p = navigator.platform.toLowerCase();
  if (p.includes("mac")) return "darwin";
  if (p.includes("win")) return "windows";
  return "linux";
}

/** Browser mode: take #token=… from the URL once, keep it in sessionStorage, strip it. */
export function takeBrowserToken(): string {
  const m = /^#token=([^&]+)/.exec(window.location.hash);
  if (m) {
    const t = decodeURIComponent(m[1]);
    try {
      sessionStorage.setItem(TOKEN_KEY, t);
    } catch {
      /* private mode: keep it in memory only */
    }
    history.replaceState(null, "", window.location.pathname + window.location.search + "#/");
    return t;
  }
  try {
    return sessionStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function mockRequested(): string | null {
  const q = new URLSearchParams(window.location.search);
  const v = q.get("mock");
  if (v !== null) return v || "1";
  if (import.meta.env.VITE_MOCK && import.meta.env.VITE_MOCK !== "0") return String(import.meta.env.VITE_MOCK);
  return null;
}

async function waitForWails(ms: number): Promise<WailsApp | undefined> {
  const start = Date.now();
  while (Date.now() - start < ms) {
    const app = wailsApp();
    if (app && window.runtime) return app;
    // Outside Wails there is no /wails/ runtime at all; stop waiting early.
    if (!/^wails:|wails\.localhost/.test(window.location.href) && Date.now() - start > 50) return undefined;
    await new Promise((r) => setTimeout(r, 25));
  }
  return wailsApp();
}

async function reachable(t: Transport): Promise<boolean> {
  try {
    await Promise.race([
      t.request("GET", "/api/health"),
      new Promise((_, rej) => setTimeout(() => rej(new Error("timeout")), 2500)),
    ]);
    return true;
  } catch {
    return false;
  }
}

export async function createTransport(): Promise<Transport> {
  const mock = mockRequested();
  if (mock) {
    const { MockTransport } = await import("./mock/transport");
    return new MockTransport({ scenario: mock });
  }

  const app = await waitForWails(1500);
  if (app && window.runtime) {
    const cfg = await app.Config();
    const http = new HttpTransport({ baseURL: cfg.url, token: cfg.token, desktop: true, platform: cfg.platform });
    if (await reachable(http)) return http;
    const bridge = new BridgeTransport(app, window.runtime, cfg);
    if (await reachable(bridge)) return bridge;
    return http; // daemon not up yet; the connection manager keeps retrying
  }

  const token = takeBrowserToken();
  // Served by the daemon: same origin. Under `vite dev`, /api is proxied to the daemon.
  return new HttpTransport({ baseURL: window.location.origin, apiBase: "", token });
}

// Companion, the owner's desktop agent app (DESIGN.md "Companion"): the links' status
// (GET /api/companion), the tasks tmx handed to it (GET /api/companion/tasks, then
// companion.task events), the 1:1 conversation (GET /api/companion/conversation, logic in
// companion-chat.ts), and the owner's calls. Kept apart from the fleet store.
import { create } from "zustand";
import {
  CONVERSATION_LIMIT,
  createCompanionChat,
  initialConvState,
  readSeen,
  SEEN_KEY,
  unreadCount,
  type CompanionConversation,
  type ConvState,
} from "./companion-chat";
import { ownerAction, withConfirmation } from "./owner-gate";
import { onBusEvent, useStore } from "./store";
import type { Approval, BusEvent } from "./types";

export type Effects = "none" | "account" | "outward" | "money";

export interface CompanionSpec {
  goal: string;
  context?: string;
  steps?: string[];
  success?: string;
  do_not_touch?: string[];
  where?: string;
  effects?: Effects | "";
}

export type CompanionTaskState =
  | "awaiting_approval"
  | "denied"
  | "queued"
  | "sending"
  | "working"
  | "input_required"
  | "completed"
  | "failed"
  | "canceled"
  | "refused"
  | "unconfirmed";

export interface CompanionTask {
  id: string;
  /** "message" = a conversation message (the Companion page shows those); absent or "task" = a task */
  kind?: "task" | "message";
  body?: string;
  agent_id: string;
  origin: "owner" | "agent";
  approval_id?: string;
  source_approval_id?: string;
  question_approval_id?: string;
  spec: CompanionSpec;
  text: string;
  effects: Effects;
  state: CompanionTaskState;
  remote_id?: string;
  question?: string;
  result?: string;
  error?: string;
  attempts: number;
  next_at?: number;
  created_at: number;
  updated_at: number;
  sent_at?: number;
  finished_at?: number;
}

export interface InstallStatus {
  aios_home: string;
  companion_dir: boolean;
  mcp_path: string;
  skill_path: string;
  installed: boolean;
  foreign: boolean;
  current: boolean;
  enabled: boolean;
  skill: boolean;
  skill_foreign: boolean;
  command?: string;
}

export interface CompanionStatus {
  install: InstallStatus;
  link_token: boolean;
  connected: boolean;
  /** when tmx's key was stored (ms): a new key from Companion changes it */
  key_set_at?: number;
  address: { url: string; source: "config" | "engine.json" | "engine-state" | "default" | string };
  address_error?: string;
  checked: boolean;
  reachable: boolean;
  version?: string;
  key_ok?: boolean;
  label?: string;
  error?: string;
  link_used_at?: number;
  active: number;
  awaiting: number;
  checked_at: number;
}

/** POST /api/companion/open-connect (202). */
export interface OpenConnectResult {
  /** tmx's entry was (re)written into Companion now */
  installed: boolean;
  /** Companion's "Connect tmx?" link went to the operating system */
  opened: boolean;
  /** why it did not open */
  error?: string;
}

export interface CompanionPreview {
  text: string;
  effects: Effects;
  effects_label: string;
  text_hash: string;
  agent: string;
  agent_id: string;
}

/** The Input of an Approval of kind companion. */
export interface CompanionApprovalInput {
  task_id: string;
  text?: string;
  effects?: Effects;
  spec?: CompanionSpec;
  question?: string;
}

export const DONE: CompanionTaskState[] = ["denied", "completed", "failed", "canceled", "refused", "unconfirmed"];
export const isDone = (s: CompanionTaskState) => DONE.includes(s);

export const TASK_STATE_LABEL: Record<CompanionTaskState, string> = {
  awaiting_approval: "Waiting for you",
  denied: "Declined",
  queued: "Queued",
  sending: "Sending",
  working: "Working",
  input_required: "Asks you",
  completed: "Completed",
  failed: "Failed",
  canceled: "Cancelled",
  refused: "Refused by Companion",
  unconfirmed: "Unconfirmed",
};

export const EFFECTS_LABEL: Record<Effects, string> = {
  none: "No money, accounts or outward actions found",
  account: "Touches accounts or access",
  outward: "Acts outward (sends, posts, signs)",
  money: "May spend or move money",
};

interface CompanionState {
  status: CompanionStatus | null;
  /** by id */
  tasks: Record<string, CompanionTask>;
  loaded: boolean;
  checking: boolean;
}

export const useCompanion = create<CompanionState>(() => ({ status: null, tasks: {}, loaded: false, checking: false }));

function transport() {
  const t = useStore.getState().transport;
  if (!t) throw new Error("Not connected yet.");
  return t;
}

const enc = encodeURIComponent;

/** Tasks once per connection (the snapshot event triggers it), then companion.task events. */
export async function loadCompanionTasks() {
  try {
    const list = await transport().request<CompanionTask[] | null>("GET", "/api/companion/tasks?limit=100");
    const tasks: Record<string, CompanionTask> = {};
    for (const t of list ?? []) tasks[t.id] = t;
    useCompanion.setState({ tasks, loaded: true });
  } catch {
    useCompanion.setState({ loaded: true });
  }
}

/** Status; probe asks Companion too (read-only GETs). */
export async function loadCompanionStatus(probe = true): Promise<CompanionStatus | null> {
  useCompanion.setState({ checking: probe });
  try {
    const st = await transport().request<CompanionStatus>("GET", `/api/companion?probe=${probe ? 1 : 0}`);
    useCompanion.setState({ status: st });
    return st;
  } finally {
    useCompanion.setState({ checking: false });
  }
}

function putTask(t: CompanionTask) {
  useCompanion.setState((s) => ({ tasks: { ...s.tasks, [t.id]: t } }));
}

// ------------------------------------------------------------------ the 1:1 conversation

function localStore(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export const useCompanionChat = create<ConvState>(() => initialConvState(readSeen(localStore)));

export const companionChat = createCompanionChat({
  store: useCompanionChat,
  fetch: () => transport().request<CompanionConversation>("GET", `/api/companion/conversation?limit=${CONVERSATION_LIMIT}`),
  // A message that reads like money, accounts or outward actions asks the owner gate (when it is on).
  post: (text, replyTo) =>
    ownerAction((c) =>
      transport().request<CompanionTask>("POST", withConfirmation("/api/companion/messages", c), { text, ...(replyTo ? { reply_to: replyTo } : {}) }),
    ),
  storage: localStore,
});

/** The owner writes to Companion; replyTo (a task id) answers Companion's question on it. */
export const sendCompanionMessage = (text: string, replyTo?: string) => companionChat.send(text, replyTo);

window.addEventListener("storage", (e) => {
  if (e.key === SEEN_KEY) companionChat.syncSeen();
});

/** Companion is set up here (connected, or tmx installed into it), so the conversation is offered. */
export function useCompanionSetUp(): boolean {
  const installed = useCompanion((s) => !!s.status?.install.installed || !!s.status?.connected);
  return useCompanionChat((s) => s.available !== false && (installed || s.connected || s.entries.length > 0));
}

/** Entries from Companion the owner hasn't seen. */
export function useCompanionUnread(): number {
  return useCompanionChat((s) => unreadCount(s.entries, s.seenAt));
}

/** The status once per connection for the sidebar (installed or connected), without asking
 * Companion; it never replaces a status Settings already loaded (that one may be probed). */
async function loadStatusQuietly() {
  if (useCompanion.getState().status) return;
  try {
    const st = await transport().request<CompanionStatus>("GET", "/api/companion?probe=0");
    if (!useCompanion.getState().status) useCompanion.setState({ status: st });
  } catch {
    /* an older daemon: no Companion */
  }
}

onBusEvent((ev: BusEvent) => {
  if (ev.type === "snapshot") {
    void loadCompanionTasks();
    void loadStatusQuietly();
  }
  if (ev.type === "companion.task") {
    const t = ev.data as CompanionTask;
    if (t?.id) putTask(t);
  }
  companionChat.onEvent(ev);
});

export const companionApi = {
  /** The status without the "checking" spinner (the one-click connect polls it). */
  status: (probe: boolean) =>
    transport()
      .request<CompanionStatus>("GET", `/api/companion?probe=${probe ? 1 : 0}`)
      .then((st) => (useCompanion.setState({ status: st }), st)),
  /** One click: tmx installs itself into Companion if needed and opens Companion's "Connect tmx?"
   * on this machine; Companion hands tmx its key when the owner clicks Connect there. */
  openConnect: () => transport().request<OpenConnectResult>("POST", "/api/companion/open-connect"),
  connect: (token: string, url?: string) =>
    transport()
      .request<CompanionStatus>("PUT", "/api/companion/connect", { token, url: url || "" })
      .then((st) => (useCompanion.setState({ status: st }), companionChat.refreshSoon(0), st)),
  disconnect: () =>
    transport()
      .request<CompanionStatus>("DELETE", "/api/companion/connect")
      .then((st) => (useCompanion.setState({ status: st }), companionChat.refreshSoon(0), st)),
  install: () =>
    transport()
      .request<CompanionStatus>("POST", "/api/companion/install")
      .then((st) => (useCompanion.setState({ status: st }), st)),
  uninstall: () =>
    transport()
      .request<CompanionStatus>("DELETE", "/api/companion/install")
      .then((st) => (useCompanion.setState({ status: st }), st)),
  rotateLinkToken: () =>
    transport()
      .request<CompanionStatus>("POST", "/api/companion/link-token")
      .then((st) => (useCompanion.setState({ status: st }), st)),
  preview: (spec: CompanionSpec, opts: { agent_id?: string; source_approval_id?: string }) =>
    transport().request<CompanionPreview>("POST", "/api/companion/preview", { spec, ...opts }),
  /** A task with effects asks the owner gate (when it is on). */
  create: (spec: CompanionSpec, opts: { agent_id?: string; source_approval_id?: string; text_hash: string }) =>
    ownerAction((c) => transport().request<CompanionTask>("POST", withConfirmation("/api/companion/tasks", c), { spec, ...opts })).then(
      (t) => (t && putTask(t), t),
    ),
  cancel: (id: string) =>
    transport()
      .request<CompanionTask>("POST", `/api/companion/tasks/${enc(id)}/cancel`)
      .then((t) => (t && putTask(t), t)),
};

export function companionInputOf(a: Approval): CompanionApprovalInput | null {
  if (a.kind !== "companion" || !a.input || typeof a.input !== "object") return null;
  return a.input as CompanionApprovalInput;
}

/** The task a Needs-you item was handed over in (newest first). */
export function taskForSource(tasks: Record<string, CompanionTask>, approvalId: string): CompanionTask | undefined {
  return Object.values(tasks)
    .filter((t) => t.source_approval_id === approvalId)
    .sort((a, b) => b.created_at - a.created_at)[0];
}

/** Tasks newest first; messages are left out (they live in the Companion conversation). */
export function sortedTasks(tasks: Record<string, CompanionTask>): CompanionTask[] {
  return Object.values(tasks)
    .filter((t) => t.kind !== "message")
    .sort((a, b) => b.created_at - a.created_at);
}

/** Lines of a textarea → list (blank lines dropped). */
export function lines(s: string): string[] {
  return s
    .split("\n")
    .map((l) => l.replace(/^\s*(?:\d+[.)]|[-*•])\s+/, "").trim())
    .filter(Boolean);
}

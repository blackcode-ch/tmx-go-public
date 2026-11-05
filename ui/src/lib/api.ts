// Typed calls for the API table in DESIGN.md.
import type { Upload } from "./attachments";
import { ownerAction, withConfirmation } from "./owner-gate";
import { useStore } from "./store";
import type {
  Agent,
  AgentUpdate,
  Approval,
  ConfigView,
  CreateAgentBody,
  CreateProjectBody,
  DecideBody,
  AdoptBody,
  DiscoveredAgent,
  GateStatus,
  HarnessInfo,
  GuardSettings,
  GuardRule,
  RuleState,
  Mode,
  ModeChange,
  PatchAgentBody,
  PatchProjectBody,
  Policy,
  Profile,
  Project,
  SafetyInfo,
  PinStatus,
  P2PStatus,
  P2PPeerRow,
} from "./types";

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

const enc = encodeURIComponent;

export const api = {
  // The owner PIN (ownerpin.go): set/changed at the machine, or changed with the current PIN.
  pinStatus: () => t().request<PinStatus>("GET", "/api/gate/pin"),
  setPin: (pin: string, oldPin?: string) => t().request<{ set: boolean }>("POST", "/api/gate/pin", oldPin ? { pin, old_pin: oldPin } : { pin }),
  clearPinWaits: () => t().request<{ cleared: boolean }>("POST", "/api/gate/pin/clear"),
  processes: (id: string) => t().request<ProcessInfo[]>("GET", `/api/agents/${enc(id)}/processes`),
  systemTools: () => t().request<ProcessInfo[]>("GET", "/api/system/tools"),
  systemWarnings: () => t().request<string[]>("GET", "/api/system/warnings"),
  processTools: () => t().request<ProcessInfo[]>("GET", "/api/processes"),
  createProject: (b: CreateProjectBody) => t().request<Project>("POST", "/api/projects", b),
  patchProject: (id: string, b: PatchProjectBody) => t().request<Project>("PATCH", `/api/projects/${enc(id)}`, b),
  // Gated actions go through ownerAction: a 428 asks for the owner's confirmation, then retries.
  deleteProject: (id: string) => ownerAction((c) => t().request<void>("DELETE", withConfirmation(`/api/projects/${enc(id)}`, c))),

  createAgent: (b: CreateAgentBody) => t().request<Agent>("POST", "/api/agents", b),
  agentAction: (id: string, action: "start" | "stop" | "restart" | "interrupt") =>
    t().request<void>("POST", `/api/agents/${enc(id)}/${action}`),
  deleteAgent: (id: string) => t().request<void>("DELETE", `/api/agents/${enc(id)}`),
  patchAgent: (id: string, b: PatchAgentBody) => t().request<AgentUpdate>("PATCH", `/api/agents/${enc(id)}`, b),
  /** chat, or the harness's own screen (terminal), on the same conversation; mid-turn it waits */
  setMode: (id: string, mode: Mode) => t().request<ModeChange>("POST", `/api/agents/${enc(id)}/mode`, { mode }),
  /** thread: to the head, the board topic the message is filed under */
  send: (id: string, text: string, uploads?: string[], thread?: string, retryId?: string) =>
    t().request<void>("POST", `/api/agents/${enc(id)}/send`, {
      text,
      ...(retryId ? { retry_id: retryId } : {}),
      ...(uploads?.length ? { uploads } : {}),
      ...(thread ? { thread_id: thread } : {}),
    }),
  /** Stores a file for the agent's next message; data is base64. */
  upload: (id: string, f: { name: string; type: string; data: string }) =>
    t().request<Upload>("POST", `/api/agents/${enc(id)}/uploads`, f),
  getUpload: (id: string, data = false) => t().request<Upload>("GET", `/api/uploads/${enc(id)}${data ? "?data=1" : ""}`),

  decide: (id: string, b: DecideBody) =>
    ownerAction((c) => t().request<Approval>("POST", withConfirmation(`/api/approvals/${enc(id)}`, c), b)),

  getPolicy: () => t().request<Policy>("GET", "/api/policy"),
  putPolicy: (p: Policy) => ownerAction((c) => t().request<Policy>("PUT", withConfirmation("/api/policy", c), p)),

  getConfig: () => t().request<ConfigView>("GET", "/api/config"),
  putConfig: (c: ConfigView) => ownerAction((cc) => t().request<ConfigView>("PUT", withConfirmation("/api/config", cc), c)),

  // Profiles can grant bypassPermissions too, so writes go through the owner gate like the policy.
  createProfile: (p: Partial<Profile>) => ownerAction((c) => t().request<Profile>("POST", withConfirmation("/api/profiles", c), p)),
  putProfile: (slug: string, p: Partial<Profile>) =>
    ownerAction((c) => t().request<Profile>("PUT", withConfirmation(`/api/profiles/${enc(slug)}`, c), p)),
  deleteProfile: (slug: string) => t().request<void>("DELETE", `/api/profiles/${enc(slug)}`),
  resetProfile: (slug: string) => t().request<Profile>("POST", `/api/profiles/${enc(slug)}/reset`),

  harnesses: () => t().request<HarnessInfo[]>("GET", "/api/harnesses"),
  discover: () => t().request<{ agents: DiscoveredAgent[] | null }>("GET", "/api/discover"),
  // Adopting stops a process tmx did not start: the owner gate guards it.
  adopt: (b: AdoptBody) => ownerAction((c) => t().request<Agent>("POST", withConfirmation("/api/adopt", c), b)),

  getSafety: () => t().request<SafetyInfo>("GET", "/api/safety"),
  putGuard: (g: GuardSettings) => ownerAction((c) => t().request<GuardSettings>("PUT", withConfirmation("/api/safety/guard", c), g)),
  /** Flips one guard rule (block | hold | allow); always asks for a fresh proof (the PIN on a phone). */
  putRuleState: (id: string, state: RuleState) =>
    ownerAction((c) =>
      t().request<{ guard: GuardSettings; rules: GuardRule[] }>("PUT", withConfirmation(`/api/safety/guard/rules/${enc(id)}`, c), { state }),
    ),
  /** Ends one standing "Allow always"; always asks for a fresh proof. */
  revokeAlways: (id: string) =>
    ownerAction((c) => t().request<GuardSettings>("DELETE", withConfirmation(`/api/safety/guard/always/${enc(id)}`, c))),
  putGate: (enabled: boolean) => ownerAction((c) => t().request<GateStatus>("PUT", withConfirmation("/api/gate", c), { enabled })),
  /** The grace window's length (0 = off); longer asks for a fresh proof. */
  putGrace: (minutes: number) =>
    ownerAction((c) => t().request<GateStatus>("PUT", withConfirmation("/api/gate/grace", c), { grace_minutes: minutes })),
  /** Also offer Windows Hello / Touch ID at this machine (the PIN always works); asks every time. */
  putHello: (enabled: boolean) =>
    ownerAction((c) => t().request<GateStatus>("PUT", withConfirmation("/api/gate/hello", c), { enabled })),
  /** Ends the grace window now; never asks. */
  lockGate: () => t().request<GateStatus>("POST", "/api/gate/lock"),

  getP2P: () => t().request<P2PStatus>("GET", "/api/p2p"),
  getP2PPeers: () => t().request<P2PPeerRow[]>("GET", "/api/p2p/net/peers"),
  /** Saves config.json's p2p.enabled; takes effect at the next daemon start (see restartP2P). */
  putP2PConfig: (enabled: boolean) => ownerAction((c) => t().request<P2PStatus>("PUT", withConfirmation("/api/p2p/config", c), { enabled })),
  /** A graceful self-restart (agents keep running) so the saved setting above applies now. */
  restartP2P: () => ownerAction((c) => t().request<{ restarting: boolean; back_in: string }>("POST", withConfirmation("/api/p2p/restart", c))),
};

export interface ProcessInfo {
	 promoted?: boolean;
  id: string;
  category: "agent" | "agent_tool" | "system_tool";
  owner_id: string;
  project_id?: string;
  purpose: string;
  started_at: number;
  pid: number;
  command: string;
  state: string;
  restarts: number;
}

/** Optimistically drop an approval from the pending list (the resolved event confirms it). */
export function dropApproval(id: string) {
  useStore.setState((st) => {
    const approvals = { ...st.approvals };
    delete approvals[id];
    return { approvals };
  });
}

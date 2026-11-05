// Pure text/decision logic for the fleet-hosts strip (WP4, lib/fleet-hosts.ts). No runtime
// project imports, so this stays trivially unit-testable (test/fleet-hosts.test.mjs) without a
// daemon connection. Every string here is composed from another install's own data: callers
// must render it as plain text only, never as HTML or instructions.
import type { HostSummary } from "./fleet-hosts";

/** A failed request on these statuses just means the strip has nothing to show: an agent
 * caller (403, owner-only), or an older daemon without the route (404/501). Anything else
 * keeps the last known hosts. */
export function hidesOnStatus(status: number | undefined): boolean {
  return status === 403 || status === 404 || status === 501;
}

export function fleetHostsResult(hosts: HostSummary[] | null | undefined): { loaded: true; hosts: HostSummary[] } {
  return { loaded: true, hosts: hosts ?? [] };
}

/** "testbox · 3 agents · 1 running · 2 need you" - machine and needs-you are left out when empty. */
export function hostStatusLine(h: Pick<HostSummary, "machine" | "agents" | "running" | "needs_you">): string {
  const bits: string[] = [];
  if (h.machine) bits.push(h.machine);
  bits.push(`${h.agents} ${h.agents === 1 ? "agent" : "agents"}`);
  bits.push(`${h.running} running`);
  if (h.needs_you > 0) bits.push(`${h.needs_you} need you`);
  return bits.join(" · ");
}

/** The head's latest activity line, labelled with its name - or null when there is none yet. */
export function hostHeadLine(h: Pick<HostSummary, "head_name" | "head_line">): string | null {
  if (!h.head_line) return null;
  return h.head_name ? `${h.head_name}: ${h.head_line}` : h.head_line;
}

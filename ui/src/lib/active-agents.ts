import type { Agent } from "./types";

export function agentVisible(agent: Agent, showAll: boolean, now: number): boolean {
  if (showAll) return true;
  if (agent.shelved_at) return false;
  if (["working", "starting", "waiting", "queued", "paused"].includes(agent.state)) return true;
  return agent.state === "idle" && now - (agent.last_activity_at || agent.created_at) < 2 * 60 * 60 * 1000;
}

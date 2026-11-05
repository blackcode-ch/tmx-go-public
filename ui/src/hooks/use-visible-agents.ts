import { useMemo } from "react";
import { agentVisible } from "@/lib/active-agents";
import { useStore } from "@/lib/store";
import { useUI } from "@/lib/ui";
import { useNow } from "./use-now";

export function useVisibleAgents() {
  const agents = useStore((s) => s.agents);
  const showAll = useUI((s) => s.showAllAgents);
  const now = useNow(30_000);
  return useMemo(() => Object.fromEntries(Object.entries(agents).filter(([, a]) => agentVisible(a, showAll, now))), [agents, showAll, now]);
}

import { ui, useUI } from "@/lib/ui";

export function AgentListFilter() {
  const showAll = useUI((s) => s.showAllAgents);
  return <label className="inline-flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
    <input type="checkbox" checked={showAll} onChange={(e) => ui.set({ showAllAgents: e.target.checked })} />
    Show all
  </label>;
}

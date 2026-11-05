import type { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";

/** A quiet monogram: head is solid, leads outlined, workers muted. label: the head's shown name
 * (agentLabel), so a head renamed "Architect" reads "A". */
export function AgentAvatar({ agent, label, className }: { agent: Pick<Agent, "name" | "level">; label?: string; className?: string }) {
  const first = (s: string) => (s.match(/[\p{L}\p{N}]/u)?.[0] ?? "?").toUpperCase();
  const letter = agent.level === "head" ? (label ? first(label) : "H") : first(agent.name);
  return (
    <span
      aria-hidden
      className={cn(
        "flex size-6 shrink-0 items-center justify-center rounded-md text-[0.6875rem] font-semibold",
        agent.level === "head" && "bg-foreground text-background",
        agent.level === "lead" && "border border-border-strong bg-background text-foreground",
        agent.level === "worker" && "bg-muted text-muted-foreground",
        className,
      )}
    >
      {letter}
    </span>
  );
}

import { cn } from "@/lib/utils";
import { STATE_LABEL } from "@/lib/format";
import { pauseLabel } from "@/lib/resources";
import type { Agent, AgentState } from "@/lib/types";

/** A small state indicator: working pulses, waiting is amber, error red, stopped hollow. */
export function StateDot({ state, className, size = "sm" }: { state: AgentState; className?: string; size?: "sm" | "md" }) {
  const s = size === "md" ? "size-2.5" : "size-2";
  const base = cn("relative inline-flex shrink-0 rounded-full", s, className);
  switch (state) {
    case "working":
      return (
        <span className={base} aria-label="Working">
          <span className="absolute inset-0 animate-pulse-dot rounded-full bg-state-working" />
        </span>
      );
    case "starting":
      return <span className={cn(base, "animate-pulse-dot border-[1.5px] border-state-working")} aria-label="Starting" />;
    case "waiting":
      return <span className={cn(base, "bg-state-waiting")} aria-label="Needs you" />;
    case "error":
      return <span className={cn(base, "bg-state-error")} aria-label="Error" />;
    case "idle":
      return <span className={cn(base, "bg-state-idle")} aria-label="Idle" />;
    case "paused":
      // Two bars: paused by a resource brake, needs a look.
      return (
        <span className={cn(base, "items-stretch justify-center gap-[1.5px] rounded-none")} aria-label="Paused">
          <span className="w-[2.5px] rounded-[1px] bg-state-waiting" />
          <span className="w-[2.5px] rounded-[1px] bg-state-waiting" />
        </span>
      );
    case "queued":
      return <span className={cn(base, "border-[1.5px] border-dashed border-muted-foreground/70")} aria-label="Queued" />;
    default:
      return <span className={cn(base, "border-[1.5px] border-state-stopped")} aria-label={STATE_LABEL[state] ?? state} />;
  }
}

const TEXT: Partial<Record<AgentState, string>> = {
  working: "text-state-working",
  starting: "text-state-working",
  waiting: "text-[color-mix(in_oklch,var(--state-waiting)_75%,var(--foreground))]",
  paused: "text-[color-mix(in_oklch,var(--state-waiting)_75%,var(--foreground))]",
  error: "text-state-error",
};

export function StateLabel({ state, className }: { state: AgentState; className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground", TEXT[state], className)}>
      <StateDot state={state} />
      {STATE_LABEL[state] ?? state}
    </span>
  );
}

/** StateLabel plus the queue position ("Queued · 2nd") or the brake ("Paused · Write budget"). */
export function AgentStateLabel({ agent, className }: { agent: Agent; className?: string }) {
  if (agent.shelved_at) return <span className={cn("text-xs text-muted-foreground", className)}>Shelved</span>;
  let extra = "";
  if (agent.state === "queued" && agent.queue_pos) extra = ordinal(agent.queue_pos);
  if (agent.state === "paused") extra = pauseLabel(agent);
  if (agent.state === "starting" && agent.queue_reason) extra = agent.queue_reason; // waiting in claude's start queue
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)} title={agent.queue_reason || agent.paused_detail || undefined}>
      <StateLabel state={agent.state} />
      {extra && <span className="tabular truncate text-xs text-muted-foreground">· {extra}</span>}
    </span>
  );
}

export function ordinal(n: number): string {
  const s = n % 100 >= 11 && n % 100 <= 13 ? "th" : ({ 1: "st", 2: "nd", 3: "rd" } as Record<number, string>)[n % 10] ?? "th";
  return `${n}${s}`;
}

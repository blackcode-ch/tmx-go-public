import { Eye, MessagesSquare, SquareTerminal } from "lucide-react";
import { cn } from "@/lib/utils";
import { agentModelLabel, LEVEL_LABEL } from "@/lib/format";
import { harnessLabel } from "@/lib/harness";
import type { Agent, Harness, Level, Mode, Profile, ProjectStatus } from "@/lib/types";

const pill = "inline-flex h-5 shrink-0 items-center gap-1 rounded-[5px] border px-1.5 text-2xs font-medium whitespace-nowrap";

export function ModelBadge({ agent, className }: { agent: Pick<Agent, "model" | "harness">; className?: string }) {
  return (
    <span className={cn(pill, "border-border bg-muted/60 text-muted-foreground", className)} title={agent.model || undefined}>
      {agentModelLabel(agent)}
    </span>
  );
}

export function LevelBadge({ level, className }: { level: Level; className?: string }) {
  return (
    <span
      className={cn(
        pill,
        level === "head" ? "border-brand/30 bg-brand-soft text-brand" : "border-border text-muted-foreground",
        className,
      )}
    >
      {LEVEL_LABEL[level]}
    </span>
  );
}

const STATUS: Record<ProjectStatus, { label: string; cls: string }> = {
  active: { label: "Active", cls: "border-border text-muted-foreground" },
  paused: { label: "Paused", cls: "border-state-waiting/40 text-[color-mix(in_oklch,var(--state-waiting)_70%,var(--foreground))]" },
  archived: { label: "Archived", cls: "border-border text-subtle-foreground" },
};

export function ProjectStatusBadge({ status, className }: { status: ProjectStatus; className?: string }) {
  const s = STATUS[status] ?? STATUS.active;
  return <span className={cn(pill, s.cls, className)}>{s.label}</span>;
}

export function CountBadge({ n, tone = "default", className }: { n: number; tone?: "default" | "attention"; className?: string }) {
  if (n <= 0) return null;
  return (
    <span
      className={cn(
        "tabular inline-flex h-[1.125rem] min-w-[1.125rem] items-center justify-center rounded-full px-1.5 text-2xs font-semibold",
        tone === "attention" ? "bg-state-waiting text-black/80" : "bg-muted text-muted-foreground",
        className,
      )}
    >
      {n}
    </span>
  );
}

/** A profile's name, for agents whose profile isn't their level's built-in. Read-only profiles get an eye. */
export function ProfileBadge({ profile, className }: { profile: Pick<Profile, "name" | "read_only" | "slug">; className?: string }) {
  return (
    <span
      className={cn(pill, "border-border bg-background text-foreground/80", className)}
      title={profile.read_only ? `Profile ${profile.slug}: read-only (no shell, no file edits)` : `Profile ${profile.slug}`}
    >
      {profile.read_only && <Eye className="size-3 text-muted-foreground" aria-hidden />}
      {profile.name}
    </span>
  );
}

/** Which tool runs the agent and how: "Codex" with a chat or terminal mark. */
export function HarnessBadge({ harness, mode, className }: { harness: Harness; mode?: Mode; className?: string }) {
  const Icon = mode === "terminal" ? SquareTerminal : MessagesSquare;
  return (
    <span
      className={cn(pill, "border-border bg-background text-foreground/80", className)}
      title={`${harnessLabel(harness)}${mode ? (mode === "terminal" ? " in its own screen (read by tmx)" : " in chat mode (structured)") : ""}`}
    >
      {mode && <Icon className="size-3 text-muted-foreground" aria-hidden />}
      {harnessLabel(harness)}
    </span>
  );
}

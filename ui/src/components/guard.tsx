// The dangerous-command guard in the UI: held calls (Approvals of kind "guard") and the rows the
// guard writes into a conversation (held, decided, blocked).
import { Ban, ShieldAlert, ShieldCheck, ShieldX } from "lucide-react";
import { relPath, relativeTime } from "@/lib/format";
import type { Agent, Approval, GuardHold, TimelineEntry } from "@/lib/types";
import { cn } from "@/lib/utils";
import { CopyButton } from "./copy-button";

export function guardOf(a: Pick<Approval, "kind" | "input">): GuardHold | null {
  if (a.kind !== "guard" || !a.input || typeof a.input !== "object") return null;
  return a.input as GuardHold;
}

export function guardMeta(e: TimelineEntry): GuardHold | null {
  const g = e.meta?.guard;
  return g && typeof g === "object" ? (g as GuardHold) : null;
}

const FILE_TOOLS = new Set(["Write", "Edit", "MultiEdit", "NotebookEdit", "Read", "Grep"]);

/** The command (or file path) with the part that tripped the rule highlighted. */
export function GuardCommand({ g, tone, cwd, className }: { g: GuardHold; tone: "hold" | "block"; cwd?: string; className?: string }) {
  const file = FILE_TOOLS.has(g.tool);
  const text = file ? relPath(g.command, cwd) : g.command;
  const at = g.match && !file ? text.indexOf(g.match) : -1;
  const mark = tone === "block" ? "bg-state-error/15 text-foreground ring-state-error/30" : "bg-state-waiting/20 text-foreground ring-state-waiting/40";
  return (
    <div className={cn("group/cmd relative overflow-hidden rounded-lg border bg-code", className)}>
      <div className="flex h-7 items-center justify-between border-b px-3 pr-1">
        <span className="font-mono text-2xs text-subtle-foreground">{file ? `${g.tool} · file` : g.tool === "PowerShell" ? "PowerShell" : "shell"}</span>
        <CopyButton text={g.command} label="Copy command" />
      </div>
      <pre className="scroll-thin max-h-48 overflow-auto px-3 py-2.5 font-mono text-[0.7812rem] leading-[1.65] whitespace-pre-wrap text-foreground/80 [overflow-wrap:anywhere]">
        {at >= 0 ? (
          <>
            {text.slice(0, at)}
            <mark className={cn("rounded-[3px] px-0.5 py-px ring-1", mark)}>{g.match}</mark>
            {text.slice(at + g.match.length)}
          </>
        ) : (
          <span className="text-foreground">{text}</span>
        )}
      </pre>
    </div>
  );
}

/** The body of a guard hold in Needs you and inline in the conversation. */
export function GuardHoldBody({ a, agent, compact }: { a: Approval; agent?: Agent; compact?: boolean }) {
  const g = guardOf(a);
  if (!g) return null;
  return (
    <div className="space-y-2.5">
      <GuardCommand g={g} tone="hold" cwd={agent?.cwd} className={compact ? "text-[0.75rem]" : undefined} />
      <div className="space-y-1 text-xs leading-relaxed">
        {g.detail && (
          <p className="text-foreground/90">
            <span>{sentence(g.detail, g.command)}.</span>
          </p>
        )}
        <p className="text-muted-foreground">{g.why}</p>
      </div>
    </div>
  );
}

/** The detail as a sentence: capitalized, unless it starts with a command name ("pkill kills …"). */
function sentence(detail: string, command: string) {
  if (!detail) return detail;
  const first = detail.split(" ")[0];
  if (command.split(/[\s|;&()]+/).includes(first)) return detail;
  return detail[0].toUpperCase() + detail.slice(1);
}

/** A guard row in the conversation: blocked (an error), held for you, or your decision. */
export function GuardTimelineRow({ e, g, cwd }: { e: TimelineEntry; g: GuardHold; cwd?: string }) {
  if (e.kind === "error" || g.outcome === "block") {
    return (
      <div className="overflow-hidden rounded-xl border border-state-error/35 bg-card" role="alert">
        <div className="flex items-center gap-2 border-b border-state-error/20 bg-diff-del-bg px-3.5 py-2 text-xs">
          <Ban className="size-3.5 shrink-0 text-state-error" />
          <span className="font-medium text-foreground">Blocked by the guard</span>
          <span className="min-w-0 truncate text-muted-foreground">{g.rule_name}</span>
          <span className="tabular ml-auto shrink-0 text-2xs text-subtle-foreground">{relativeTime(e.at)}</span>
        </div>
        <div className="space-y-2 px-3.5 py-3">
          <GuardCommand g={g} tone="block" cwd={cwd} />
          <p className="text-xs leading-relaxed text-muted-foreground">
            {g.detail && <span className="text-foreground/90">{sentence(g.detail, g.command)}. </span>}
            {g.why} The agent was told this is never allowed.
          </p>
        </div>
      </div>
    );
  }
  const decision = String(e.meta?.decision ?? "");
  const Icon = decision === "allowed" ? ShieldCheck : decision === "denied" || decision === "expired" ? ShieldX : ShieldAlert;
  const tone =
    decision === "allowed"
      ? "text-state-ok"
      : decision === "denied" || decision === "expired"
        ? "text-state-error"
        : "text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]";
  return (
    <div className="flex items-center justify-center gap-1.5 text-2xs text-subtle-foreground">
      <Icon className={cn("size-3.5", tone)} />
      <span>{e.text}</span>
    </div>
  );
}

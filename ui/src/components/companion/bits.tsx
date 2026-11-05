// Small pieces of the Companion UI: its avatar, effects badge, task state, task rows.
import { useState } from "react";
import { ChevronRight, Sparkles, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { absoluteTime, relativeTime } from "@/lib/format";
import { companionApi, EFFECTS_LABEL, isDone, TASK_STATE_LABEL, type CompanionTask, type CompanionTaskState, type Effects } from "@/lib/companion";
import { href, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";

/** Companion's mark: the conversation, the sidebar, the page header. */
export function CompanionAvatar({ size = "md", className }: { size?: "sm" | "md" | "lg"; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex shrink-0 items-center justify-center rounded-full bg-brand-soft text-brand",
        size === "sm" && "size-5",
        size === "md" && "size-7",
        size === "lg" && "size-10",
        className,
      )}
    >
      <Sparkles className={size === "lg" ? "size-5" : size === "md" ? "size-3.5" : "size-3"} />
    </span>
  );
}

const pill = "inline-flex h-5 shrink-0 items-center gap-1 rounded-[5px] border px-1.5 text-2xs font-medium whitespace-nowrap";

const EFFECTS_CLS: Record<Effects, string> = {
  none: "border-border text-muted-foreground",
  account: "border-state-waiting/45 bg-state-waiting/10 text-foreground/85",
  outward: "border-state-waiting/45 bg-state-waiting/10 text-foreground/85",
  money: "border-state-error/40 bg-diff-del-bg text-foreground/90",
};

const EFFECTS_SHORT: Record<Effects, string> = { none: "No effects", account: "Accounts", outward: "Outward", money: "Money" };

export function EffectsBadge({ effects, className }: { effects: Effects; className?: string }) {
  const e = EFFECTS_CLS[effects] ? effects : "money";
  return (
    <span className={cn(pill, EFFECTS_CLS[e], className)} title={EFFECTS_LABEL[e]}>
      {EFFECTS_SHORT[e]}
    </span>
  );
}

function dotCls(s: CompanionTaskState): string {
  switch (s) {
    case "working":
    case "sending":
      return "bg-state-working animate-pulse-dot";
    case "awaiting_approval":
    case "input_required":
      return "bg-state-waiting";
    case "queued":
      return "border-[1.5px] border-dashed border-muted-foreground/70";
    case "completed":
      return "bg-state-ok";
    case "failed":
    case "refused":
    case "unconfirmed":
      return "bg-state-error";
    default:
      return "border-[1.5px] border-state-stopped";
  }
}

export function TaskState({ state, className }: { state: CompanionTaskState; className?: string }) {
  return (
    <span className={cn("inline-flex shrink-0 items-center gap-1.5 text-xs font-medium text-muted-foreground", className)}>
      <span className={cn("inline-flex size-2 shrink-0 rounded-full", dotCls(state))} />
      {TASK_STATE_LABEL[state] ?? state}
    </span>
  );
}

/** One task: goal, who asked, state; expands to the exact text sent and Companion's report. */
export function TaskRow({ t, defaultOpen }: { t: CompanionTask; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(!!defaultOpen);
  const [busy, setBusy] = useState(false);
  const agent = useStore((s) => s.agents[t.agent_id]);
  const cancel = async () => {
    setBusy(true);
    try {
      await companionApi.cancel(t.id);
      toast("Cancelled the Companion task");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Collapsible open={open} onOpenChange={setOpen} data-companion-task={t.id}>
      <CollapsibleTrigger className="grid w-full grid-cols-[1fr_auto] items-center gap-x-3 gap-y-0.5 px-4 py-2.5 text-left outline-none hover:bg-accent/40 focus-visible:bg-accent/50 sm:flex sm:h-11 sm:py-0">
        <span className="col-span-2 flex min-w-0 flex-1 items-center gap-2 sm:col-span-1">
          <ChevronRight className={cn("size-3.5 shrink-0 text-subtle-foreground transition-transform duration-150", open && "rotate-90")} />
          <span className="min-w-0 truncate text-[0.8125rem]">{t.spec.goal}</span>
        </span>
        <span className="col-start-1 row-start-2 truncate pl-5.5 text-2xs text-muted-foreground sm:w-[9.375rem] sm:shrink-0 sm:pl-0">
          {t.origin === "agent" ? `asked by ${agent?.name ?? "an agent"}` : `for ${agent?.name ?? "the head"}`}
        </span>
        {t.effects !== "none" && <EffectsBadge effects={t.effects} className="hidden sm:inline-flex" />}
        <TaskState state={t.state} className="col-start-2 row-start-2 justify-self-end sm:w-[8.25rem]" />
        <span className="tabular hidden w-12 shrink-0 text-right text-2xs text-subtle-foreground sm:inline" title={absoluteTime(t.created_at)}>
          {relativeTime(t.created_at)}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="space-y-2.5 px-4 pb-3.5 sm:pl-10">
          {t.question && t.state === "input_required" && (
            <p className="text-xs">
              <span className="font-medium">Companion asks:</span> {t.question}{" "}
              {t.question_approval_id && (
                <a className="text-brand hover:underline" href={href(paths.inbox() + "?id=" + t.question_approval_id)}>
                  Answer in Needs you
                </a>
              )}
            </p>
          )}
          {t.result && (
            <div>
              <p className="mb-1 text-2xs font-medium text-muted-foreground">Companion's report</p>
              <p className="rounded-md border bg-card px-2.5 py-2 text-[0.7812rem] leading-relaxed whitespace-pre-wrap">{t.result}</p>
            </div>
          )}
          {t.error && <p className={cn("text-xs", isDone(t.state) && t.state !== "completed" ? "text-state-error" : "text-muted-foreground")}>{t.error}</p>}
          <div>
            <p className="mb-1 text-2xs font-medium text-muted-foreground">{t.sent_at ? "Sent to Companion" : "What Companion receives"}</p>
            <pre className="scroll-thin max-h-48 overflow-auto rounded-md border bg-muted/40 px-2.5 py-2 font-mono text-[0.7188rem] leading-relaxed whitespace-pre-wrap text-foreground/85">
              {t.text}
            </pre>
          </div>
          {t.state === "awaiting_approval" && t.approval_id && (
            <a className="inline-flex text-xs text-brand hover:underline" href={href(paths.inbox() + "?id=" + t.approval_id)}>
              Decide in Needs you
            </a>
          )}
          {!isDone(t.state) && t.state !== "awaiting_approval" && (
            <Button size="xs" variant="outline" onClick={() => void cancel()} disabled={busy}>
              <X />
              Cancel task
            </Button>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

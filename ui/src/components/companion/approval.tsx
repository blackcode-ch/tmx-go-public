// Companion in Needs you: an agent's hand-off (the exact text, allow/deny), Companion's question
// about a task (answer, or cancel the task), and, on a question an agent asked you, "Hand to
// Companion" with the handed task's progress.
import { Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import { companionInputOf, taskForSource, useCompanion, type Effects } from "@/lib/companion";
import type { Approval } from "@/lib/types";
import { EffectsBadge, TaskState } from "./bits";
import { openHandOff } from "./hand-off-dialog";

export function isCompanionTask(a: Approval) {
  return a.kind === "companion" && a.tool === "companion_task";
}

/** The head's message to Companion that reads like money: the owner sends it or not. */
export function isCompanionMessage(a: Approval) {
  return a.kind === "companion" && a.tool === "companion_message";
}

/** Something for Companion the owner sends or not (an agent's task, or the head's message). */
export function isCompanionSend(a: Approval) {
  return isCompanionTask(a) || isCompanionMessage(a);
}

export function isCompanionQuestion(a: Approval) {
  return a.kind === "companion" && a.tool === "companion_question";
}

export function companionEffects(a: Approval): Effects | undefined {
  return companionInputOf(a)?.effects;
}

export function CompanionApprovalBody({ a, compact }: { a: Approval; compact?: boolean }) {
  const input = companionInputOf(a);
  const task = useCompanion((s) => (input?.task_id ? s.tasks[input.task_id] : undefined));
  if (isCompanionQuestion(a)) {
    return (
      <div className="space-y-1.5">
        <p className="text-[0.875rem] leading-snug font-medium whitespace-pre-wrap text-foreground">{a.question}</p>
        <p className="text-xs text-muted-foreground">
          Companion asks about {task ? <>the task “{task.spec.goal}”</> : "a task tmx handed it"}. Your answer goes to Companion; Cancel task stops it.
        </p>
      </div>
    );
  }
  const eff = input?.effects ?? "money";
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-0 text-[0.875rem] leading-snug font-medium text-foreground">{a.question}</p>
        <EffectsBadge effects={eff} />
      </div>
      <p className="text-xs text-muted-foreground">{a.description} This is exactly what Companion would receive:</p>
      <pre
        className={
          "scroll-thin overflow-auto rounded-md border bg-muted/40 px-2.5 py-2 font-mono text-[0.7188rem] leading-relaxed whitespace-pre-wrap text-foreground/90 " +
          (compact ? "max-h-40" : "max-h-64")
        }
      >
        {input?.text ?? "(the task text is missing)"}
      </pre>
    </div>
  );
}

/** Under a question an agent asked you: hand it to Companion, or how the handed task is doing. */
export function HandToCompanion({ a }: { a: Approval }) {
  const task = useCompanion((s) => taskForSource(s.tasks, a.id));
  if (a.kind !== "question") return null;
  if (task) {
    return (
      <div className="flex flex-wrap items-start gap-x-2 gap-y-1 rounded-md border border-dashed px-2.5 py-1.5 text-xs" data-handed-task={task.id}>
        <Sparkles className="mt-px size-3.5 shrink-0 text-brand" />
        <span className="text-muted-foreground">Handed to Companion</span>
        <TaskState state={task.state} />
        {task.result && <span className="w-full pl-5.5 text-foreground/85 whitespace-pre-wrap">{task.result}</span>}
        {!task.result && task.error && <span className="w-full pl-5.5 text-muted-foreground">{task.error}</span>}
      </div>
    );
  }
  return (
    <Button
      size="xs"
      variant="ghost"
      className="text-muted-foreground"
      onClick={() => openHandOff({ agentId: a.agent_id, sourceApprovalId: a.id, goal: a.question.length <= 200 ? a.question : "", context: a.question.length > 200 ? a.question : "" })}
    >
      <Sparkles />
      Hand to Companion
    </Button>
  );
}

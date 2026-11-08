import { forwardRef, useImperativeHandle, useRef, useState } from "react";
import { ArrowUpToLine, Check, CirclePause, Fingerprint, MessageCircleQuestion, Play, ShieldAlert, Sparkles, SquareTerminal, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, dropApproval } from "@/lib/api";
import { cardAnswerKey, clearCardAnswer, saveCardAnswer, useCardAnswers } from "@/lib/card-answers";
import { PAUSE_LABEL } from "@/lib/resources";
import { useStore } from "@/lib/store";
import { summarizeTool } from "@/lib/tools";
import type { Agent, Approval, DecideBody } from "@/lib/types";
import { cn } from "@/lib/utils";
import { ToolInputView } from "./conversation/tool-input";
import { OwnerCardActions } from "./owner-card-actions";
import { Kbd } from "@/components/ui/kbd";
import { useGateAsks } from "@/lib/owner-gate";
import { GuardHoldBody, guardOf } from "./guard";
import { harnessLabel, terminalMenuHeadline, terminalMenuOf } from "@/lib/harness";
import { ownerName } from "@/lib/owner-name";
import { href, paths } from "@/lib/router";
import { CompanionApprovalBody, companionEffects, isCompanionMessage, isCompanionQuestion, isCompanionSend, isCompanionTask } from "./companion/approval";

export function approvalHeadline(a: Approval): string {
  if (a.tool === "owner_recovery_notice") return "reports restricted owner recovery";
  const menu = terminalMenuOf(a);
  if (menu) return terminalMenuHeadline(menu);
  if (a.kind === "question") return "asks you";
  if (isCompanionTask(a)) return "wants to hand Companion a task";
  if (isCompanionMessage(a)) return "wants to send Companion a message";
  if (isCompanionQuestion(a)) return "has a question from Companion";
  if (a.kind === "brake") return "was paused by tmx";
  if (a.kind === "guard") return "needs your OK";
  if (a.kind === "whatsapp" || a.tool === "mcp__tmxg__whatsapp_send") return "wants to send a WhatsApp message";
  switch (a.tool) {
    case "Bash":
      return "wants to run a command";
    case "Edit":
    case "MultiEdit":
      return "wants to edit a file";
    case "Write":
      return "wants to write a file";
    case "Read":
      return "wants to read a file";
    case "WebFetch":
      return "wants to fetch a page";
    case "WebSearch":
      return "wants to search the web";
    case "NotebookEdit":
      return "wants to edit a notebook";
  }
  const s = summarizeTool(a.tool, a.input);
  return `wants to use ${s.label || s.detail || a.tool}`;
}

/** An agent's WhatsApp message waiting for your OK: who it goes to, and the exact text (sent under your name). */
function WhatsAppApprovalBody({ a, compact }: { a: Approval; compact?: boolean }) {
  const to = (a.input as { chat_name?: string } | null)?.chat_name || "a chat";
  return (
    <div className="space-y-1.5">
      <p className="text-xs text-muted-foreground">
        To <span className="font-medium text-foreground">{to}</span> on WhatsApp, under your name. Allow sends it now.
      </p>
      <p
        className={cn(
          "scroll-thin overflow-auto rounded-md border bg-muted/40 px-2.5 py-2 text-[0.8125rem] leading-relaxed whitespace-pre-wrap text-foreground",
          compact ? "max-h-28" : "max-h-48",
        )}
      >
        {a.question}
      </p>
    </div>
  );
}

/** A menu read off a terminal agent's screen: the dialog's text as the harness drew it. */
function TerminalMenuBody({ a, compact }: { a: Approval; compact?: boolean }) {
  const menu = terminalMenuOf(a)!;
  return (
    <div>
      {a.question && (
        <pre
          className={cn(
            "scroll-thin overflow-auto rounded-md border bg-muted/40 px-2.5 py-2 font-mono text-[0.75rem] leading-relaxed whitespace-pre-wrap text-foreground/90",
            compact ? "max-h-28" : "max-h-48",
          )}
        >
          <span className="sr-only">On {harnessLabel(menu.harness)}'s screen: </span>
          {a.question}
        </pre>
      )}
    </div>
  );
}

/** The options of a terminal menu, picked by tmx with the arrow keys (it checks the highlight before Enter). */
function TerminalMenuActions({ a, busy, decide }: { a: Approval; busy: boolean; decide: (o: string) => void }) {
  const menu = terminalMenuOf(a)!;
  const opts = a.options ?? [];
  return (
    <div className="space-y-2">
      <div className="flex flex-col gap-1">
        {opts.map((o, i) => {
          const highlighted = menu.choices?.[i]?.n === menu.selected;
          return (
            <button
              key={i}
              type="button"
              disabled={busy}
              onClick={() => decide(o)}
              className="group flex min-h-8 items-center gap-2 rounded-md border px-2.5 py-1.5 text-left text-[0.7812rem] outline-none transition-colors duration-150 hover:border-border-strong hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
            >
              <span className="tabular flex size-4 shrink-0 items-center justify-center rounded-[4px] bg-muted font-mono text-[0.625rem] text-muted-foreground">
                {i + 1}
              </span>
              <span className="min-w-0 flex-1 break-words">{o}</span>
              {highlighted && <span className="shrink-0 text-2xs text-subtle-foreground">highlighted</span>}
            </button>
          );
        })}
      </div>
      <a href={href(paths.agent(a.agent_id, "terminal"))} className="inline-flex text-2xs text-muted-foreground hover:text-foreground hover:underline">
        Or answer it in the terminal
      </a>
    </div>
  );
}

export function ApprovalBody({ a, agent, compact }: { a: Approval; agent?: Agent; compact?: boolean }) {
  const project = useStore((s) => a.card?.project_id ? s.projects[a.card.project_id] : undefined);
  if (a.tool === "owner_recovery_notice") return <p role="alert">{a.description}</p>;
  if (terminalMenuOf(a)) return <TerminalMenuBody a={a} compact={compact} />;
  if (a.kind === "guard") return <GuardHoldBody a={a} agent={agent} compact={compact} />;
  if (a.kind === "companion") return <CompanionApprovalBody a={a} compact={compact} />;
  if (a.kind === "whatsapp") return <WhatsAppApprovalBody a={a} compact={compact} />;
  if (a.kind === "brake") {
    return (
      <div className="space-y-1">
        <p className="text-[0.875rem] leading-snug font-medium text-foreground">{PAUSE_LABEL[a.tool] ?? "Paused"}</p>
        <p className="text-xs text-muted-foreground">
          {a.description} {agent?.level === "head" ? "The head cannot answer you until it is resumed." : "Its whole process tree is stopped; nothing is lost."}
        </p>
      </div>
    );
  }
  if (a.kind === "question") {
    return (
      <div className="space-y-1.5">
        <p className="text-[0.875rem] leading-snug font-medium text-foreground">{a.question}</p>
        {a.description && <p className="text-xs text-muted-foreground">{a.description}</p>}
        {a.card?.project_id && <p className="text-xs text-muted-foreground">
          <a className="hover:underline" href={href(paths.project(a.card.project_id))}>{project?.name || a.card.project_id}</a>
          {a.card.urgent && <span className="ml-2 font-medium">Urgent</span>}
          {!!a.card.priority && <span className="ml-2">Priority {a.card.priority}</span>}
          {a.card.owner_only && <span className="ml-2">{ownerName}'s own call</span>}
          {a.card.batchable && <span className="ml-2">Can wait for a batch</span>}
        </p>}
        {a.card?.close_reason && <p className="text-sm">Closed: {a.card.close_reason}</p>}
        {a.card?.context && <p className="whitespace-pre-wrap text-sm">{a.card.context}</p>}
        {a.card?.recommendation && <p className="text-sm">Recommended: {a.card.recommendation}</p>}
        {a.card && <p className="text-xs text-muted-foreground">Version {a.version}</p>}
        {a.card?.action && <div className="space-y-1 text-sm">
          {a.card.action.account && <p>Account: {a.card.action.account}</p>}
          <ol className="list-inside list-decimal">{a.card.action.steps.map((step, i) => <li key={i}>{step}</li>)}</ol>
          <p>Success: {a.card.action.success}</p>
          {!!a.card.action.do_not_touch?.length && <p>Leave alone: {a.card.action.do_not_touch.join("; ")}</p>}
          <p>Touches: {{money: "money", outward: "messages or publishing", account: "account access", none: "not classified (agent-written)"}[a.card.action.effects]}</p>
        </div>}
      </div>
    );
  }
  const s = summarizeTool(a.tool, a.input, agent?.cwd);
  const showDetailLine = !["Bash"].includes(a.tool);
  return (
    <div className="space-y-2">
      {a.description && a.tool !== "Bash" && <p className="text-xs text-muted-foreground">{a.description}</p>}
      {showDetailLine && s.detail && (
        <p className={cn("truncate text-xs text-foreground/85", s.mono && "font-mono text-[0.7188rem]")}>{s.detail}</p>
      )}
      <ToolInputView tool={a.tool} input={a.input} cwd={agent?.cwd} compact={compact} hidePath />
      {a.tool === "Bash" && a.description && <p className="text-xs text-muted-foreground">{a.description}</p>}
    </div>
  );
}

export interface ApprovalActionsHandle {
  allow(): void;
  deny(): void;
  pick(i: number): void;
}

/** Allow / Deny (with an optional reason) for permissions; option buttons + free text for questions. */
export const ApprovalActions = forwardRef<ApprovalActionsHandle, { a: Approval; size?: "sm" | "default"; showKeys?: boolean }>(
  function ApprovalActions(props, ref) {
    return props.a.card && props.a.kind === "question" && !terminalMenuOf(props.a)
      ? <OwnerCardActions ref={ref} a={props.a} size={props.size} />
      : <LegacyApprovalActions {...props} ref={ref} />;
  },
);

const LegacyApprovalActions = forwardRef<ApprovalActionsHandle, { a: Approval; size?: "sm" | "default"; showKeys?: boolean }>(
  function ApprovalActions({ a, size = "sm", showKeys }, ref) {
    const [busy, setBusy] = useState(false);
    const [denying, setDenying] = useState(false);
    const [reason, setReason] = useState("");
    const [answer, setAnswer] = useState("");
    const scope = useStore((s) => s.transport?.baseURL || "");
    const pendingKey = cardAnswerKey(scope, a.id);
    const pending = useCardAnswers((s) => s.items[pendingKey]);
    const reasonRef = useRef<HTMLInputElement>(null);
    const name = useStore((s) => s.agents[a.agent_id]?.name ?? "the agent");
    const gateOn = useGateAsks(); // the gate is on and no grace window is open
    const menu = terminalMenuOf(a);
    const decidable = a.tool !== "owner_recovery_notice" && !menu && (a.kind === "permission" || a.kind === "guard" || a.kind === "whatsapp" || isCompanionSend(a));
    const guarded = a.kind === "guard";
    const pairing = a.tool === "p2p_invite_create" || a.tool === "p2p_join";
    const [always, setAlways] = useState(false);
    const companionQ = isCompanionQuestion(a);
    // The owner gate asks before allowing a guard hold, or a hand-off to Companion with effects.
    const gated = pairing || (gateOn && (guarded || (isCompanionSend(a) && companionEffects(a) !== "none")));

    async function decide(decision: DecideBody["decision"], extra: { message?: string; answer?: string; scope?: DecideBody["scope"] } = {}) {
      if (busy) return;
      setBusy(true);
      try {
        // Another mounted view may have started this card's request already.
        const saved = useCardAnswers.getState().items[pendingKey];
        const request = saved?.request || { decision, ...extra, ...(a.card ? { version: a.version, answer_id: crypto.randomUUID() } : {}) };
        if (a.card && !saved) saveCardAnswer(pendingKey, { scope, card: a, request });
        const result = await api.decide(a.id, request);
        if (a.card) clearCardAnswer(pendingKey);
        if (result.state === "pending") {
          useStore.setState((s) => ({ approvals: { ...s.approvals, [result.id]: result } }));
        } else dropApproval(a.id);
        setBusy(false);
        toast(
          {
            allow_always: request.scope === "any" ? "Allowed always, for any agent" : `Allowed always for agents like ${name}`,
            allow: guarded ? `Allowed once for ${name}` : isCompanionTask(a) ? `Handed ${name}'s task to Companion` : isCompanionMessage(a) ? `Sent ${name}'s message to Companion` : `Allowed for ${name}`,
            deny: a.kind === "brake" ? `${name} stays paused` : companionQ ? "Cancelled the Companion task" : `Denied for ${name}`,
            answer: companionQ ? "Answer sent to Companion" : `Answer sent to ${name}`,
            resume: `Resumed ${name}`,
            raise: `Raised the limit and resumed ${name}`,
            defer: "Saved for later",
            reask: "Question reopened",
          }[request.decision],
        );
      } catch (e) {
        toast.error((e as Error).message || "That didn't go through.");
        setBusy(false);
      }
    }

    function startDeny() {
      setDenying(true);
      setTimeout(() => reasonRef.current?.focus(), 0);
    }

    useImperativeHandle(ref, () => ({
      allow: () => (a.kind === "brake" ? void decide("resume") : decidable && void decide("allow")),
      deny: () =>
        a.kind === "brake"
          ? void decide("deny")
          : decidable
            ? denying
              ? void decide("deny", { message: reason.trim() || undefined })
              : startDeny()
            : undefined,
      pick: (i: number) => {
        const o = a.options?.[i];
        if ((a.kind === "question" || menu) && o) void decide("answer", { answer: o });
      },
    }));

    if (a.tool === "owner_recovery_notice") return <p className="text-sm">This notice cannot authorize access. Review Owner devices on the host.</p>;
    if (a.card && pending) {
      return <div className="space-y-2 text-sm" role="status">
        <p>{busy ? "Sending…" : "Not sent yet — your answer is saved on this device."}</p>
        {pending.request.answer && <p className="whitespace-pre-wrap">{pending.request.answer}</p>}
        <Button size={size} disabled={busy} onClick={() => void decide(pending.request.decision)}>Retry</Button>
        <Button size={size} variant="ghost" disabled={busy} onClick={() => clearCardAnswer(pendingKey)}>Dismiss saved request</Button>
      </div>;
    }
    if (a.card && a.deferred) {
      return <Button size={size} disabled={busy} onClick={() => void decide("reask")}>Ask me again</Button>;
    }
    if (a.kind === "brake") {
      const raise = (a.options ?? []).some((o) => o.startsWith("Raise"));
      return (
        <div className="flex flex-wrap items-center gap-1.5">
          <Button size={size} onClick={() => void decide("resume")} disabled={busy}>
            <Play className="fill-current" />
            Resume
            {showKeys && <Kbd className="ml-0.5 hidden h-4 min-w-4 bg-primary-foreground/15 text-[0.625rem] text-primary-foreground/80 sm:inline-flex">A</Kbd>}
          </Button>
          {raise && (
            <Button size={size} variant="outline" onClick={() => void decide("raise")} disabled={busy}>
              <ArrowUpToLine />
              Raise limit
            </Button>
          )}
          <Button size={size} variant="ghost" className="text-muted-foreground" onClick={() => void decide("deny")} disabled={busy}>
            Keep paused
            {showKeys && <Kbd className="ml-0.5 hidden h-4 min-w-4 text-[0.625rem] sm:inline-flex">D</Kbd>}
          </Button>
        </div>
      );
    }

    if (menu && (a.options?.length ?? 0) > 0) {
      return <TerminalMenuActions a={a} busy={busy} decide={(o) => void decide("answer", { answer: o })} />;
    }

    if (a.kind === "question" || companionQ) {
      const opts = a.options ?? [];
      return (
        <div className="space-y-2.5">
          {opts.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {opts.map((o, i) => (
                <Button key={o} variant="outline" size={size} disabled={busy} onClick={() => void decide("answer", { answer: o })} className="h-auto min-h-7 py-1 text-left whitespace-normal">
                  {showKeys && i < 9 && <Kbd className="mr-0.5 hidden h-4 min-w-4 text-[0.625rem] sm:inline-flex">{i + 1}</Kbd>}
                  {o}
                </Button>
              ))}
            </div>
          )}
          <form
            className="flex gap-1.5"
            onSubmit={(e) => {
              e.preventDefault();
              if (answer.trim()) void decide("answer", { answer: answer.trim() });
            }}
          >
            <Input
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              placeholder={companionQ ? "Your answer for Companion…" : opts.length ? "Or write your own answer…" : "Your answer…"}
              className="h-7 flex-1 text-[0.7812rem]"
              disabled={busy}
            />
            <Button type="submit" size={size} disabled={busy || !answer.trim()}>
              Send
            </Button>
            {a.card && <Button type="button" size={size} variant="ghost" disabled={busy} onClick={() => void decide("defer")}>Later</Button>}
            {companionQ && (
              <Button type="button" size={size} variant="ghost" className="text-muted-foreground" disabled={busy} onClick={() => void decide("deny")}>
                Cancel task
              </Button>
            )}
          </form>
        </div>
      );
    }

    if (denying) {
      return (
        <form
          className="flex flex-wrap items-center gap-1.5"
          onSubmit={(e) => {
            e.preventDefault();
            void decide("deny", { message: reason.trim() || undefined });
          }}
        >
          <Input
            ref={reasonRef}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.stopPropagation();
                setDenying(false);
              }
            }}
            placeholder="Reason (optional), tells the agent what to do instead"
            className="h-7 min-w-[13.75rem] flex-1 text-[0.7812rem]"
            disabled={busy}
          />
          <Button type="submit" size={size} variant="destructive" disabled={busy}>
            Deny
          </Button>
          <Button type="button" size={size} variant="ghost" onClick={() => setDenying(false)} disabled={busy}>
            Cancel
          </Button>
        </form>
      );
    }

    if (always) {
      // Allow always: remembered for this rule and this exact command; listed and revocable in
      // Settings > Safety. Always asks for your confirmation, even inside the grace window.
      return (
        <div className="flex flex-wrap items-center gap-1.5">
          <Button size={size} onClick={() => void decide("allow_always", { scope: "agent" })} disabled={busy} title={pairing ? "Confirm with your owner PIN on your phone" : "Needs your confirmation on this machine"}>
            <Fingerprint />
            Always, agents like {name}
          </Button>
          <Button size={size} variant="outline" onClick={() => void decide("allow_always", { scope: "any" })} disabled={busy} title={pairing ? "Confirm with your owner PIN on your phone" : "Needs your confirmation on this machine"}>
            <Fingerprint />
            Always, {pairing ? "any head or lead" : "any agent"}
          </Button>
          <Button size={size} variant="ghost" onClick={() => setAlways(false)} disabled={busy}>
            Cancel
          </Button>
        </div>
      );
    }

    return (
      <div className="flex flex-wrap items-center gap-1.5">
        <Button size={size} onClick={() => void decide("allow")} disabled={busy} title={pairing ? "Confirm with your owner PIN on your phone" : gated ? "Needs your confirmation on this machine" : undefined}>
          {gated ? <Fingerprint /> : isCompanionSend(a) ? <Sparkles /> : <Check />}
          {guarded ? "Allow once" : isCompanionSend(a) ? "Send to Companion" : "Allow"}
          {showKeys && <Kbd className="ml-0.5 hidden h-4 min-w-4 bg-primary-foreground/15 text-[0.625rem] text-primary-foreground/80 sm:inline-flex">A</Kbd>}
        </Button>
        {guarded && (
          <Button size={size} variant="outline" onClick={() => setAlways(true)} disabled={busy} title={pairing ? "Stop asking for this pairing tool; head and leads only" : "Stop asking for this exact command"}>
            Allow always…
          </Button>
        )}
        <Button size={size} variant="outline" onClick={startDeny} disabled={busy}>
          <X />
          Deny
          {showKeys && <Kbd className="ml-0.5 hidden h-4 min-w-4 text-[0.625rem] sm:inline-flex">D</Kbd>}
        </Button>
      </div>
    );
  },
);

export function GuardFootnote() {
  return <p className="-mt-1 text-2xs text-subtle-foreground">If nobody answers within 10 minutes, the agent is told no.</p>;
}

/** The card shown at the bottom of a conversation while the agent waits for you. */
export function InlineApproval({ a, agent }: { a: Approval; agent: Agent }) {
  const Icon = terminalMenuOf(a)
    ? SquareTerminal
    : a.kind === "question"
      ? MessageCircleQuestion
      : a.kind === "brake"
        ? CirclePause
        : a.kind === "companion"
          ? Sparkles
          : ShieldAlert;
  const g = guardOf(a);
  return (
    <div className="overflow-hidden rounded-xl border border-state-waiting/45 bg-card">
      <div className="flex items-center gap-2 border-b border-state-waiting/25 bg-state-waiting/[0.07] px-3.5 py-2 text-xs">
        <Icon className="size-3.5 text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
        <span className="font-medium text-foreground">{agent.name}</span>
        <span className="min-w-0 truncate text-muted-foreground">{approvalHeadline(a)}</span>
        {g && <GuardRuleChip name={g.rule_name} />}
      </div>
      <div className="space-y-3 px-3.5 py-3">
        <ApprovalBody a={a} agent={agent} />
        <ApprovalActions a={a} />
        {a.kind === "guard" && <GuardFootnote />}
      </div>
    </div>
  );
}

export function GuardRuleChip({ name }: { name: string }) {
  return (
    <span className="ml-auto hidden shrink-0 items-center gap-1 rounded-md border border-state-waiting/40 bg-state-waiting/10 px-1.5 py-px text-2xs font-medium text-foreground/85 sm:inline-flex">
      <ShieldAlert className="size-3 text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
      {name}
    </span>
  );
}

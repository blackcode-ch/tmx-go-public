import { useEffect, useMemo, useRef, useState } from "react";
import { CheckCircle2, CirclePause, MessageCircleQuestion, ShieldAlert, Sparkles, SquareTerminal } from "lucide-react";
import { Kbd } from "@/components/ui/kbd";
import { JobQuarantineCards } from "@/components/job-quarantine";
import { OwnerCardRecovery } from "@/components/owner-card-recovery";
import { AgentAvatar } from "@/components/agent-avatar";
import { ApprovalActions, type ApprovalActionsHandle, ApprovalBody, approvalHeadline, GuardFootnote } from "@/components/approval";
import { guardOf } from "@/components/guard";
import { HandToCompanion } from "@/components/companion/approval";
import { EmptyState, PageBody, PageHeader, PageTitle } from "@/components/page";
import { StateDot } from "@/components/state";
import { isTyping } from "@/hooks/use-hotkeys";
import { useNow } from "@/hooks/use-now";
import { absoluteTime, relativeTime } from "@/lib/format";
import { PAUSE_LABEL } from "@/lib/resources";
import { harnessLabel, terminalMenuOf } from "@/lib/harness";
import { href, paths } from "@/lib/router";
import { pendingApprovals, useStore } from "@/lib/store";
import { cardAnswerKey, useCardSends, cardsWithSavedAnswers, useCardAnswers, useCardDrafts } from "@/lib/card-answers";
import type { Agent, Approval } from "@/lib/types";
import { cn } from "@/lib/utils";

interface Group {
  agent: Agent | undefined;
  agentId: string;
  items: Approval[];
}

function ApprovalCard({
  a,
  agent,
  selected,
  onSelect,
  actionsRef,
}: {
  a: Approval;
  agent?: Agent;
  selected: boolean;
  onSelect: () => void;
  actionsRef?: React.Ref<ApprovalActionsHandle>;
}) {
  const menu = terminalMenuOf(a);
  const Icon = menu
    ? SquareTerminal
    : a.kind === "question"
      ? MessageCircleQuestion
      : a.kind === "brake"
        ? CirclePause
        : a.kind === "companion"
          ? Sparkles
          : ShieldAlert;
  const title = menu
    ? harnessLabel(menu.harness)
    : a.kind === "companion"
      ? a.tool === "companion_task"
        ? "Hand to Companion"
        : a.tool === "companion_message"
          ? "Message to Companion"
          : "Companion asks"
      : a.kind === "question"
      ? "Question"
      : a.kind === "guard"
        ? (guardOf(a)?.rule_name ?? "Held by the guard")
        : a.kind === "brake"
          ? (PAUSE_LABEL[a.tool] ?? "Paused")
          : a.tool;
  return (
    <article
      data-approval={a.id}
      onMouseDown={onSelect}
      onFocusCapture={onSelect}
      className={cn(
        "rounded-xl border bg-card transition-[border-color,box-shadow] duration-150",
        selected ? "border-border-strong ring-2 ring-ring/30" : "hover:border-border-strong",
      )}
    >
      <header className="flex items-center gap-2 px-4 pt-3 text-xs">
        <Icon className="size-3.5 shrink-0 text-[color-mix(in_oklch,var(--state-waiting)_80%,var(--foreground))]" />
        <span className="font-medium text-foreground">{title}</span>
        <span className={cn("text-muted-foreground", a.kind === "guard" && "hidden sm:inline")}>{a.kind === "guard"
            ? "held by the guard"
            : a.kind === "companion"
              ? a.tool === "companion_task" || a.tool === "companion_message"
                ? "the agent asks you to send this"
                : "about a task tmx handed it"
              : approvalHeadline(a)}</span>
        <span className="tabular ml-auto shrink-0 text-2xs text-subtle-foreground" title={absoluteTime(a.created_at)}>
          {relativeTime(a.created_at)}
        </span>
      </header>
      <div className="space-y-3 px-4 pt-2.5 pb-3.5">
        <ApprovalBody a={a} agent={agent} compact />
        <ApprovalActions ref={actionsRef} a={a} showKeys={selected} />
        {a.kind === "guard" && <GuardFootnote />}
        <HandToCompanion a={a} />
      </div>
    </article>
  );
}

export function InboxPage() {
  const approvals = useStore((s) => s.approvals);
  const scope = useStore((s) => s.transport?.baseURL || "");
  const sending = useCardSends((s) => s.items);
  const pendingAnswers = useCardAnswers((s) => s.items);
  const drafts = useCardDrafts((s) => s.items);
  const agents = useStore((s) => s.agents);
  const projects = useStore((s) => s.projects);
  useNow(20_000);
  const list = useMemo(() => {
    const merged = cardsWithSavedAnswers(approvals, scope, pendingAnswers, drafts);
    return pendingApprovals(merged).filter((a) => !sending[cardAnswerKey(scope, a.id)]);
  }, [approvals, pendingAnswers, drafts, scope, sending]);
  const sendingCount = Object.values(pendingAnswers).filter((p) => p.scope === scope && sending[cardAnswerKey(scope, p.card.id)]).length;
  const deferred = Object.values(approvals).filter((a) => a.state === "pending" && a.deferred);

  const groups = useMemo(() => {
    const m = new Map<string, Group>();
    for (const a of list) {
      const g = m.get(a.agent_id) ?? { agent: agents[a.agent_id], agentId: a.agent_id, items: [] };
      g.items.push(a);
      m.set(a.agent_id, g);
    }
    return [...m.values()];
  }, [list, agents]);
  const flat = useMemo(() => groups.flatMap((g) => g.items), [groups]);

  const initial = new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("id");
  const [sel, setSel] = useState<string | null>(initial);
  const selected = flat.find((a) => a.id === sel) ?? flat[0];
  const actions = useRef<ApprovalActionsHandle>(null);

  useEffect(() => {
    if (!selected) return;
    document.querySelector(`[data-approval="${selected.id}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [selected?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (isTyping(e.target) || e.metaKey || e.ctrlKey || e.altKey || document.querySelector("[role=dialog]")) return;
      if (!flat.length) return;
      const i = Math.max(0, flat.findIndex((a) => a.id === selected?.id));
      if (e.key === "j" || e.key === "ArrowDown") {
        e.preventDefault();
        setSel(flat[Math.min(flat.length - 1, i + 1)].id);
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault();
        setSel(flat[Math.max(0, i - 1)].id);
      } else if (e.key === "a") {
        e.preventDefault();
        actions.current?.allow();
      } else if (e.key === "d") {
        e.preventDefault();
        actions.current?.deny();
      } else if (/^[1-9]$/.test(e.key)) {
        e.preventDefault();
        actions.current?.pick(Number(e.key) - 1);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [flat, selected?.id]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader>
        <OwnerCardRecovery />
        <PageTitle>Needs you</PageTitle>
        {list.length > 0 && <span className="tabular text-xs text-muted-foreground">{list.length} pending</span>}
        {list.length > 1 && (
          <span className="ml-auto hidden items-center gap-1.5 text-2xs text-subtle-foreground md:flex">
            <Kbd>J</Kbd>
            <Kbd>K</Kbd> to move · <Kbd>A</Kbd> allow · <Kbd>D</Kbd> deny
          </span>
        )}
      </PageHeader>
      <PageBody width="max-w-[51.25rem]">
        <JobQuarantineCards />
        {sendingCount > 0 && <p role="status" className="mb-4 text-sm text-muted-foreground">Sending {sendingCount === 1 ? "your saved answer" : `${sendingCount} saved answers`}? Your words stay saved until delivery is confirmed.</p>}
        {list.length === 0 ? (
          <EmptyState icon={CheckCircle2} title="Nothing needs you" className="mt-6">
            When an agent asks for permission or has a question for you, it shows up here.
          </EmptyState>
        ) : (
          <div className="space-y-8">
            {groups.map((g) => {
              const project = g.agent?.project_id ? projects[g.agent.project_id] : undefined;
              return (
                <section key={g.agentId} aria-label={g.agent?.name}>
                  <div className="mb-2.5 flex items-center gap-2.5">
                    {g.agent && <AgentAvatar agent={g.agent} className="size-5 text-[0.625rem]" />}
                    <a href={href(paths.agent(g.agentId))} className="text-[0.8125rem] font-medium hover:underline">
                      {g.agent?.name ?? g.agentId}
                    </a>
                    {g.agent && <StateDot state={g.agent.state} />}
                    {project && (
                      <a href={href(paths.project(project.id))} className="text-xs text-muted-foreground hover:text-foreground">
                        {project.name}
                      </a>
                    )}
                    {g.items.length > 1 && <span className="tabular ml-auto text-2xs text-subtle-foreground">{g.items.length} pending</span>}
                  </div>
                  <div className="space-y-2.5">
                    {g.items.map((a) => (
                      <ApprovalCard
                        key={a.id}
                        a={a}
                        agent={g.agent}
                        selected={a.id === selected?.id}
                        onSelect={() => setSel(a.id)}
                        actionsRef={a.id === selected?.id ? actions : undefined}
                      />
                    ))}
                  </div>
                </section>
              );
            })}
          </div>
        )}
        {deferred.length > 0 && <section className="mt-8 space-y-2" aria-label="Saved for later">
          <h2 className="text-sm font-medium">Saved for later</h2>
          {deferred.map((a) => <ApprovalCard key={a.id} a={a} agent={agents[a.agent_id]} selected={false} onSelect={() => {}} />)}
        </section>}
      </PageBody>
    </div>
  );
}

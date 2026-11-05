import { useVisibleAgents } from "@/hooks/use-visible-agents";
import { AgentListFilter } from "@/components/agent-list-filter";
import { useEffect, useMemo, useRef } from "react";
import { AlertTriangle, ArrowRight, ArrowUpRight, FolderClosed, FolderPlus, Mail, MessageSquare, Plus, SquareTerminal, Waypoints } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { AgentAvatar } from "@/components/agent-avatar";
import { approvalHeadline } from "@/components/approval";
import { ModelBadge, ProjectStatusBadge } from "@/components/badges";
import { Composer, type ComposerHandle } from "@/components/conversation/composer";
import { EmptyState, PageBody, PageHeader, PageTitle, Section } from "@/components/page";
import { nowLine } from "@/components/sidebar";
import { HeadPausedBanner, MachineStrip } from "@/components/resources/machine-strip";
import { FleetHostsStrip } from "@/components/resources/fleet-hosts-strip";
import { CompanionStrip } from "@/components/companion/strip";
import { StateDot, StateLabel } from "@/components/state";
import { Truncate } from "@/components/truncate";
import { useNow } from "@/hooks/use-now";
import { firstLine, plural, relativeTime } from "@/lib/format";
import { splitAttachments } from "@/lib/attachments";
import { href, navigate, paths } from "@/lib/router";
import { pendingApprovals, projectTree, useStore } from "@/lib/store";
import { agentLabel } from "@/lib/profiles";
import { summarizeTool } from "@/lib/tools";
import type { Agent, AgentState, Project, TimelineEntry } from "@/lib/types";
import { ui, useUI } from "@/lib/ui";
import { cn } from "@/lib/utils";
import { dailyLine, greeting } from "@/lib/daily";

/** "Needs you" as the Daily card (#235): the first thing in large type, the next two below. */
function NeedsYouStrip() {
  const approvals = useStore((s) => s.approvals);
  const agents = useStore((s) => s.agents);
  const list = pendingApprovals(approvals);
  if (list.length === 0) return null;
  const what = (a: (typeof list)[number]) => {
    const s = summarizeTool(a.tool, a.input, agents[a.agent_id]?.cwd);
    return { text: a.kind === "question" || a.kind === "companion" ? a.question : a.kind === "brake" ? a.description : s.detail, mono: a.kind === "permission" && s.mono };
  };
  const [first, ...rest] = list;
  const f = what(first);
  return (
    <section className="overflow-hidden rounded-2xl border border-state-waiting/45 bg-state-waiting/[0.09]" aria-label="Needs you">
      <div className="px-5 pt-4 pb-4">
        <div className="flex items-center gap-2 text-xs font-semibold tracking-wide text-[color-mix(in_oklab,var(--state-waiting)_55%,var(--foreground))] uppercase">
          <span className="size-2 rounded-full bg-state-waiting" />
          {list.length === 1 ? "One thing needs you" : `Needs you · 1 of ${list.length}`}
        </div>
        <p className={cn("mt-2 line-clamp-3 text-lg leading-snug font-semibold [overflow-wrap:anywhere]", f.mono && "font-mono text-base")}>{f.text || approvalHeadline(first)}</p>
        <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">
          <span className="text-sm text-muted-foreground">
            <span className="font-medium text-foreground">{agents[first.agent_id]?.name ?? "agent"}</span> · {approvalHeadline(first)} · {relativeTime(first.created_at)}
          </span>
          <Button size="sm" className="ml-auto" asChild>
            <a href={href(paths.inbox() + "?id=" + first.id)}>Open</a>
          </Button>
        </div>
      </div>
      {rest.length > 0 && (
        <ul className="divide-y border-t border-state-waiting/25 bg-card/70">
          {rest.slice(0, 2).map((a) => (
            <li key={a.id}>
              <a href={href(paths.inbox() + "?id=" + a.id)} className="flex items-center gap-3 px-5 py-2.5 text-sm outline-none hover:bg-accent/50 focus-visible:bg-accent/60">
                <span className="shrink-0 font-medium">{agents[a.agent_id]?.name ?? "agent"}</span>
                <span className="min-w-0 flex-1 truncate text-muted-foreground">{what(a).text || approvalHeadline(a)}</span>
                <span className="tabular shrink-0 text-2xs text-subtle-foreground">{relativeTime(a.created_at)}</span>
              </a>
            </li>
          ))}
          <li>
            <a href={href(paths.inbox())} className="flex items-center gap-1 px-5 py-2.5 text-sm text-muted-foreground hover:text-foreground">
              {rest.length > 2 ? `${rest.length - 2} more · ` : ""}See all <ArrowRight className="size-3.5" />
            </a>
          </li>
        </ul>
      )}
    </section>
  );
}

function HeadPanel({ head }: { head: Agent }) {
  const entries = useStore((s) => s.timelines[head.id]?.entries);
  const composer = useRef<ComposerHandle>(null);
  const focusHead = useUI((s) => s.focusHead);
  const focusAny = useUI((s) => s.focusComposer);
  useNow(20_000);
  useEffect(() => {
    if (focusHead || focusAny) composer.current?.focus();
  }, [focusHead, focusAny]);
  const now = nowLine(entries, head);
  const headLabel = useStore((s) => agentLabel(head, s.profiles));
  return (
    <section className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 pt-4">
        <AgentAvatar agent={head} label={headLabel} className="size-8 text-[0.8125rem]" />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <a href={href(paths.agent(head.id))} className="text-[0.875rem] font-semibold tracking-tight hover:underline">
              {headLabel}
            </a>
            <ModelBadge agent={head} />
          </div>
          <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
            <StateLabel state={head.state} />
            <span className="text-subtle-foreground">·</span>
            <span className="tabular">{relativeTime(head.last_activity_at)}</span>
          </div>
        </div>
        <div className="ml-auto flex items-center gap-0.5">
          <Button variant="ghost" size="sm" className="text-muted-foreground" asChild>
            <a href={href(paths.matrix())} title="The conversation by thread">
              <Waypoints />
              <span className="hidden sm:inline">Matrix</span>
            </a>
          </Button>
          <Button variant="ghost" size="sm" className="text-muted-foreground" asChild>
            <a href={href(paths.agent(head.id))}>
              <MessageSquare />
              <span className="hidden sm:inline">Open conversation</span>
            </a>
          </Button>
        </div>
      </div>
      <div className="px-4 pt-3">
        {entries === undefined ? (
          <Skeleton className="h-4 w-2/3" />
        ) : (
          <p className="truncate text-sm text-foreground/80">
            {now ? (
              <>
                <span className="text-muted-foreground">{head.state === "working" ? "Now: " : "Last: "}</span>
                {now}
              </>
            ) : (
              <span className="text-muted-foreground">The head plans projects, briefs leads and keeps you posted.</span>
            )}
          </p>
        )}
      </div>
      <div className="p-3 pt-3">
        <Composer ref={composer} agent={head} placeholder="Tell the head what to build, or ask how things are going…" className="bg-background" />
      </div>
    </section>
  );
}

function stateCounts(agents: Agent[]): [AgentState, number][] {
  const order: AgentState[] = ["waiting", "paused", "working", "starting", "error", "queued", "idle", "stopped", "exited"];
  const m = new Map<AgentState, number>();
  for (const a of agents) m.set(a.state, (m.get(a.state) ?? 0) + 1);
  return order.filter((s) => m.get(s)).map((s) => [s, m.get(s)!]);
}

const COUNT_LABEL: Partial<Record<AgentState, string>> = { waiting: "need you", working: "working", idle: "idle", stopped: "stopped", error: "error", starting: "starting", exited: "exited", queued: "queued", paused: "paused" };

function ProjectCard({ p }: { p: Project }) {
  const agents = useVisibleAgents();
  const { lead, workers } = projectTree(agents, p);
  const all = lead ? [lead, ...workers] : workers;
  const last = Math.max(p.updated_at, ...all.map((a) => a.last_activity_at || 0));
  return (
    <a
      href={href(paths.project(p.id))}
      className={cn(
        "group flex flex-col rounded-xl border bg-card p-4 outline-none transition-colors duration-150 hover:border-border-strong focus-visible:ring-2 focus-visible:ring-ring",
        p.status !== "active" && "bg-transparent",
      )}
    >
      <div className="flex items-center gap-2">
        <FolderClosed className="size-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate text-[0.875rem] font-medium">{p.name}</span>
        {p.status !== "active" && <ProjectStatusBadge status={p.status} />}
      </div>
      <p className="mt-2 line-clamp-2 min-h-[2.5rem] text-xs leading-5 text-muted-foreground">{p.brief || "No brief."}</p>
      <div className="mt-3 flex items-center gap-3 border-t pt-3 text-2xs text-muted-foreground">
        {all.length === 0 ? (
          <span>No agents</span>
        ) : (
          <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            {stateCounts(all).map(([s, n]) => (
              <span key={s} className="tabular inline-flex items-center gap-1.5">
                <StateDot state={s} />
                {n} {COUNT_LABEL[s]}
              </span>
            ))}
          </span>
        )}
        <span className="tabular ml-auto shrink-0 text-subtle-foreground">{relativeTime(last)}</span>
      </div>
    </a>
  );
}

type Activity = { e: TimelineEntry; agent: Agent };

/** Bookkeeping calls that would only add noise to the feed. */
const QUIET_TOOLS = new Set(["mcp__tmxg__whoami", "mcp__tmxg__fleet", "mcp__tmxg__inbox", "mcp__tmxg__agent_status", "TodoWrite", "Read", "Glob", "Grep"]);

function activityText(e: TimelineEntry, a: Agent, agents: Record<string, Agent>): { icon: React.ComponentType<{ className?: string }> | null; text: string; mono?: boolean } {
  switch (e.kind) {
    case "assistant":
      return { icon: null, text: firstLine(e.text, 200) };
    case "tool_use": {
      const s = summarizeTool(e.tool, e.input, a.cwd);
      return { icon: s.icon, text: [s.label, s.detail].filter(Boolean).join(" "), mono: s.mono };
    }
    case "message_in": {
      const from = String(e.meta?.from ?? "");
      const name = from === "owner" ? "you" : agents[from]?.name ?? from;
      const { text, files } = splitAttachments(e.text);
      const att = files.length ? plural(files.length, "file") : "";
      return { icon: Mail, text: `From ${name}: ${[firstLine(text, 160), att].filter(Boolean).join(" · ")}` };
    }
    case "user": {
      const { text, files } = splitAttachments(e.text);
      const att = files.length ? plural(files.length, "file") : "";
      return { icon: MessageSquare, text: `You: ${[firstLine(text, 160), att].filter(Boolean).join(" · ")}` };
    }
    case "error":
      return { icon: AlertTriangle, text: firstLine(e.text, 200) };
    default:
      return { icon: SquareTerminal, text: firstLine(e.text, 200) };
  }
}

function ActivityFeed() {
  const timelines = useStore((s) => s.timelines);
  const agents = useStore((s) => s.agents);
  useNow(20_000);
  const items = useMemo(() => {
    const out: Activity[] = [];
    for (const [id, tl] of Object.entries(timelines)) {
      const agent = agents[id];
      if (!agent) continue;
      for (const e of tl.entries.slice(-14)) {
        if (e.kind === "assistant" || e.kind === "tool_use" || e.kind === "message_in" || e.kind === "user" || e.kind === "error") {
          if (e.kind === "assistant" && !e.text.trim()) continue;
          if (e.kind === "tool_use" && QUIET_TOOLS.has(e.tool ?? "")) continue;
          out.push({ e, agent });
        }
      }
    }
    return out.sort((a, b) => b.e.at - a.e.at).slice(0, 14);
  }, [timelines, agents]);

  const loading = Object.keys(timelines).length === 0 && Object.keys(agents).length > 0;
  if (loading) {
    return (
      <div className="space-y-2 rounded-xl border bg-card p-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-4" style={{ width: `${90 - i * 12}%` }} />
        ))}
      </div>
    );
  }
  if (items.length === 0) {
    return <p className="rounded-xl border bg-card px-4 py-6 text-center text-sm text-muted-foreground">No activity yet.</p>;
  }
  return (
    <ul className="divide-y overflow-hidden rounded-xl border bg-card">
      {items.map(({ e, agent }, i) => {
        const t = activityText(e, agent, agents);
        const repeat = i > 0 && items[i - 1].agent.id === agent.id;
        const Icon = t.icon;
        return (
          <li key={agent.id + ":" + e.seq}>
            <a
              href={href(paths.agent(agent.id))}
              className="flex h-10 items-center gap-3 px-4 text-sm outline-none hover:bg-accent/50 focus-visible:bg-accent/60"
            >
              <span className={cn("flex w-[8.25rem] shrink-0 items-center gap-2 truncate", repeat && "invisible")} aria-hidden={repeat}>
                <StateDot state={agent.state} />
                <span className="truncate font-medium text-foreground/90">{agent.name}</span>
              </span>
              <span className="flex min-w-0 flex-1 items-center gap-2 text-muted-foreground">
                {Icon && <Icon className={cn("size-3.5 shrink-0", e.kind === "error" ? "text-state-error" : "text-subtle-foreground")} />}
                <Truncate className={cn(t.mono && "font-mono text-xs")}>{t.text}</Truncate>
              </span>
              <span className="tabular w-14 shrink-0 text-right text-2xs text-subtle-foreground">{relativeTime(e.at)}</span>
            </a>
          </li>
        );
      })}
    </ul>
  );
}

export function OverviewPage() {
  const head = useStore((s) => (s.headId ? s.agents[s.headId] : undefined));
  const projects = useStore((s) => s.projects);
  const agents = useStore((s) => s.agents);
  const machine = useStore((s) => s.machine);
  const list = useMemo(() => {
    const order = { active: 0, paused: 1, archived: 2 } as const;
    return Object.values(projects).sort((a, b) => order[a.status] - order[b.status] || b.updated_at - a.updated_at);
  }, [projects]);
  const running = Object.values(agents).filter((a) => a.state === "working" || a.state === "waiting" || a.state === "starting").length;
  const pending = pendingApprovals(useStore((s) => s.approvals)).length;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        actions={
          <>
            <AgentListFilter />
            <Button variant="outline" size="sm" onClick={() => ui.openNewProject(true)}>
              <Plus />
              New project
            </Button>
          </>
        }
      >
        <PageTitle>Home</PageTitle>
        <span className="tabular hidden truncate text-xs text-muted-foreground sm:inline">
          {machine} · {plural(Object.keys(agents).length, "agent")} · {running} active
        </span>
      </PageHeader>
      <PageBody className="space-y-8">
        {/* Daily (#235): a greeting and one plain line, then what needs you, the conversation to
            pick up, and what moves forward; the machine below. */}
        <div>
          <h2 className="text-2xl leading-tight font-semibold tracking-tight">{greeting(new Date().getHours())}</h2>
          <p className="mt-1 text-muted-foreground">{dailyLine(running, pending)}</p>
        </div>
        <NeedsYouStrip />
        <HeadPausedBanner />
        <Section title="Pick up a conversation">{head ? <HeadPanel head={head} /> : <Skeleton className="h-40 rounded-xl" />}</Section>

        <Section
          title={
            <span className="flex items-center gap-2">
              Moving forward
              {list.length > 0 && <span className="tabular text-xs font-normal text-subtle-foreground">{list.length}</span>}
            </span>
          }
        >
          {list.length === 0 ? (
            <EmptyState
              icon={FolderPlus}
              title="No projects yet"
              actions={
                <>
                  <Button onClick={() => ui.focusHead()}>
                    <MessageSquare />
                    Tell the head what to build
                  </Button>
                  <Button variant="outline" onClick={() => ui.openNewProject(true)}>
                    <Plus />
                    New project
                  </Button>
                </>
              }
            >
              The head sets up projects and leads for you. Describe what you want to build, or create a project yourself.
            </EmptyState>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {list.map((p) => (
                <ProjectCard key={p.id} p={p} />
              ))}
            </div>
          )}
        </Section>
        <MachineStrip />
        <FleetHostsStrip />
        <CompanionStrip />

        {list.length > 0 && (
          <Section
            title="Recent activity"
            right={
              head && (
                <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={() => navigate(paths.agent(head.id))}>
                  Ask the head for a summary <ArrowUpRight />
                </Button>
              )
            }
          >
            <ActivityFeed />
          </Section>
        )}
      </PageBody>
    </div>
  );
}

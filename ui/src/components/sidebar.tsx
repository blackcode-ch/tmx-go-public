import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ChevronRight, CircleDot, FolderClosed, Inbox, LayoutGrid, ListTree, Moon, Plus, Search, Settings, Sun } from "lucide-react";
import { SquareTerminal } from "lucide-react"; // an agent in its own screen (harness-screen.tsx)
import { Gauge, ListChecks } from "lucide-react";
import { UsageDot } from "./accounts/usage-bits";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useNow } from "@/hooks/use-now";
import { useVisibleAgents } from "@/hooks/use-visible-agents";
import { AgentListFilter } from "./agent-list-filter";
import { firstLine, relativeTime, STATE_LABEL } from "@/lib/format";
import { href, paths, usePath } from "@/lib/router";
import { pendingApprovals, projectTree, useStore } from "@/lib/store";
import { toggleTheme, useTheme } from "@/lib/theme";
import { summarizeTool } from "@/lib/tools";
import type { Agent, AgentState, Project, TimelineEntry } from "@/lib/types";
import { MOD, ui } from "@/lib/ui";
import { cn } from "@/lib/utils";
import { ModelBadge } from "./badges";
import { harnessLabel } from "@/lib/harness";
import { agentLabel } from "@/lib/profiles";
import { useUpdate } from "@/lib/update";
import { UpdateDot } from "./update-banner";
import { useLiveRecording } from "./recorder/recording-bar";
import { SidebarChats } from "./chat/sidebar-chats";
import { SidebarWhatsApp } from "./whatsapp/sidebar-wa";
import { ConnectionDot, ReconnectingPill } from "./connection";
import { StateDot } from "./state";
import { Truncate } from "./truncate";
import { BrandMark } from "@/components/brand-mark";

/** What an agent is doing now, from its latest meaningful timeline entry. */
export function nowLine(entries: TimelineEntry[] | undefined, agent: Agent): string {
  if (agent.shelved_at) return "Shelved · Start to resume";
  if (!entries?.length) return "";
  for (let i = entries.length - 1; i >= 0 && i >= entries.length - 12; i--) {
    const e = entries[i];
    if (e.kind === "assistant" && e.text.trim()) return firstLine(e.text, 140);
    if (e.kind === "tool_use") {
      const s = summarizeTool(e.tool, e.input, agent.cwd);
      return [s.label, s.detail].filter(Boolean).join(" ");
    }
    if (e.kind === "message_in") return "Read a message: " + firstLine(e.text, 120);
    if (e.kind === "user") return "You: " + firstLine(e.text, 120);
    if (e.kind === "error") return firstLine(e.text, 140);
  }
  return "";
}

const COLLAPSE_KEY = "tmx.sidebar.collapsed"; // project ids and the sections below → closed
const CHATS_SECTION = "section:chats";
const WA_SECTION = "section:whatsapp";
function readCollapsed(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(COLLAPSE_KEY) ?? "{}");
  } catch {
    return {};
  }
}

function NavItem({
  to,
  icon: Icon,
  label,
  active,
  right,
  onNavigate,
}: {
  to: string;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  active: boolean;
  right?: React.ReactNode;
  onNavigate?: () => void;
}) {
  return (
    <a
      href={href(to)}
      onClick={onNavigate}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex h-7 items-center gap-2 rounded-md px-2 text-[0.8125rem] text-sidebar-foreground outline-none transition-colors duration-150 hover:bg-sidebar-accent/70 focus-visible:ring-2 focus-visible:ring-ring",
        active && "bg-sidebar-accent font-medium text-sidebar-accent-foreground",
      )}
    >
      <Icon className={cn("size-4 shrink-0 text-muted-foreground", active && "text-foreground")} />
      <span className="truncate">{label}</span>
      {right && <span className="ml-auto flex items-center">{right}</span>}
    </a>
  );
}

function AgentRow({
  agent,
  depth,
  active,
  onNavigate,
  guide,
}: {
  agent: Agent;
  depth: number;
  active: boolean;
  onNavigate?: () => void;
  guide?: boolean;
}) {
  return (
    <a
      href={href(paths.agent(agent.id))}
      onClick={onNavigate}
      aria-current={active ? "page" : undefined}
      title={`${agent.name} · ${STATE_LABEL[agent.state]}`}
      className={cn(
        "relative flex h-7 items-center gap-2 rounded-md pr-2 text-[0.8125rem] text-sidebar-foreground/90 outline-none transition-colors duration-150 hover:bg-sidebar-accent/70 focus-visible:ring-2 focus-visible:ring-ring",
        active && "bg-sidebar-accent font-medium text-sidebar-accent-foreground",
        depth === 1 ? "pl-[1.625rem]" : "pl-[2.625rem]",
      )}
    >
      {guide && <span className="absolute top-0 bottom-0 left-[1.8125rem] w-px bg-sidebar-border" aria-hidden />}
      <StateDot state={agent.state} />
      <span className="min-w-0 truncate">{agent.name}</span>
      {agent.harness === "claude" && agent.mode === "terminal" && (
        <SquareTerminal className="size-3 shrink-0 text-subtle-foreground" aria-label="in Claude Code's own screen" />
      )}
      {agent.harness !== "claude" && agent.harness !== "shell" && (
        <span className="shrink-0 rounded-[4px] border border-sidebar-border px-1 font-mono text-[0.625rem] leading-4 text-subtle-foreground" title={harnessLabel(agent.harness)}>
          {agent.harness}
        </span>
      )}
      {agent.state === "queued" && agent.queue_pos ? (
        <span className="tabular ml-auto shrink-0 text-2xs text-subtle-foreground" title={agent.queue_reason}>
          #{agent.queue_pos}
        </span>
      ) : agent.level === "lead" ? (
        <span className="ml-auto shrink-0 text-2xs text-subtle-foreground">{agent.profile && agent.profile !== "lead" ? agent.profile : "lead"}</span>
      ) : (
        agent.profile &&
        agent.profile !== agent.level && <span className="ml-auto shrink-0 truncate text-2xs text-subtle-foreground">{agent.profile}</span>
      )}
    </a>
  );
}

function aggregate(agents: Agent[]): AgentState | null {
  if (agents.some((a) => a.state === "waiting")) return "waiting";
  if (agents.some((a) => a.state === "paused")) return "paused";
  if (agents.some((a) => a.state === "error")) return "error";
  if (agents.some((a) => a.state === "working" || a.state === "starting")) return "working";
  return null;
}

function ProjectItem({
  project,
  agents,
  path,
  collapsed,
  onToggle,
  onNavigate,
}: {
  project: Project;
  agents: Record<string, Agent>;
  path: string;
  collapsed: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
}) {
  const { lead, workers } = projectTree(agents, project);
  const all = lead ? [lead, ...workers] : workers;
  const agg = aggregate(all);
  const active = path === paths.project(project.id);
  const inactive = project.status !== "active";
  return (
    <div>
      <div
        className={cn(
          "group/proj flex h-7 items-center rounded-md text-[0.8125rem] transition-colors duration-150 hover:bg-sidebar-accent/70",
          active && "bg-sidebar-accent",
        )}
      >
        <button
          type="button"
          onClick={onToggle}
          aria-label={collapsed ? `Expand ${project.name}` : `Collapse ${project.name}`}
          aria-expanded={!collapsed}
          className="flex size-7 shrink-0 items-center justify-center rounded-md text-subtle-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight className={cn("size-3.5 transition-transform duration-150", !collapsed && "rotate-90")} />
        </button>
        <a
          href={href(paths.project(project.id))}
          onClick={onNavigate}
          aria-current={active ? "page" : undefined}
          className={cn(
            "flex h-full min-w-0 flex-1 items-center gap-2 rounded-md pr-2 outline-none focus-visible:ring-2 focus-visible:ring-ring",
            active ? "font-medium text-sidebar-accent-foreground" : "text-sidebar-foreground",
            inactive && !active && "text-muted-foreground",
          )}
        >
          <FolderClosed className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate">{project.name}</span>
          <span className="ml-auto flex shrink-0 items-center gap-1.5">
            {inactive && <span className="text-2xs text-subtle-foreground">{project.status}</span>}
            {collapsed && agg && <StateDot state={agg} />}
          </span>
        </a>
      </div>
      {!collapsed && (
        <div className="mt-px flex flex-col gap-px">
          {lead && <AgentRow agent={lead} depth={1} active={path.startsWith(paths.agent(lead.id))} onNavigate={onNavigate} />}
          {workers.map((w, i) => (
            <AgentRow
              key={w.id}
              agent={w}
              depth={lead ? 2 : 1}
              active={path.startsWith(paths.agent(w.id))}
              onNavigate={onNavigate}
              guide={!!lead && i >= 0}
            />
          ))}
          {all.length === 0 && <div className="py-1 pl-[1.625rem] text-2xs text-subtle-foreground">No agents</div>}
        </div>
      )}
    </div>
  );
}

function HeadCard({ path, onNavigate }: { path: string; onNavigate?: () => void }) {
  const head = useStore((s) => (s.headId ? s.agents[s.headId] : undefined));
  const entries = useStore((s) => (s.headId ? s.timelines[s.headId]?.entries : undefined));
  const profiles = useStore((s) => s.profiles);
  useNow(20_000);
  if (!head) {
    return (
      <div className="rounded-lg border border-dashed px-3 py-2.5 text-xs text-muted-foreground">No head agent on this machine yet.</div>
    );
  }
  const active = path.startsWith(paths.agent(head.id));
  const label = agentLabel(head, profiles);
  const now = head.state === "working" || head.state === "waiting" ? nowLine(entries, head) : "";
  return (
    <a
      href={href(paths.agent(head.id))}
      onClick={onNavigate}
      aria-current={active ? "page" : undefined}
      className={cn(
        "block rounded-lg border bg-card px-2.5 py-2 outline-none transition-colors duration-150 hover:border-border-strong focus-visible:ring-2 focus-visible:ring-ring",
        active && "border-border-strong bg-sidebar-accent/60",
        head.state === "paused" && "border-state-waiting/60",
      )}
    >
      <div className="flex items-center gap-2">
        <span className="flex size-5 shrink-0 items-center justify-center rounded-[5px] bg-foreground text-[0.625rem] font-semibold text-background">
          {(label.match(/[\p{L}\p{N}]/u)?.[0] ?? "H").toUpperCase()}
        </span>
        <span className="truncate text-[0.8125rem] font-medium">{label}</span>
        <ModelBadge agent={head} className="ml-auto" />
      </div>
      <div className="mt-1.5 flex items-center gap-1.5 pl-7 text-2xs text-muted-foreground">
        <StateDot state={head.state} />
        <span>{STATE_LABEL[head.state]}</span>
        {head.last_activity_at > 0 && (
          <>
            <span className="text-subtle-foreground">·</span>
            <span className="tabular truncate text-subtle-foreground">{relativeTime(head.last_activity_at)}</span>
          </>
        )}
      </div>
      {now && <Truncate className="mt-1 pl-7 text-2xs text-subtle-foreground">{now}</Truncate>}
    </a>
  );
}

export function SidebarContent({ onNavigate, desktopMac }: { onNavigate?: () => void; desktopMac?: boolean }) {
  const path = usePath();
  const machine = useStore((s) => s.machine);
  const version = useStore((s) => s.version);
  const updateState = useUpdate((s) => s.status?.state);
  const projects = useStore((s) => s.projects);
  const agents = useVisibleAgents();
  const approvals = useStore((s) => s.approvals);
  const pending = pendingApprovals(approvals).length;
  const recording = !!useLiveRecording().rec;
  const { resolved } = useTheme();
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(readCollapsed);

  const sorted = useMemo(() => {
    const order = { active: 0, paused: 1, archived: 2 } as const;
    return Object.values(projects).sort((a, b) => order[a.status] - order[b.status] || a.name.localeCompare(b.name));
  }, [projects]);

  const toggle = (id: string) =>
    setCollapsed((c) => {
      const n = { ...c, [id]: !c[id] };
      try {
        localStorage.setItem(COLLAPSE_KEY, JSON.stringify(n));
      } catch {
        /* ignore */
      }
      return n;
    });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className={cn("drag-region flex h-[3.25rem] shrink-0 items-center gap-2 pr-2", desktopMac ? "pl-[5.875rem]" : "pl-4")}>
        <BrandMark />
        <span className="min-w-0 truncate text-[0.8125rem] font-semibold tracking-tight">{machine || "tmx"}</span>
        <ConnectionDot />
        <ReconnectingPill />
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" className="ml-auto text-muted-foreground" aria-label="Search" onClick={() => ui.openPalette(true)}>
              <Search className="size-4" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Search · {MOD}K</TooltipContent>
        </Tooltip>
      </div>

      <div className="shrink-0 px-2">
        <HeadCard path={path} onNavigate={onNavigate} />
      </div>

      {/* Below the head, everything scrolls as one: the main links, Chats, WhatsApp, Projects. */}
      <div className="scroll-thin mt-3 min-h-0 flex-1 overflow-y-auto pb-3" data-sidebar-scroll>
        <nav className="flex flex-col gap-px px-2" aria-label="Main">
          <NavItem to={paths.overview()} icon={LayoutGrid} label="Home" active={path === "/"} onNavigate={onNavigate} />
          <NavItem to={paths.matrix()} icon={ListTree} label="Matrix" active={path === paths.matrix()} onNavigate={onNavigate} />
          <NavItem
            to={paths.inbox()}
            icon={Inbox}
            label="Needs you"
            active={path === paths.inbox()}
            onNavigate={onNavigate}
            right={
              pending > 0 ? (
                <span className="tabular inline-flex h-[1.125rem] min-w-[1.125rem] items-center justify-center rounded-full bg-state-waiting px-1.5 text-2xs font-semibold text-black/80">
                  {pending}
                </span>
              ) : undefined
            }
          />
          <NavItem
            to={paths.recordings()}
            icon={CircleDot}
            label="Recordings"
            active={path.startsWith(paths.recordings())}
            onNavigate={onNavigate}
            right={recording ? <span className="size-2 animate-pulse rounded-full bg-state-error" aria-label="Recording" /> : undefined}
          />
          <NavItem to={paths.usage()} icon={Gauge} label="Usage" active={path === paths.usage()} onNavigate={onNavigate} right={<UsageDot />} />
          <NavItem to={paths.features()} icon={ListChecks} label="Features" active={path === paths.features()} onNavigate={onNavigate} />
        </nav>

        <SidebarChats path={path} onNavigate={onNavigate} open={!collapsed[CHATS_SECTION]} onToggle={() => toggle(CHATS_SECTION)} />
        <SidebarWhatsApp path={path} onNavigate={onNavigate} open={!collapsed[WA_SECTION]} onToggle={() => toggle(WA_SECTION)} />

        <div className="mt-5 flex items-center px-4 pr-2">
          <span className="text-2xs font-medium tracking-wide text-subtle-foreground uppercase">Projects</span>
          <span className="ml-3"><AgentListFilter /></span>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon-xs" className="ml-auto text-muted-foreground" aria-label="New project" onClick={() => ui.openNewProject(true)}>
                <Plus />
              </Button>
            </TooltipTrigger>
            <TooltipContent>New project · C</TooltipContent>
          </Tooltip>
        </div>
        <div className="mt-1 px-2">
          <div className="flex flex-col gap-0.5">
            {sorted.map((p) => (
              <ProjectItem
                key={p.id}
                project={p}
                agents={agents}
                path={path}
                collapsed={collapsed[p.id] ?? p.status === "archived"}
                onToggle={() => toggle(p.id)}
                onNavigate={onNavigate}
              />
            ))}
          </div>
          <button
            type="button"
            onClick={() => ui.openNewProject(true)}
            className="mt-1 flex h-7 w-full items-center gap-2 rounded-md px-2 text-[0.8125rem] text-muted-foreground outline-none hover:bg-sidebar-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="size-4" />
            New project
          </button>
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-1 border-t border-sidebar-border px-2 py-2">
        <div className="min-w-0 flex-1">
          <NavItem to={paths.settings()} icon={Settings} label="Settings" active={path.startsWith(paths.settings())} onNavigate={onNavigate} />
        </div>
        {version && (
          <a
            href={href(paths.settings()) + "?section=updates"}
            onClick={onNavigate}
            title={updateState && ["available", "downloading", "ready", "installing"].includes(updateState) ? "A new release is on its way" : "Updates"}
            className="tabular flex items-center gap-1 rounded px-1 font-mono text-2xs text-subtle-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            {version}
            <UpdateDot state={updateState} />
          </a>
        )}
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" className="text-muted-foreground" aria-label="Toggle theme" onClick={toggleTheme}>
              {resolved === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{resolved === "dark" ? "Light theme" : "Dark theme"}</TooltipContent>
        </Tooltip>
      </div>
    </div>
  );
}

const WIDTH_KEY = "tmx.sidebar.width";

export function Sidebar({ desktopMac }: { desktopMac?: boolean }) {
  const [width, setWidth] = useState(() => {
    try {
      const v = Number(localStorage.getItem(WIDTH_KEY));
      if (v >= 220 && v <= 380) return v;
    } catch {
      /* ignore */
    }
    return 260;
  });
  const dragging = useRef(false);

  const onMove = useCallback((e: PointerEvent) => {
    if (!dragging.current) return;
    setWidth(Math.min(380, Math.max(220, e.clientX)));
  }, []);
  const onUp = useCallback(() => {
    if (!dragging.current) return;
    dragging.current = false;
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
  }, []);
  useEffect(() => {
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
  }, [onMove, onUp]);
  useEffect(() => {
    try {
      localStorage.setItem(WIDTH_KEY, String(width));
    } catch {
      /* ignore */
    }
  }, [width]);

  return (
    <aside className="relative hidden h-full shrink-0 border-r border-sidebar-border bg-sidebar md:block" style={{ width }}>
      <SidebarContent desktopMac={desktopMac} />
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize sidebar"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "ArrowLeft") setWidth((w) => Math.max(220, w - 16));
          if (e.key === "ArrowRight") setWidth((w) => Math.min(380, w + 16));
        }}
        onPointerDown={(e) => {
          e.preventDefault();
          dragging.current = true;
          document.body.style.cursor = "col-resize";
          document.body.style.userSelect = "none";
        }}
        onDoubleClick={() => setWidth(260)}
        className="absolute top-0 -right-[3px] bottom-0 z-10 w-[0.375rem] cursor-col-resize outline-none after:absolute after:inset-y-0 after:left-[2px] after:w-[2px] after:bg-transparent after:transition-colors after:duration-150 hover:after:bg-border-strong focus-visible:after:bg-ring"
      />
    </aside>
  );
}

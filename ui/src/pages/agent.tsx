import { useEffect, useRef, useState } from "react";
import { ChevronRight, Ellipsis, Play, RotateCw, Square, Trash2, OctagonX, Bot, Sparkles, Pencil, Pin, PinOff } from "lucide-react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { AgentAvatar } from "@/components/agent-avatar";
import { AgentConfig } from "@/components/agent-config";
import { AgentTerminal } from "@/components/agent-terminal";
import { TerminalKeys } from "@/components/classic/terminal-keys";
import { ProcessList } from "@/components/process-list";
import { openHandOff } from "@/components/companion/hand-off-dialog";
import { HarnessBadge, LevelBadge, ModelBadge, ProfileBadge } from "@/components/badges";
import { canOpenScreen, HarnessScreen, switchMode } from "@/components/harness-screen";
import { Conversation, MessageBox } from "@/components/conversation/conversation";
import { MailReceipts } from "@/components/mail-receipts";
import type { ComposerHandle } from "@/components/conversation/composer";
import { CopyButton } from "@/components/copy-button";
import { EmptyState, PageHeader } from "@/components/page";
import { AgentResourceBanner, AgentResourceDetails, AgentUsageInline } from "@/components/resources/agent-resources";
import { AgentStateLabel, StateDot } from "@/components/state";
import { Truncate } from "@/components/truncate";
import { useClassic } from "@/hooks/use-classic";
import { useMedia } from "@/hooks/use-media";
import { useNow } from "@/hooks/use-now";
import { PHONE_QUERY } from "@/hooks/use-phone-classic";
import { useAgentMessagePreference } from "@/lib/agent-message-preference";
import { isAgentView, rememberAgentView, rememberedAgentView, resolveAgentView, visiblePaneView, showPane, SPLIT_MIN_WIDTH, splitApplies, togglePane, type AgentPane } from "@/lib/agent-view";
import { agentPinKey } from "@/lib/agent-pins";
import { api } from "@/lib/api";
import { absoluteTime, displayPath, isRunning, LEVEL_LABEL, relativeTime, STATE_LABEL } from "@/lib/format";
import { harnessLabel, hasChatMode } from "@/lib/harness";
import { agentHeaderLine, stateTone } from "@/lib/phone";
import { agentLabel, isCustomProfile, profileOf } from "@/lib/profiles";
import { agentLine } from "@/lib/ui-profile";
import { href, navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Agent } from "@/lib/types";
import { ui, useUI } from "@/lib/ui";
import { cn } from "@/lib/utils";

type Action = "start" | "stop" | "restart" | "interrupt";

/** whose turn it is, in the colour of the words on a phone */
const PHONE_TONE = {
  you: "text-[color-mix(in_oklch,var(--state-waiting)_75%,var(--foreground))]",
  moving: "text-state-working",
  fault: "text-state-error",
  quiet: "",
} as const;

async function run(agent: Agent, action: Action) {
  try {
    await api.agentAction(agent.id, action);
    const done = { start: "Starting", stop: "Stopping", restart: "Restarting", interrupt: "Interrupted" }[action];
    toast(`${done} ${agent.name}`);
  } catch (e) {
    toast.error((e as Error).message);
  }
}

function AgentActions({ agent, compact }: { agent: Agent; compact?: boolean }) {
  const machine = useStore((s) => s.machine);
  const pinKey = agentPinKey(machine, agent.id);
  const pinned = useUI((s) => s.agentPins.includes(pinKey));
  const classic = useClassic();
  const { show: agentMessages, setShow: setAgentMessages } = useAgentMessagePreference();
  const [confirm, setConfirm] = useState(false);
  const [rename, setRename] = useState(false);
  const [name, setName] = useState(agent.name);
  const [saving, setSaving] = useState(false);
  async function saveAgent(change: () => Promise<Agent>) {
    setSaving(true);
    try {
      const updated = await change();
      useStore.setState((s) => ({ agents: { ...s.agents, [updated.id]: updated } }));
      return true;
    } catch (e) { toast.error((e as Error).message); return false; }
    finally { setSaving(false); }
  }
  const running = isRunning(agent.state);
  const busy = agent.state === "working" || agent.state === "waiting";
  return (
    <>
      {/* classic: Python offers "Hand to Companion" on a card, not on the agent */}
      {agent.level === "head" && !classic && !compact && (
        <Button
          variant="outline"
          size="sm"
          onClick={() => openHandOff({ agentId: agent.id })}
          aria-label="Hand to Companion"
          title="Companion, your desktop assistant, does it; its report comes back to the head"
        >
          <Sparkles />
          <span className="hidden sm:inline">Hand to Companion</span>
        </Button>
      )}
      {!running && !compact && (
        <Button variant="outline" size="sm" onClick={() => void run(agent, "start")}>
          <Play className="fill-current" />
          Start
        </Button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label="Agent actions" className="text-muted-foreground">
            <Ellipsis className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-48">
          {compact && (
            <>
              <DropdownMenuCheckboxItem checked={agentMessages} onCheckedChange={setAgentMessages}>
                Show agent messages
              </DropdownMenuCheckboxItem>
              <DropdownMenuSeparator />
            </>
          )}
          <DropdownMenuItem disabled={agent.level === "head" || running || agent.desired === "running"} onSelect={() => { setName(agent.name); setRename(true); }}>
            <Pencil /> Rename{running ? " (stop first)" : ""}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => ui.toggleAgentPin(pinKey)}>
            {pinned ? <PinOff /> : <Pin />} {pinned ? "Unpin" : "Pin"}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem disabled={!busy} onSelect={() => void run(agent, "interrupt")}>
            <OctagonX /> Interrupt turn
          </DropdownMenuItem>
          {running ? (
            <DropdownMenuItem onSelect={() => void run(agent, "stop")}>
              <Square /> Stop
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem onSelect={() => void run(agent, "start")}>
              <Play /> Start
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => void run(agent, "restart")}>
            <RotateCw /> Restart
          </DropdownMenuItem>
          {(agent.level !== "head" || compact) && !classic && (
            <DropdownMenuItem onSelect={() => openHandOff({ agentId: agent.id })}>
              <Sparkles /> Hand to Companion…
            </DropdownMenuItem>
          )}
          {agent.level !== "head" && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={() => setConfirm(true)}>
                {/* Not "Remove from list" (Python's words): here it deletes the agent with its conversation.
                    That wording waits for a real shelve on the fleet line (tmx-lead, 17 Nov). */}
                <Trash2 /> Delete agent…
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <Dialog open={rename} onOpenChange={setRename}>
        <DialogContent>
          <form onSubmit={(e) => { e.preventDefault(); if (!saving && name.trim()) void saveAgent(() => api.renameAgent(agent.id, name.trim())).then((ok) => { if (ok) setRename(false); }); }}>
            <DialogHeader><DialogTitle>Rename agent</DialogTitle><DialogDescription>The agent keeps its conversation and history.</DialogDescription></DialogHeader>
            <Input className="my-4" aria-label="Agent name" value={name} onChange={(e) => setName(e.target.value)} autoFocus disabled={saving} />
            <DialogFooter><Button type="button" variant="ghost" onClick={() => setRename(false)}>Cancel</Button><Button type="submit" disabled={saving || !name.trim() || name.trim() === agent.name}>Save</Button></DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {agent.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              The agent is stopped and removed with its conversation. Files it wrote in {displayPath(agent.cwd)} stay where they are.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={async () => {
                try {
                  await api.deleteAgent(agent.id);
                  toast(`Deleted ${agent.name}`);
                  navigate(agent.project_id ? paths.project(agent.project_id) : paths.overview());
                } catch (e) {
                  toast.error((e as Error).message);
                }
              }}
            >
              Delete agent
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function Detail({ label, children, mono, copy }: { label: string; children: React.ReactNode; mono?: boolean; copy?: string }) {
  return (
    <div className="grid grid-cols-[140px_1fr] items-start gap-4 py-2.5 text-sm sm:grid-cols-[180px_1fr]">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={"flex min-w-0 items-center gap-1 " + (mono ? "font-mono text-[0.7812rem] break-all" : "")}>
        <span className="min-w-0">{children || <span className="text-subtle-foreground">—</span>}</span>
        {copy && <CopyButton text={copy} className="-my-1" />}
      </dd>
    </div>
  );
}

/**
 * Classic: the one place that changes how a Claude or Codex agent runs (chat mode or its own
 * screen): a deliberate button, never a side effect of looking at a view (the owner #7, 17 Nov).
 * The Terminal view of a chat agent offers the same change towards its screen.
 */
function ModeSwitch({ agent }: { agent: Agent }) {
  if (!hasChatMode(agent.harness) || agent.state === "paused") return null;
  const cls = "ml-2 h-6 align-middle";
  if (agent.pending_mode) {
    return (
      <Button size="xs" variant="ghost" className={cls} onClick={() => void switchMode(agent, agent.mode, true)}>
        Cancel the switch
      </Button>
    );
  }
  if (agent.mode === "terminal") {
    return (
      <Button size="xs" variant="outline" className={cls} onClick={() => void switchMode(agent, "chat", true)}>
        Run in chat mode
      </Button>
    );
  }
  return (
    <Button size="xs" variant="outline" className={cls} disabled={isRunning(agent.state) && !canOpenScreen(agent)} onClick={() => void switchMode(agent, "terminal", true)}>
      Open {harnessLabel(agent.harness)}'s screen
    </Button>
  );
}

const PANE_BUTTON = "inline-flex h-6 items-center rounded px-2 text-[0.8125rem] outline-none transition-colors duration-150 focus-visible:ring-2 focus-visible:ring-ring";

/** Classic: a click on Chat / Terminal / Details only changes the view (and is remembered). */
function showView(agentId: string, v: string) {
  if (isAgentView(v)) rememberAgentView(agentId, v);
  navigate(paths.agent(agentId, v), { replace: true });
}

function Details({ agent }: { agent: Agent }) {
  const classic = useClassic();
  const machine = useStore((s) => s.machine);
  const agents = useStore((s) => s.agents);
  const projects = useStore((s) => s.projects);
  const parent = agent.parent_id ? agents[agent.parent_id] : undefined;
  const project = agent.project_id ? projects[agent.project_id] : undefined;
  const children = Object.values(agents).filter((a) => a.parent_id === agent.id);
  return (
    <div className="scroll-thin min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-[48.75rem] px-5 py-6 md:px-8">
        <AgentResourceDetails agent={agent} />
        {!!agent.mcp_warnings?.length && <div role="alert" className="mb-6 rounded-lg border border-state-error/30 bg-diff-del-bg px-3.5 py-2.5 text-sm"><p className="font-medium">MCP tools unavailable</p>{agent.mcp_warnings.map((warning) => <p key={warning}>{warning}</p>)}</div>}
        {agent.error && (
          <div className="mb-6 rounded-lg border border-state-error/30 bg-diff-del-bg px-3.5 py-2.5 text-sm">
            <p className="font-medium text-state-error">Last error</p>
            <p className="mt-0.5 text-foreground/90">{agent.error}</p>
          </div>
        )}
        <AgentConfig agent={agent} />
        <ProcessList agent={agent.id} />
        <h2 className="mb-1 flex h-7 items-center text-[0.8125rem] font-medium">Runtime</h2>
        <dl className="divide-y border-y">
          <Detail label="State">
            {STATE_LABEL[agent.state]}
            {agent.desired === "stopped" && agent.state !== "stopped" ? " (stopping)" : ""}
          </Detail>
          <Detail label="Level">{LEVEL_LABEL[agent.level]}</Detail>
          {machine && <Detail label="Machine">{machine}</Detail>}
          {agent.role && <Detail label="Role">{agent.role}</Detail>}
          <Detail label="Harness · mode">
            {harnessLabel(agent.harness)} · {agent.mode === "terminal" ? "its own screen (read by tmx)" : "chat"}
            {agent.agy_full_access && <span className="ml-2 text-amber-600">Owner enabled permission skipping</span>}
            {agent.home_mode ? ` · switched from ${agent.home_mode === "chat" ? "chat" : "its screen"} (a daemon restart brings it back)` : ""}
            {agent.pending_mode ? ` · switches to ${agent.pending_mode === "chat" ? "chat" : "its screen"} when this turn ends` : ""}
            {classic && <ModeSwitch agent={agent} />}
          </Detail>
          <Detail label="Project">
            {project ? (
              <a className="text-brand hover:underline" href={href(paths.project(project.id))}>
                {project.name}
              </a>
            ) : null}
          </Detail>
          <Detail label="Reports to">
            {parent ? (
              <a className="text-brand hover:underline" href={href(paths.agent(parent.id))}>
                {parent.name}
              </a>
            ) : agent.level === "head" ? (
              "You"
            ) : null}
          </Detail>
          {children.length > 0 && (
            <Detail label="Launched">
              <span className="flex flex-wrap gap-x-3 gap-y-1">
                {children.map((c) => (
                  <a key={c.id} className="text-brand hover:underline" href={href(paths.agent(c.id))}>
                    {c.name}
                  </a>
                ))}
              </span>
            </Detail>
          )}
          <Detail label="Folder" mono copy={agent.cwd}>
            {agent.cwd}
          </Detail>
          <Detail label="Turns">
            <span className="tabular">{agent.turns}</span>
          </Detail>
          <Detail label="Queued messages">
            <span className="tabular">{agent.queued}</span>
          </Detail>
          <Detail label="Last activity">
            {agent.last_activity_at ? `${relativeTime(agent.last_activity_at)} · ${absoluteTime(agent.last_activity_at)}` : ""}
          </Detail>
          <Detail label="Created">{absoluteTime(agent.created_at)}</Detail>
          <Detail label="Session" mono copy={agent.session_id || undefined}>
            {agent.session_id}
          </Detail>
          <Detail label="Process" mono>
            {agent.pid ? `pid ${agent.pid}` : ""}
          </Detail>
          <Detail label="Agent ID" mono copy={agent.id}>
            {agent.id}
          </Detail>
        </dl>
      </div>
    </div>
  );
}

export function AgentPage({ id, tab }: { id: string; tab?: string }) {
  const agent = useStore((s) => s.agents[id]);
  const profiles = useStore((s) => s.profiles);
  const project = useStore((s) => (agent?.project_id ? s.projects[agent.project_id] : undefined));
  const machine = useStore((s) => s.machine);
  const composer = useRef<ComposerHandle>(null);
  const classic = useClassic();
  const phone = useMedia(PHONE_QUERY);
  // "asks you" when the agent put a question, "needs you" for a permission or anything else
  const asks = useStore((s) => Object.values(s.approvals).some((a) => a.agent_id === id && a.state === "pending" && !a.deferred && a.kind === "question"));
  const focusTick = useUI((s) => s.focusComposer);
  useNow(30_000);

  // Tabs (DESIGN.md "Switching live"): every agent has one named after its harness with its
  // own screen. Claude Code and Codex: Conversation, the harness, Details (their screen opens on
  // the same conversation); the others only have their screen: the harness, Log, Details.
  const screen = agent?.mode === "terminal";
  const chatCapable = hasChatMode(agent?.harness);
  // Classic, on a wide screen (parity B1; lib/agent-view.ts): Chat and Terminal side by side,
  // Python's "Split workspace"; "Focus view" is one at a time, the tabs.
  const wide = useMedia(`(min-width: ${SPLIT_MIN_WIDTH}px)`);
  const panes = useUI((s) => s.agentPanes);
  const split = splitApplies({ classic, wide, layout: panes, chatCapable, harness: agent?.harness });
  const home = screen ? "terminal" : "conversation"; // the tab without a name in the URL
  // Classic (the owner #7, 17 Nov; lib/agent-view.ts): Python's toggle, Chat · Terminal · Details. A
  // click only changes the view, whatever mode the agent runs in: the view is the one in the
  // address, else the one last chosen for this agent on this device, else Chat.
  const current = classic
    ? resolveAgentView(tab, rememberedAgentView(id), chatCapable)
    : tab === "details" || tab === "terminal" || tab === "conversation"
      ? tab
      : home;

  useEffect(() => {
    if (!split) return;
    const visible = visiblePaneView(panes, current);
    if (visible !== current) showView(id, visible);
  }, [split, panes, current, id]);

  useEffect(() => {
    if (focusTick) composer.current?.focus();
  }, [focusTick]);

  if (!agent) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <EmptyState icon={Bot} title="This agent isn't here" className="w-full max-w-md border-none bg-transparent">
          It may have been deleted, or the link points to another machine.
          <div className="mt-4">
            <Button variant="outline" size="sm" asChild>
              <a href={href(paths.overview())}>Back to overview</a>
            </Button>
          </div>
        </EmptyState>
      </div>
    );
  }

  const phoneLine = agentHeaderLine(agent, asks, project?.name, machine);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader actions={<AgentActions agent={agent} compact={phone} />} className="border-b-0" compactPhone={phone}>
        <AgentAvatar agent={agent} />
        {project && !classic && (
          <>
            <a href={href(paths.project(project.id))} className="hidden shrink-0 truncate text-[0.8125rem] text-muted-foreground hover:text-foreground lg:block">
              {project.name}
            </a>
            <ChevronRight className="hidden size-3.5 shrink-0 text-subtle-foreground lg:block" />
          </>
        )}
        {phone ? (
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <h1 className="min-w-0 truncate text-[15px] leading-tight font-semibold tracking-tight">{agentLabel(agent, profiles)}</h1>
            <p className="flex min-w-0 items-center gap-1 text-[12px] leading-tight text-muted-foreground" data-phone-agent-line>
              {!agent.shelved_at && <StateDot state={agent.state} />}
              <span className={cn("truncate font-medium", PHONE_TONE[stateTone(agent)])}>{phoneLine.state}</span>
            </p>
          </div>
        ) : (
          <h1 className="min-w-[4rem] truncate text-[0.875rem] font-semibold tracking-tight">{agentLabel(agent, profiles)}</h1>
        )}
        {/* Classic (the owner #3/#9/#14, the calm top): Python's one row: face, name, state, then one
            line "project · machine · role" and the harness tag. No level, profile or model badges. */}
        {!classic && !phone && <LevelBadge level={agent.level} />}
        {!phone && !classic && isCustomProfile(agent) && profileOf(agent, profiles) && <ProfileBadge profile={profileOf(agent, profiles)!} className="hidden sm:inline-flex" />}
        {!phone && !classic && agent.harness !== "shell" && <HarnessBadge harness={agent.harness} mode={agent.mode} className="hidden sm:inline-flex" />}
        {!phone && !classic && <ModelBadge agent={agent} className="hidden sm:inline-flex" />}
        {!phone && <AgentStateLabel agent={agent} className="ml-1 hidden sm:inline-flex" />}
        {classic && (
          <span className="ml-1 hidden min-w-0 items-center gap-1.5 truncate text-xs text-muted-foreground md:inline-flex" data-agent-line>
            {agentLine(agent, project?.name, machine).map((part, i) => (
              <span key={i} className={cn("truncate", i > 0 && "before:mr-1.5 before:text-subtle-foreground before:content-['·']")}>
                {i === 0 && project ? (
                  <a href={href(paths.project(project.id))} className="hover:text-foreground hover:underline">
                    {part}
                  </a>
                ) : (
                  part
                )}
              </span>
            ))}
          </span>
        )}
        {!phone && classic && agent.harness !== "shell" && <HarnessBadge harness={agent.harness} className="hidden sm:inline-flex" />}
      </PageHeader>
      {/* the tabs scroll sideways when a phone at a large text size can't fit them */}
      <div className="scroll-thin flex h-8 shrink-0 items-end gap-3 overflow-x-auto overflow-y-hidden border-b px-3 md:h-9 md:gap-4 md:px-6">
        {split && (
          // side by side: each button shows or hides its pane, as in Python; Details is still a view
          <div className="flex h-9 items-center gap-1" role="group" aria-label="Panes" data-agent-panes>
            {(["chat", "terminal"] as AgentPane[]).map((p) => {
              const on = current !== "details" && panes[p];
              return (
                <button
                  key={p}
                  type="button"
                  aria-pressed={on}
                  onClick={() => {
                    if (current === "details") {
                      ui.setAgentPanes(showPane(panes, p));
                      navigate(paths.agent(agent.id), { replace: true });
                    } else ui.setAgentPanes(togglePane(panes, p));
                  }}
                  className={cn(PANE_BUTTON, on ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
                >
                  {p === "chat" ? "Chat" : "Terminal"}
                </button>
              );
            })}
            <button
              type="button"
              aria-pressed={current === "details"}
              onClick={() => navigate(paths.agent(agent.id, current === "details" ? undefined : "details"), { replace: true })}
              className={cn(PANE_BUTTON, current === "details" ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
            >
              Details
            </button>
          </div>
        )}
        {!split && <Tabs
          value={current}
          onValueChange={(v) => {
            if (classic) return showView(agent.id, v);
            navigate(paths.agent(agent.id, v === home ? undefined : v), { replace: true });
            // Clicking the harness tab of an agent in chat opens its screen (mid-turn: when the turn ends).
            if (v === "terminal" && canOpenScreen(agent)) void switchMode(agent, "terminal");
          }}
        >
          <TabsList variant="line" className="h-8 gap-3 p-0 md:h-9 md:gap-4">
            {chatCapable && <TabsTrigger value="conversation" className="h-8 flex-none px-0 text-[0.8125rem] md:h-9">{classic || phone ? "Chat" : "Conversation"}</TabsTrigger>}
            <TabsTrigger value="terminal" className="h-8 flex-none gap-1.5 px-0 text-[0.8125rem] md:h-9" title={`${harnessLabel(agent.harness)}'s own screen`}>
              {classic || phone ? "Terminal" : harnessLabel(agent.harness)}
              {!classic && chatCapable && screen && (
                <>
                  <span className="size-1.5 rounded-full bg-state-working" aria-hidden />
                  <span className="sr-only">(open)</span>
                </>
              )}
            </TabsTrigger>
            {!chatCapable && <TabsTrigger value="conversation" className="h-8 flex-none px-0 text-[0.8125rem] md:h-9">Log</TabsTrigger>}
            <TabsTrigger value="details" className="h-8 flex-none px-0 text-[0.8125rem] md:h-9">Details</TabsTrigger>
          </TabsList>
        </Tabs>}
        {classic && wide && chatCapable && agent.harness !== "shell" && (
          <button
            type="button"
            onClick={() => ui.setAgentPanes({ ...panes, split: !panes.split })}
            className={cn(PANE_BUTTON, "mb-1.5 self-end border text-muted-foreground hover:text-foreground")}
            data-split-toggle
          >
            {panes.split ? "Focus view" : "Split workspace"}
          </button>
        )}
        {/* mail delivery receipts: a badge beside the tabs, details on a click */}
        {!classic && <MailReceipts agentId={agent.id} />}
        <div className="ml-auto hidden h-9 min-w-0 items-center gap-3 text-xs text-muted-foreground md:flex">
          <Truncate className="max-w-[22.5rem] font-mono text-[0.7188rem]" title={agent.cwd}>
            {displayPath(agent.cwd)}
          </Truncate>
          {!classic && <AgentUsageInline agent={agent} />}
        </div>
      </div>
      <AgentResourceBanner agent={agent} />
      {split && current !== "details" && (
        <>
          <div className={cn("flex min-h-0 flex-1", agent.mode === "chat" && panes.chat && "flex-col")} data-agent-split>
            {panes.terminal && panes.chat && agent.mode === "chat" && <AgentTerminal agent={agent} autoFocus={!split} />}
            {panes.chat && (
              <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-pane="chat" onPointerDownCapture={() => current !== "conversation" && showView(agent.id, "conversation")} onFocusCapture={() => current !== "conversation" && showView(agent.id, "conversation")}>
                <Conversation agent={agent} composerRef={composer} hideBox />
              </div>
            )}
            {panes.terminal && !(panes.chat && agent.mode === "chat") && (
              <div className={cn("flex min-w-0 flex-1 flex-col", panes.chat && "border-l")} data-pane="terminal" onPointerDownCapture={() => current !== "terminal" && showView(agent.id, "terminal")} onFocusCapture={() => current !== "terminal" && showView(agent.id, "terminal")}>
                <AgentTerminal agent={agent} autoFocus={!split} />
                <TerminalKeys agent={agent} />
              </div>
            )}
          </div>
          {/* one message box, full width, under both panes */}
          <div className="shrink-0 border-t bg-background px-3 py-3 md:px-4" onFocusCapture={() => current !== "conversation" && showView(agent.id, "conversation")}>
            <MessageBox agent={agent} composerRef={composer} />
          </div>
        </>
      )}
      {current === "conversation" && !split && <Conversation agent={agent} composerRef={composer} />}
      {current === "terminal" && !classic && <HarnessScreen agent={agent} />}
      {!split && current === "terminal" && classic && (
        <>
          <AgentTerminal agent={agent} />
          {/* Python's key row: Enter, Esc, Ctrl+C, arrows, Tab, 1 2 3, y, n (only with a live screen) */}
          <TerminalKeys agent={agent} />
          {/* the message box is under Terminal too; a shell is typed into directly */}
          {agent.harness !== "shell" && (
            <div className="shrink-0 bg-background px-3 pb-3 md:px-4 md:pb-4">
              <MessageBox agent={agent} composerRef={composer} bare />
            </div>
          )}
        </>
      )}
      {current === "details" && <Details agent={agent} />}
    </div>
  );
}

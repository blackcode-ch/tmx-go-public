import { useEffect, useRef, useState } from "react";
import { ChevronRight, Ellipsis, Play, RotateCw, Square, Trash2, OctagonX, Bot, Sparkles } from "lucide-react";
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
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { AgentAvatar } from "@/components/agent-avatar";
import { AgentConfig } from "@/components/agent-config";
import { ProcessList } from "@/components/process-list";
import { openHandOff } from "@/components/companion/hand-off-dialog";
import { HarnessBadge, LevelBadge, ModelBadge, ProfileBadge } from "@/components/badges";
import { canOpenScreen, HarnessScreen, switchMode } from "@/components/harness-screen";
import { Conversation } from "@/components/conversation/conversation";
import { MailReceipts } from "@/components/mail-receipts";
import type { ComposerHandle } from "@/components/conversation/composer";
import { CopyButton } from "@/components/copy-button";
import { EmptyState, PageHeader } from "@/components/page";
import { AgentResourceBanner, AgentResourceDetails, AgentUsageInline } from "@/components/resources/agent-resources";
import { AgentStateLabel } from "@/components/state";
import { Truncate } from "@/components/truncate";
import { useNow } from "@/hooks/use-now";
import { api } from "@/lib/api";
import { absoluteTime, displayPath, isRunning, LEVEL_LABEL, relativeTime, STATE_LABEL } from "@/lib/format";
import { harnessLabel, hasChatMode } from "@/lib/harness";
import { agentLabel, isCustomProfile, profileOf } from "@/lib/profiles";
import { href, navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Agent } from "@/lib/types";
import { useUI } from "@/lib/ui";

type Action = "start" | "stop" | "restart" | "interrupt";

async function run(agent: Agent, action: Action) {
  try {
    await api.agentAction(agent.id, action);
    const done = { start: "Starting", stop: "Stopping", restart: "Restarting", interrupt: "Interrupted" }[action];
    toast(`${done} ${agent.name}`);
  } catch (e) {
    toast.error((e as Error).message);
  }
}

function AgentActions({ agent }: { agent: Agent }) {
  const [confirm, setConfirm] = useState(false);
  const running = isRunning(agent.state);
  const busy = agent.state === "working" || agent.state === "waiting";
  return (
    <>
      {agent.level === "head" && (
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
      {!running && (
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
          {agent.level !== "head" && (
            <DropdownMenuItem onSelect={() => openHandOff({ agentId: agent.id })}>
              <Sparkles /> Hand to Companion…
            </DropdownMenuItem>
          )}
          {agent.level !== "head" && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={() => setConfirm(true)}>
                <Trash2 /> Delete agent…
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
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

function Details({ agent }: { agent: Agent }) {
  const agents = useStore((s) => s.agents);
  const projects = useStore((s) => s.projects);
  const parent = agent.parent_id ? agents[agent.parent_id] : undefined;
  const project = agent.project_id ? projects[agent.project_id] : undefined;
  const children = Object.values(agents).filter((a) => a.parent_id === agent.id);
  return (
    <div className="scroll-thin min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-[48.75rem] px-5 py-6 md:px-8">
        <AgentResourceDetails agent={agent} />
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
          <Detail label="Harness · mode">
            {harnessLabel(agent.harness)} · {agent.mode === "terminal" ? "its own screen (read by tmx)" : "chat"}
            {agent.agy_full_access && <span className="ml-2 text-amber-600">Owner enabled permission skipping</span>}
            {agent.home_mode ? ` · switched from ${agent.home_mode === "chat" ? "chat" : "its screen"} (a daemon restart brings it back)` : ""}
            {agent.pending_mode ? ` · switches to ${agent.pending_mode === "chat" ? "chat" : "its screen"} when this turn ends` : ""}
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
  const composer = useRef<ComposerHandle>(null);
  const focusTick = useUI((s) => s.focusComposer);
  useNow(30_000);

  // Tabs (DESIGN.md "Switching live"): every agent has one named after its harness with its
  // own screen. Claude Code and Codex: Conversation, the harness, Details (their screen opens on
  // the same conversation); the others only have their screen: the harness, Log, Details.
  const screen = agent?.mode === "terminal";
  const chatCapable = hasChatMode(agent?.harness);
  const home = screen ? "terminal" : "conversation"; // the tab without a name in the URL
  const current = tab === "details" || tab === "terminal" || tab === "conversation" ? tab : home;

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

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader actions={<AgentActions agent={agent} />} className="border-b-0">
        <AgentAvatar agent={agent} />
        {project && (
          <>
            <a href={href(paths.project(project.id))} className="hidden shrink-0 truncate text-[0.8125rem] text-muted-foreground hover:text-foreground lg:block">
              {project.name}
            </a>
            <ChevronRight className="hidden size-3.5 shrink-0 text-subtle-foreground lg:block" />
          </>
        )}
        <h1 className="min-w-[4rem] truncate text-[0.875rem] font-semibold tracking-tight">{agentLabel(agent, profiles)}</h1>
        <LevelBadge level={agent.level} />
        {isCustomProfile(agent) && profileOf(agent, profiles) && <ProfileBadge profile={profileOf(agent, profiles)!} className="hidden sm:inline-flex" />}
        {agent.harness !== "shell" && <HarnessBadge harness={agent.harness} mode={agent.mode} className="hidden sm:inline-flex" />}
        <ModelBadge agent={agent} className="hidden sm:inline-flex" />
        <AgentStateLabel agent={agent} className="ml-1 hidden sm:inline-flex" />
      </PageHeader>
      {/* the tabs scroll sideways when a phone at a large text size can't fit them */}
      <div className="scroll-thin flex h-9 shrink-0 items-end gap-4 overflow-x-auto overflow-y-hidden border-b px-5 md:px-6">
        <Tabs
          value={current}
          onValueChange={(v) => {
            navigate(paths.agent(agent.id, v === home ? undefined : v), { replace: true });
            // Clicking the harness tab of an agent in chat opens its screen (mid-turn: when the turn ends).
            if (v === "terminal" && canOpenScreen(agent)) void switchMode(agent, "terminal");
          }}
        >
          <TabsList variant="line" className="h-9 gap-4 p-0">
            {chatCapable && <TabsTrigger value="conversation" className="h-9 flex-none px-0 text-[0.8125rem]">Conversation</TabsTrigger>}
            <TabsTrigger value="terminal" className="h-9 flex-none gap-1.5 px-0 text-[0.8125rem]" title={`${harnessLabel(agent.harness)}'s own screen`}>
              {harnessLabel(agent.harness)}
              {chatCapable && screen && (
                <>
                  <span className="size-1.5 rounded-full bg-state-working" aria-hidden />
                  <span className="sr-only">(open)</span>
                </>
              )}
            </TabsTrigger>
            {!chatCapable && <TabsTrigger value="conversation" className="h-9 flex-none px-0 text-[0.8125rem]">Log</TabsTrigger>}
            <TabsTrigger value="details" className="h-9 flex-none px-0 text-[0.8125rem]">Details</TabsTrigger>
          </TabsList>
        </Tabs>
        <div className="ml-auto hidden h-9 min-w-0 items-center gap-3 text-xs text-muted-foreground md:flex">
          <Truncate className="max-w-[22.5rem] font-mono text-[0.7188rem]" title={agent.cwd}>
            {displayPath(agent.cwd)}
          </Truncate>
          <AgentUsageInline agent={agent} />
        </div>
      </div>
      <AgentResourceBanner agent={agent} />
      <MailReceipts agentId={agent.id} />
      {current === "conversation" && <Conversation agent={agent} composerRef={composer} />}
      {current === "terminal" && <HarnessScreen agent={agent} />}
      {current === "details" && <Details agent={agent} />}
    </div>
  );
}

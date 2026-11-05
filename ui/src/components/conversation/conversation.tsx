import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type Ref } from "react";
import { ArrowDown, MessageSquareDashed, MessagesSquare, Play, SquareTerminal } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api";
import { ensureFullTimeline, loadEarlier, pendingApprovals, useStore } from "@/lib/store";
import { isRunning, STATE_LABEL } from "@/lib/format";
import { harnessLabel, hasChatMode, inOwnScreen } from "@/lib/harness";
import { navigate, paths } from "@/lib/router";
import type { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";
import { InlineApproval } from "../approval";
import { switchMode } from "../harness-screen";
import { buildItems, type Item } from "./build";
import { Composer, type ComposerHandle } from "./composer";
import { AssistantText, ErrorRow, MailChip, ResultDivider, SystemLine, Thinking, TimeBreak, ToolGroup, UserBubble } from "./items";

const PAGE = 120;

export function ItemView({ item, cwd }: { item: Item; cwd: string }) {
  switch (item.type) {
    case "user":
      return <UserBubble e={item.e} />;
    case "assistant":
      return <AssistantText e={item.e} />;
    case "thinking":
      return <Thinking e={item.e} />;
    case "tools":
      return <ToolGroup tools={item.tools} cwd={cwd} />;
    case "mail":
      return <MailChip e={item.e} />;
    case "result":
      return <ResultDivider e={item.e} />;
    case "system":
      return <SystemLine e={item.e} />;
    case "error":
      return <ErrorRow e={item.e} />;
    case "time":
      return <TimeBreak at={item.at} />;
  }
}

export function spacing(prev: Item | undefined, cur: Item): string {
  if (!prev) return "";
  if (cur.type === "tools" && (prev.type === "assistant" || prev.type === "thinking")) return "mt-2";
  if (prev.type === "tools" && cur.type === "thinking") return "mt-2";
  if (cur.type === "thinking" && prev.type === "assistant") return "mt-2";
  if (cur.type === "result" || prev.type === "result") return "mt-5";
  if (cur.type === "user" || prev.type === "user") return "mt-5";
  return "mt-3.5";
}

/** Above the composer while a Claude or Codex agent is in its own screen, or about to switch
 * (DESIGN.md "Switching live"): the conversation goes on there, tmx shows what it reads. */
function ScreenNotice({ agent }: { agent: Agent }) {
  if (!hasChatMode(agent.harness)) return null;
  const label = harnessLabel(agent.harness);
  const screen = inOwnScreen(agent);
  if (!screen && agent.pending_mode !== "terminal") return null;
  let text = `${agent.name} is in ${label}'s own screen. What it does there shows up here; messages are typed in when it is ready.`;
  if (agent.pending_mode === "terminal") text = `${agent.name} opens ${label}'s screen when this turn ends.`;
  if (agent.pending_mode === "chat") text = `${agent.name} comes back to chat when this turn ends.`;
  return (
    <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-lg border bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground" role="status">
      <SquareTerminal className="size-3.5 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1">{text}</span>
      <span className="flex shrink-0 gap-1.5">
        {agent.pending_mode === "terminal" ? (
          <Button size="xs" variant="ghost" onClick={() => void switchMode(agent, "chat")}>
            Don't switch
          </Button>
        ) : (
          <>
            <Button size="xs" variant="outline" onClick={() => navigate(paths.agent(agent.id))}>
              <SquareTerminal /> Open screen
            </Button>
            {agent.pending_mode !== "chat" && (
              <Button size="xs" variant="outline" onClick={() => void switchMode(agent, "chat")}>
                <MessagesSquare /> Back to chat
              </Button>
            )}
          </>
        )}
      </span>
    </div>
  );
}

function WorkingLine({ agent }: { agent: Agent }) {
  if (agent.state !== "working" && agent.state !== "starting") return null;
  return (
    <div className="mt-3 flex items-center gap-2 text-xs text-muted-foreground" aria-live="polite">
      <span className="flex gap-1">
        {[0, 1, 2].map((i) => (
          <span key={i} className="size-1 animate-pulse-dot rounded-full bg-state-working" style={{ animationDelay: `${i * 160}ms` }} />
        ))}
      </span>
      {agent.state === "starting" ? "Starting…" : `${agent.name} is working`}
    </div>
  );
}

export function Conversation({ agent, composerRef }: { agent: Agent; composerRef?: Ref<ComposerHandle> }) {
  const tl = useStore((s) => s.timelines[agent.id]);
  const approvals = useStore((s) => s.approvals);
  const mine = useMemo(() => pendingApprovals(approvals).filter((a) => a.agent_id === agent.id && a.kind !== "brake").reverse(), [approvals, agent.id]);
  const entries = useMemo(() => tl?.entries ?? [], [tl?.entries]);
  const items = useMemo(() => buildItems(entries), [entries]);
  const [shown, setShown] = useState(PAGE);
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [atBottom, setAtBottom] = useState(true);
  const [unseen, setUnseen] = useState(false);
  const lastSeq = entries.length ? entries[entries.length - 1].seq : 0;

  useEffect(() => {
    ensureFullTimeline(agent.id);
    stick.current = true;
    setShown(PAGE);
    setAtBottom(true);
    setUnseen(false);
  }, [agent.id]);

  const toBottom = useCallback((smooth = false) => {
    const el = scroller.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  }, []);

  // New entries: follow when stuck to the bottom, otherwise flag them.
  useLayoutEffect(() => {
    if (stick.current) toBottom();
    else setUnseen(true);
  }, [lastSeq, mine.length, toBottom]);

  // Content that grows later (highlighting, expanding rows) keeps the bottom pinned.
  useEffect(() => {
    const el = content.current;
    if (!el) return;
    const ro = new ResizeObserver(() => stick.current && toBottom());
    ro.observe(el);
    return () => ro.disconnect();
  }, [toBottom]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    stick.current = bottom;
    if (bottom !== atBottom) setAtBottom(bottom);
    if (bottom) setUnseen(false);
  };

  const visible = items.length > shown ? items.slice(items.length - shown) : items;
  const hiddenLocal = items.length - visible.length;
  const loading = !tl?.full && (tl?.loading || !tl);
  const stopped = !isRunning(agent.state);

  async function startAgent() {
    try {
      await api.agentAction(agent.id, "start");
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div ref={scroller} onScroll={onScroll} className="scroll-thin min-h-0 flex-1 overflow-y-auto" data-testid="conversation-scroll">
        <div ref={content} className="mx-auto w-full max-w-[48.75rem] px-5 pt-6 pb-6 md:px-6">
          {(hiddenLocal > 0 || tl?.maybeMore) && (
            <div className="mb-4 flex justify-center">
              <Button
                variant="ghost"
                size="xs"
                className="text-muted-foreground"
                onClick={() => {
                  stick.current = false;
                  if (hiddenLocal > 0) setShown((n) => n + PAGE);
                  else loadEarlier(agent.id);
                }}
              >
                {tl?.loading ? "Loading…" : hiddenLocal > 0 ? `Show ${Math.min(hiddenLocal, PAGE)} earlier` : "Load earlier"}
              </Button>
            </div>
          )}

          {loading && entries.length === 0 ? (
            <div className="space-y-5" aria-busy="true">
              <Skeleton className="ml-auto h-9 w-2/3 rounded-2xl" />
              <div className="space-y-2">
                <Skeleton className="h-3.5 w-11/12" />
                <Skeleton className="h-3.5 w-4/5" />
                <Skeleton className="h-3.5 w-3/5" />
              </div>
              <div className="space-y-1.5">
                <Skeleton className="h-6 w-1/2" />
                <Skeleton className="h-6 w-2/5" />
              </div>
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-col items-center py-20 text-center">
              <MessageSquareDashed className="mb-3 size-6 text-subtle-foreground" />
              <p className="text-sm font-medium">No conversation yet</p>
              <p className="mt-1 max-w-xs text-xs text-muted-foreground">
                {agent.mode === "terminal" && !hasChatMode(agent.harness)
                  ? `This agent runs in ${harnessLabel(agent.harness)}'s own screen. Open the ${harnessLabel(agent.harness)} tab to see it.`
                  : `Messages, tool calls and results from ${agent.name} show up here.`}
              </p>
            </div>
          ) : (
            visible.map((it, i) => (
              <div key={it.key} className={spacing(visible[i - 1], it)}>
                <ItemView item={it} cwd={agent.cwd} />
              </div>
            ))
          )}

          <WorkingLine agent={agent} />

          {mine.length > 0 && (
            <div className="mt-5 space-y-3">
              {mine.map((a) => (
                <InlineApproval key={a.id} a={a} agent={agent} />
              ))}
            </div>
          )}
        </div>
      </div>

      <div className="relative shrink-0 bg-background px-3 pb-3 md:px-4 md:pb-4">
      {!atBottom && items.length > 0 && (
        <div className="pointer-events-none absolute inset-x-0 -top-10 flex justify-center">
          <Button
            variant="outline"
            size="xs"
            onClick={() => {
              stick.current = true;
              setUnseen(false);
              toBottom(true);
            }}
            className="pointer-events-auto h-7 rounded-full bg-popover px-3 shadow-sm"
          >
            <ArrowDown />
            {unseen ? "New activity" : "Jump to latest"}
          </Button>
        </div>
      )}

        <div className="mx-auto w-full max-w-[47.25rem]">
          {stopped && agent.mode === "chat" && (
            <div className={cn("mb-2 flex items-center gap-2 rounded-lg border bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground")}>
              <span className="min-w-0 flex-1 truncate">
                {agent.name} is {STATE_LABEL[agent.state].toLowerCase()}. Messages wait until it runs again.
              </span>
              <Button size="xs" variant="outline" onClick={() => void startAgent()}>
                <Play className="fill-current" />
                Start
              </Button>
            </div>
          )}
          <ScreenNotice agent={agent} />
          <Composer
            ref={composerRef}
            agent={agent}
            placeholder={agent.harness === "shell" ? `Type into ${agent.name}'s terminal from the Shell tab` : undefined}
          />
        </div>
      </div>
    </div>
  );
}

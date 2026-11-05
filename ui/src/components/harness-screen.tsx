// The harness tab of an agent's window (DESIGN.md "Switching live"): its harness's own screen
// (Claude Code, Codex, opencode, pi, a shell) live in xterm. A Claude or Codex agent in chat
// mode opens its screen on the same conversation, and goes back to chat from there; a switch
// asked for during a turn happens when the turn ends.
import { lazy, Suspense, useState } from "react";
import { Loader2, MessagesSquare, Play, SquareTerminal } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/page";
import { api } from "@/lib/api";
import { isRunning, STATE_LABEL } from "@/lib/format";
import { harnessLabel, hasChatMode } from "@/lib/harness";
import { navigate, paths } from "@/lib/router";
import { agentTerminalPath } from "@/lib/transport";
import type { Agent, Mode } from "@/lib/types";
import { cn } from "@/lib/utils";

const TerminalView = lazy(() => import("@/components/terminal-view").then((m) => ({ default: m.TerminalView })));

const EXPLAINED = "tmx.screen-explained";

function explained(): boolean {
  try {
    return localStorage.getItem(EXPLAINED) === "1";
  } catch {
    return false;
  }
}

/** Switches the agent's mode (a switch that waits for the turn shows as pending_mode on the agent). */
export async function switchMode(agent: Agent, mode: Mode): Promise<boolean> {
  try {
    await api.setMode(agent.id, mode);
    return true;
  } catch (e) {
    toast.error((e as Error).message);
    return false;
  }
}

/** The agent can switch to its screen now (it runs; not queued or paused). */
export function canOpenScreen(a: Agent): boolean {
  return hasChatMode(a.harness) && a.mode === "chat" && !a.pending_mode && isRunning(a.state) && a.state !== "queued" && a.state !== "paused";
}

function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn(className, "animate-spin")} />;
}

function busyText(a: Agent): string {
  if (a.state === "starting") return `${a.name} is starting`;
  if (a.state === "waiting") return `${a.name} is waiting for your answer`;
  return `${a.name} is in the middle of a turn`;
}

async function backToChat(agent: Agent) {
  if (!(await switchMode(agent, "chat"))) return;
  navigate(paths.agent(agent.id), { replace: true });
}

async function startIn(agent: Agent, mode: Mode) {
  if (agent.mode !== mode && !(await switchMode(agent, mode))) return;
  try {
    await api.agentAction(agent.id, "start");
  } catch (e) {
    toast.error((e as Error).message);
  }
}

function Centered({ children }: { children: React.ReactNode }) {
  return <div className="scroll-thin flex min-h-0 flex-1 items-start justify-center overflow-y-auto p-5 md:items-center md:p-8">{children}</div>;
}

function Opening({ label }: { label: string }) {
  return (
    <Centered>
      <div className="flex items-center gap-2 text-sm text-muted-foreground" aria-live="polite">
        <Loader2 className="size-4 animate-spin" /> Opening {label}'s screen…
      </div>
    </Centered>
  );
}

/** The bar above a switched agent's screen: where it is, and the way back. */
function ScreenBar({ agent, label }: { agent: Agent; label: string }) {
  const back = agent.pending_mode === "chat";
  return (
    <div className="flex min-h-10 shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b px-5 py-1.5 text-xs text-muted-foreground md:px-6">
      <SquareTerminal className="size-3.5 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1 truncate">
        {back ? `Back to chat when this turn ends` : `${label}'s own screen · same conversation`}
      </span>
      {back ? (
        <Button size="xs" variant="ghost" onClick={() => void switchMode(agent, "terminal")}>
          Stay here
        </Button>
      ) : (
        <Button size="xs" variant="outline" onClick={() => void backToChat(agent)}>
          <MessagesSquare /> Back to chat
        </Button>
      )}
    </div>
  );
}

export function HarnessScreen({ agent }: { agent: Agent }) {
  const label = harnessLabel(agent.harness);
  const chat = hasChatMode(agent.harness);
  const [seen, setSeen] = useState(explained);
  const [focus, setFocus] = useState(0);
  const running = isRunning(agent.state);

  if (agent.mode === "terminal") {
    if (running && agent.pid > 0) {
      return (
        <div className="flex min-h-0 flex-1 flex-col">
          {chat && <ScreenBar agent={agent} label={label} />}
          {chat && !seen && (
            <div className="mx-3 mt-3 flex items-start gap-3 rounded-lg border bg-muted/40 px-3 py-2 text-xs md:mx-4" role="note">
              <span className="min-w-0 flex-1 leading-5">
                This is {label}'s own screen for this agent. tmx keeps reading it; Back to chat returns to the tmx view.
              </span>
              <Button
                size="xs"
                variant="ghost"
                className="-my-0.5"
                onClick={() => {
                  try {
                    localStorage.setItem(EXPLAINED, "1");
                  } catch {
                    /* private mode: it shows again next time */
                  }
                  setSeen(true);
                  setFocus((n) => n + 1);
                }}
              >
                Got it
              </Button>
            </div>
          )}
          <Suspense fallback={<div className="flex-1" />}>
            <TerminalView key={agent.pid} path={agentTerminalPath(agent.id)} focusTick={focus} />
          </Suspense>
        </div>
      );
    }
    if (agent.state === "starting") return <Opening label={label} />;
    return (
      <Centered>
        <EmptyState
          icon={SquareTerminal}
          title={`${label}'s screen is closed`}
          className="w-full max-w-md"
          actions={
            <>
              {agent.state !== "queued" && (
                <Button size="sm" onClick={() => void startIn(agent, "terminal")}>
                  <Play className="fill-current" /> Start
                </Button>
              )}
              {chat && (
                <Button size="sm" variant="outline" onClick={() => void backToChat(agent)}>
                  <MessagesSquare /> Back to chat
                </Button>
              )}
            </>
          }
        >
          {agent.name} is {STATE_LABEL[agent.state].toLowerCase()}.{agent.error ? ` ${agent.error}` : ""}
        </EmptyState>
      </Centered>
    );
  }

  // Chat mode (Claude Code, Codex): its screen opens on the same conversation.
  if (agent.pending_mode === "terminal") {
    if (agent.state === "idle") return <Opening label={label} />;
    return (
      <Centered>
        <EmptyState
          icon={Spinner}
          title={`${label}'s screen opens when this turn ends`}
          className="w-full max-w-md"
          actions={
            <Button size="sm" variant="outline" onClick={() => void switchMode(agent, "chat")}>
              Don't switch
            </Button>
          }
        >
          {busyText(agent)}. tmx never cuts a turn short; mail waits for the switch.
        </EmptyState>
      </Centered>
    );
  }
  return (
    <Centered>
      <EmptyState
        icon={SquareTerminal}
        title={`${label}'s own screen`}
        className="w-full max-w-md"
        actions={
          canOpenScreen(agent) ? (
            <Button size="sm" onClick={() => void switchMode(agent, "terminal")}>
              <SquareTerminal /> Open {label}
            </Button>
          ) : agent.state === "queued" || agent.state === "paused" ? null : (
            <Button size="sm" onClick={() => void startIn(agent, "terminal")}>
              <Play className="fill-current" /> Start in {label}
            </Button>
          )
        }
      >
        {label}'s own screen for {agent.name}, on the same conversation. tmx keeps reading it; Back to chat returns to the tmx view.
        {!running && ` ${agent.name} is ${STATE_LABEL[agent.state].toLowerCase()}.`}
      </EmptyState>
    </Centered>
  );
}

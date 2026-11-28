import { memo, useState, useEffect } from "react";
import { api } from "@/lib/api";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { TimelinePin } from "@/components/message-pins";
import { AlertTriangle, ArrowRight, Brain, ChevronRight, CornerDownRight, ListChecks, Loader2, Mail } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { absoluteTime, clockTime, duration, firstLine, modelName, plural, relativeTime } from "@/lib/format";
import { ownerDeliveryLabel } from "@/lib/owner-delivery";
import { turnResult } from "@/lib/turn-result";
import { useStore } from "@/lib/store";
import { parseNumberedLines, summarizeTool, asInput } from "@/lib/tools";
import type { TimelineEntry } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Markdown } from "../markdown";
import { diffStats } from "../diff-view";
import { ToolInputView } from "./tool-input";
import { ownerDisplayEntry, isOutgoing, toolRunSummary, runningTool, toolElapsed, trafficLabel, type ToolPair } from "./build";
import { useClassic } from "@/hooks/use-classic";
import { CopyButton } from "../copy-button";
import { GuardTimelineRow, guardMeta } from "../guard";
import { splitAttachments } from "@/lib/attachments";
import { AttachmentList } from "./attachments";

function TimeHint({ at, children }: { at: number; children: React.ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent side="left">{absoluteTime(at)}</TooltipContent>
    </Tooltip>
  );
}

export const UserBubble = memo(function UserBubble({ e }: { e: TimelineEntry }) {
  const message = useStore((s) => s.messages.find((m) => m.id === e.meta?.message_id));
  const { ownerPreview } = ownerDisplayEntry(e);
  const { text, files } = splitAttachments(ownerPreview ?? e.text);
  // Classic (the owner, 17 Nov 19:26): his own messages are clearly his: to the right, in a tinted
  // bubble with a hairline in the brand colour and a light shadow.
  const classic = useClassic();
  return (
    <div className="flex flex-col items-end gap-1.5 pl-[12%]" data-chat={classic ? "mine" : undefined}>
      <TimelinePin e={e} />
      {files.length > 0 && <AttachmentList files={files} />}
      {text && (
        <TimeHint at={e.at}>
          <div
            className={cn(
              "max-w-full rounded-2xl rounded-br-md border border-brand/40 bg-brand/15 px-3.5 py-2 text-sm leading-relaxed whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]",
              classic && "border border-brand/25 shadow-[0_1px_2px_rgb(0_0_0/0.06)]",
            )}
          >
            {text}
          </div>
        </TimeHint>
      )}
      {message && <span className={cn("text-xs", message.receipt === "stalled" ? "text-destructive" : "text-muted-foreground")} role={message.receipt === "stalled" ? "alert" : "status"}>
        {ownerDeliveryLabel(message, clockTime)}
      </span>}
      {message?.receipt === "stalled" && <CopyButton text={message.body} label="Copy message to retry" />}
      {ownerPreview !== undefined && (
        <details className="max-w-full rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
          <summary className="cursor-pointer">monitor data (hidden) - show original</summary>
          <pre className="mt-2 whitespace-pre-wrap [overflow-wrap:anywhere]">{e.text}</pre>
        </details>
      )}
    </div>
  );
});

export const AssistantText = memo(function AssistantText({ e }: { e: TimelineEntry }) {
  // plain text on the page: under classic this is the reading line, everything else steps back
  return <div data-chat="reply"><Markdown text={e.text} /><TimelinePin e={e} /></div>;
});

export function Thinking({ e }: { e: TimelineEntry }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="text-muted-foreground">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group/th flex w-full min-w-0 items-center gap-1.5 rounded-md py-0.5 text-left text-xs outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Brain className="size-3.5 shrink-0 text-subtle-foreground" />
        <span className="shrink-0 font-medium">Thinking</span>
        {!open && <span className="min-w-0 truncate text-subtle-foreground italic">{firstLine(e.text, 140)}</span>}
        <ChevronRight className={cn("ml-auto size-3.5 shrink-0 text-subtle-foreground transition-transform duration-150", open && "rotate-90")} />
      </button>
      {open && (
        <div className="mt-1 ml-[0.4375rem] border-l pl-3.5 text-xs leading-relaxed whitespace-pre-wrap text-muted-foreground italic">
          {e.text}
        </div>
      )}
    </div>
  );
}

function ResultText({ text, numbered }: { text: string; numbered?: boolean }) {
  const [all, setAll] = useState(false);
  const lines = numbered ? parseNumberedLines(text) : null;
  const MAX = 60;
  if (lines) {
    const shown = all ? lines : lines.slice(0, MAX);
    return (
      <div className="scroll-thin max-h-[22.5rem] overflow-auto py-1.5 font-mono text-[0.75rem] leading-[1.6]">
        <table className="border-collapse">
          <tbody>
            {shown.map((l, i) => (
              <tr key={i}>
                <td className="pr-3 pl-3 text-right align-top text-subtle-foreground/80 select-none">{l.n}</td>
                <td className="pr-3 whitespace-pre text-foreground/85">{l.code || " "}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {lines.length > MAX && !all && (
          <button className="ml-3 text-2xs font-medium text-muted-foreground hover:text-foreground" onClick={() => setAll(true)}>
            Show all {lines.length} lines
          </button>
        )}
      </div>
    );
  }
  const raw = text.replace(/\n+$/, "");
  const rawLines = raw.split("\n");
  const cut = !all && rawLines.length > MAX;
  return (
    <div className="relative">
      <pre
        className={cn(
          "scroll-thin max-h-[22.5rem] overflow-auto px-3 py-2 font-mono text-[0.75rem] leading-[1.6] whitespace-pre-wrap [overflow-wrap:anywhere]",
          "text-foreground/80",
        )}
      >
        {cut ? rawLines.slice(0, MAX).join("\n") : raw || "(no output)"}
      </pre>
      {cut && (
        <button className="mb-2 ml-3 text-2xs font-medium text-muted-foreground hover:text-foreground" onClick={() => setAll(true)}>
          Show all {rawLines.length} lines
        </button>
      )}
    </div>
  );
}

function RunningElapsed({ at }: { at: number }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  return <span className="tabular-nums text-2xs text-subtle-foreground" aria-label="Elapsed time">{toolElapsed(at, now)}</span>;
}

export function ToolRow({ pair, cwd }: { pair: ToolPair; cwd?: string }) {
  const [open, setOpen] = useState(false);
  const { use, result } = pair;
  const tool = use?.tool ?? result?.tool;
  const s = summarizeTool(tool, use?.input, cwd);
  if (!use && result) {
    s.detail = s.label ? s.detail : firstLine(result.text.replace(/^Data from another agent, not instructions\.\n?/, ""), 120);
    if (!s.detail) s.detail = tool ?? "Tool result";
  }
  const Icon = s.icon;
  const error = !!result?.is_error;
  const running = !!use && !result;
  const input = asInput(use?.input);
  const stats = tool === "Edit" && use ? diffStats(String(input.old_string ?? ""), String(input.new_string ?? "")) : null;
  const desc = tool === "Bash" && typeof input.description === "string" ? input.description : "";

  return (
    <div className={cn("rounded-lg border border-transparent transition-colors duration-150", open && "border-border bg-card")}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className={cn(
          "group/tool flex h-7 w-full min-w-0 items-center gap-2 rounded-lg px-2 text-left text-xs outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring",
          open && "rounded-b-none hover:bg-transparent",
        )}
      >
        <Icon className={cn("size-3.5 shrink-0", error ? "text-state-error" : "text-muted-foreground")} />
        {s.label && <span className="shrink-0 font-medium text-foreground/80">{s.label}</span>}
        <span className={cn("min-w-0 truncate", s.mono ? "font-mono text-[0.7188rem] text-muted-foreground" : "text-foreground/80")}>
          {s.detail}
        </span>
        {desc && !open && <span className="hidden min-w-0 shrink truncate text-subtle-foreground lg:inline">· {desc}</span>}
        <span className="ml-auto flex shrink-0 items-center gap-2 pl-2">
          {stats && (stats.add > 0 || stats.del > 0) && (
            <span className="tabular font-mono text-2xs">
              <span className="text-diff-add-fg">+{stats.add}</span> <span className="text-diff-del-fg">−{stats.del}</span>
            </span>
          )}
          {running && use && <RunningElapsed at={use.at} />}
          {running && <Loader2 className="size-3.5 animate-spin text-subtle-foreground" aria-label="Running" />}
          {error && <span className="text-2xs font-medium text-state-error">Failed</span>}
          <ChevronRight
            className={cn(
              "size-3.5 text-subtle-foreground opacity-0 transition-[transform,opacity] duration-150 group-hover/tool:opacity-100",
              open && "rotate-90 opacity-100",
            )}
          />
        </span>
      </button>
      {open && (
        <div className="space-y-2 border-t px-2.5 pt-2.5 pb-2.5">
          {use && !(tool === "Read" && asInput(use.input).offset == null && asInput(use.input).limit == null) && (
            <ToolInputView tool={tool} input={use.input} cwd={cwd} hidePath />
          )}
          {result ? (
            <div className={cn("overflow-hidden rounded-md border bg-code", error && "border-state-error/35")}>
              <div className={cn("flex h-6 items-center justify-between border-b px-2.5 pr-1", error && "border-state-error/25 bg-diff-del-bg")}>
                <span className={cn("flex items-center gap-1 text-2xs font-medium", error ? "text-state-error" : "text-subtle-foreground")}>
                  <CornerDownRight className="size-3" />
                  {error ? "Error" : "Result"}
                </span>
                <CopyButton text={result.text} label="Copy result" />
              </div>
              <ResultText text={result.text} numbered={tool === "Read"} />
            </div>
          ) : (
            use && (
              <p className="flex items-center gap-1.5 text-2xs text-subtle-foreground">
                <Loader2 className="size-3 animate-spin" /> Waiting for the result…
              </p>
            )
          )}
        </div>
      )}
    </div>
  );
}

export function ToolGroup({ tools, cwd }: { tools: ToolPair[]; cwd?: string }) {
  return (
    <div className="-mx-2 flex flex-col gap-px">
      {tools.map((p) => (
        <ToolRow key={p.key} pair={p} cwd={cwd} />
      ))}
    </div>
  );
}

/**
 * Classic (the owner, 17 Nov 19:26): a run of consecutive tool calls is ONE soft card, closed, that
 * says "N steps"; a click opens the calls (each still opens by itself). Smaller and muted, with
 * a slight indent, so the eye passes over it on the way to the next reply. While a step runs,
 * or when one failed, the closed card says so.
 */
export function ToolRun({ tools, cwd }: { tools: ToolPair[]; cwd?: string }) {
  const [open, setOpen] = useState(false);
  const run = toolRunSummary(tools);
  const last = runningTool(tools);
  const description = asInput(last?.use?.input).description;
  const now = run.running && last ? summarizeTool(last.use?.tool ?? last.result?.tool, last.use?.input, cwd) : null;
  return (
    <div className="ml-3 rounded-lg border border-border/70 bg-muted/35 shadow-[0_1px_1px_rgb(0_0_0/0.03)]" data-chat="steps" data-tool-run={open ? "open" : "closed"}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group/run flex h-7 w-full min-w-0 items-center gap-2 rounded-lg px-2.5 text-left text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ListChecks className="size-3.5 shrink-0 text-subtle-foreground" aria-hidden />
        <span className="shrink-0 font-medium">{run.label}</span>
        {now && !open && (
          <span className="min-w-0 truncate text-subtle-foreground">
            · {typeof description === "string" && description.trim() ? description : [now.label, now.detail].filter(Boolean).join(" ")}
          </span>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-2 pl-2">
          {run.failed > 0 && <span className="text-2xs font-medium text-state-error">{run.failed} failed</span>}
          {last?.use && <RunningElapsed at={last.use.at} />}
          {run.running && <Loader2 className="size-3.5 animate-spin text-subtle-foreground" aria-label="Running" />}
          <ChevronRight className={cn("size-3.5 text-subtle-foreground transition-transform duration-150", open && "rotate-90")} />
        </span>
      </button>
      {open && (
        <div className="flex flex-col gap-px border-t border-border/70 p-1">
          {tools.map((p) => (
            <ToolRow key={p.key} pair={p} cwd={cwd} />
          ))}
        </div>
      )}
    </div>
  );
}

export function MailChip({ e }: { e: TimelineEntry }) {
  const agents = useStore((s) => s.agents);
  const from = String(e.meta?.from ?? "");
  const sender = from === "owner" ? null : agents[from] ?? Object.values(agents).find((a) => a.name === from);
  if (from === "owner") return <UserBubble e={e} />;
  const name = from === "owner" ? "You" : sender?.name ?? (from || "another agent");
  const { text, files } = splitAttachments(e.text);
  return (
    <div className="rounded-xl border bg-muted/35">
      <TimelinePin e={e} />
      <div className="flex items-center gap-2 px-3 pt-2 text-xs">
        <Mail className="size-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">
          {from === "owner" ? "Your answer" : "Message from"}{" "}
          {from !== "owner" && <span className="font-medium text-foreground">{name}</span>}
          {sender && <span className="text-subtle-foreground"> · {sender.level}</span>}
        </span>
        <TimeHint at={e.at}>
          <span className="tabular ml-auto text-2xs text-subtle-foreground">{relativeTime(e.at)}</span>
        </TimeHint>
      </div>
      <div className="px-3 pt-1 pb-2.5">
        {text && <Markdown text={text} className="text-[0.8125rem] text-foreground/90" />}
        {files.length > 0 && <AttachmentList files={files} className={cn("justify-start", text && "mt-2")} />}
      </div>
    </div>
  );
}

/**
 * Classic: mail between agents is shown like tool calls (the owner, 17 Nov 18:30: "don't hide them;
 * each closed by default, expands on click and closes on click again, like an accordion"). One
 * row per message, in the tool rows' style: who it is from and its first words; a click opens
 * the message as it is, another click closes it. A run of several is a group of such rows, as
 * a run of tool calls is (build.ts foldAgentTraffic makes the runs).
 */
export function AgentMessageRow({ e, defaultOpen = false }: { e: TimelineEntry; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const agents = useStore((s) => s.agents);
  // in: who it is from; out (the agent's own send / report / reply): who it goes to
  const out = isOutgoing(e);
  const from = String((out ? e.meta?.to : e.meta?.from) ?? "");
  const sender = agents[from] ?? Object.values(agents).find((a) => a.name === from);
  // the daemon also stamps the sender's name (from_name): it still names an agent that is gone
  const stamped = !out && typeof e.meta?.from_name === "string" ? e.meta.from_name : "";
  const name = sender?.name ?? (stamped || from || (out ? (e.meta?.how === "report" ? "its lead" : "an agent") : "another agent"));
  const { text, files } = splitAttachments(e.text);
  return (
    <div className={cn("rounded-lg border border-transparent transition-colors duration-150", open && "border-border bg-card")} data-agent-message={open ? "open" : "closed"}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className={cn(
          "group/tool flex h-7 w-full min-w-0 items-center gap-2 rounded-lg px-2 text-left text-xs outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring",
          open && "rounded-b-none hover:bg-transparent",
        )}
      >
        {out ? <ArrowRight className="size-3.5 shrink-0 text-muted-foreground" aria-label="to" /> : <Mail className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />}
        {/* the sender chip (the owner, 17 Nov 19:26): who the mail is from, or to, at a glance */}
        <span className="max-w-[11rem] shrink-0 truncate rounded-full bg-brand-soft px-1.5 py-px text-2xs font-medium text-brand" data-mail-chip={out ? "to" : "from"}>
          {out && !from && e.meta?.how === "reply" ? "Recipient unknown" : out ? `to ${name}` : name}
        </span>
        {out && !!e.meta?.failed && <span className="shrink-0 text-2xs font-medium text-state-error">not sent</span>}
        {!open && <span className="min-w-0 truncate text-muted-foreground">{firstLine(text, 140)}</span>}
        <span className="ml-auto flex shrink-0 items-center gap-2 pl-2">
          <TimeHint at={e.at}>
            <span className="tabular text-2xs text-subtle-foreground">{relativeTime(e.at)}</span>
          </TimeHint>
          <ChevronRight
            className={cn(
              "size-3.5 text-subtle-foreground opacity-0 transition-[transform,opacity] duration-150 group-hover/tool:opacity-100",
              open && "rotate-90 opacity-100",
            )}
          />
        </span>
      </button>
      {open && (
        <div className="border-t px-2.5 pt-2 pb-2.5">
          {text && <Markdown text={text} className="text-[0.8125rem] text-foreground/90" />}
          {files.length > 0 && <AttachmentList files={files} className={cn("justify-start", text && "mt-2")} />}
        </div>
      )}
    </div>
  );
}

/** A run of agent messages, with individual rows inside its summary. */
export function AgentTraffic({ entries }: { entries: TimelineEntry[] }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="ml-3 border-l-2 border-brand/45 pl-1.5" data-chat="mail" data-agent-traffic={entries.length}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex min-h-7 w-full items-center gap-2 rounded-lg px-2 text-left text-xs text-muted-foreground hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ChevronRight aria-hidden className={cn("size-3.5", open && "rotate-90")} />
        {trafficLabel(entries.length)}
      </button>
      {open && entries.map((e) => <AgentMessageRow key={e.seq} e={e} />)}
    </div>
  );
}

export function ResultDivider({ e }: { e: TimelineEntry }) {
  const m = e.meta ?? {};
  const turns = Number(m.num_turns ?? m.turns ?? 0);
  const ms = Number(m.duration_ms ?? 0);
  const { failed, label } = turnResult(e);
  const parts = [
    label,
    turns ? plural(turns, "turn") : "",
    ms ? duration(ms) : "",
  ].filter(Boolean);
  return (
    <div className="flex items-center gap-3 py-0.5" role="separator">
      <div className="h-px flex-1 bg-border" />
      <TimeHint at={e.at}>
        <span className={cn("tabular text-2xs text-subtle-foreground", failed && "text-state-error")}>{parts.join(" · ")}</span>
      </TimeHint>
      <div className="h-px flex-1 bg-border" />
    </div>
  );
}

function TerminalRecoveryNote({ e }: { e: TimelineEntry }) {
  const [busy, setBusy] = useState(false);
  const restart = async () => {
    setBusy(true);
    try {
      await api.agentAction(e.agent_id, "restart");
      toast("Restarting agent");
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return <div role="alert" className="flex items-center justify-center gap-2 text-sm text-state-waiting">
    <span>{e.text}</span>
    <Button size="sm" variant="outline" disabled={busy} onClick={() => void restart()}>{busy ? "Restarting..." : "Restart"}</Button>
  </div>;
}

export function SystemLine({ e }: { e: TimelineEntry }) {
  if (e.meta?.terminal_recovery === "needs_attention") return <TerminalRecoveryNote e={e} />;
  const g = guardMeta(e);
  if (g) return <GuardTimelineRow e={e} g={g} />;
  const model = typeof e.meta?.model === "string" ? modelName(e.meta.model) : "";
  return (
    <div className="text-center text-2xs text-subtle-foreground">
      {e.text}
      {model && ` · ${model}`}
      <span className="tabular"> · {clockTime(e.at)}</span>
    </div>
  );
}

export function ErrorRow({ e }: { e: TimelineEntry }) {
  const g = guardMeta(e);
  if (g) return <GuardTimelineRow e={e} g={g} />;
  return (
    <div className="flex items-start gap-2.5 rounded-lg border border-state-error/30 bg-diff-del-bg px-3 py-2.5 text-sm">
      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-state-error" />
      <div className="min-w-0 flex-1 text-foreground/90 [overflow-wrap:anywhere]">{e.text}</div>
      <span className="tabular shrink-0 text-2xs text-subtle-foreground">{relativeTime(e.at)}</span>
    </div>
  );
}

export function TimeBreak({ at }: { at: number }) {
  const d = new Date(at);
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  return (
    <div className="tabular pt-2 text-center text-2xs font-medium text-subtle-foreground">
      {sameDay ? clockTime(at) : d.toLocaleString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit" })}
    </div>
  );
}

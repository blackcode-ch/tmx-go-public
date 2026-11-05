import { memo, useState } from "react";
import { AlertTriangle, Brain, ChevronRight, CornerDownRight, Loader2, Mail } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { absoluteTime, clockTime, duration, firstLine, modelName, plural, relativeTime } from "@/lib/format";
import { useStore } from "@/lib/store";
import { parseNumberedLines, summarizeTool, asInput } from "@/lib/tools";
import type { TimelineEntry } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Markdown } from "../markdown";
import { diffStats } from "../diff-view";
import { ToolInputView } from "./tool-input";
import type { ToolPair } from "./build";
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
  const { text, files } = splitAttachments(e.text);
  return (
    <div className="flex flex-col items-end gap-1.5 pl-[12%]">
      {files.length > 0 && <AttachmentList files={files} />}
      {text && (
        <TimeHint at={e.at}>
          <div className="max-w-full rounded-2xl rounded-br-md bg-bubble px-3.5 py-2 text-sm leading-relaxed whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">
            {text}
          </div>
        </TimeHint>
      )}
    </div>
  );
});

export const AssistantText = memo(function AssistantText({ e }: { e: TimelineEntry }) {
  return <Markdown text={e.text} />;
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

export function MailChip({ e }: { e: TimelineEntry }) {
  const agents = useStore((s) => s.agents);
  const from = String(e.meta?.from ?? "");
  const sender = from === "owner" ? null : agents[from] ?? Object.values(agents).find((a) => a.name === from);
  const name = from === "owner" ? "You" : sender?.name ?? (from || "another agent");
  const { text, files } = splitAttachments(e.text);
  return (
    <div className="rounded-xl border bg-muted/35">
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

export function ResultDivider({ e }: { e: TimelineEntry }) {
  const m = e.meta ?? {};
  const turns = Number(m.num_turns ?? m.turns ?? 0);
  const ms = Number(m.duration_ms ?? 0);
  const failed = e.is_error || (typeof m.subtype === "string" && m.subtype !== "success");
  const parts = [
    failed ? (typeof m.subtype === "string" ? m.subtype.replace(/_/g, " ") : "Ended with an error") : "Done",
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

export function SystemLine({ e }: { e: TimelineEntry }) {
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

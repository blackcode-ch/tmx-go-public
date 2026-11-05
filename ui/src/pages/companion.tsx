// Companion 1:1 (#/companion, DESIGN.md "Companion"): the owner's conversation with Companion.
// tmx's side on the right (the owner's messages, the head's and agents' messages, tasks handed
// over), Companion on the left (replies, questions, messages to the head), tmx's error notes in
// the middle. "Answer" on a waiting question puts the composer into reply mode.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, ArrowDown, ArrowUp, Bot, Check, Clock3, CornerDownRight, ListTodo, Loader2, MessageCircleQuestion, RefreshCw, Settings, Sparkles, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { CompanionAvatar, TaskState } from "@/components/companion/bits";
import { DictateButton } from "@/components/dictate-button";
import { Markdown } from "@/components/markdown";
import { EmptyState, PageHeader } from "@/components/page";
import { useNow } from "@/hooks/use-now";
import { absoluteTime, clockTime } from "@/lib/format";
import { companionChat, sendCompanionMessage, TASK_STATE_LABEL, useCompanion, useCompanionChat } from "@/lib/companion";
import { inFlight, originOf, pendingQuestion, type CompanionEntry, type EntryState } from "@/lib/companion-chat";
import { href, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";

const drafts = new Map<string, string>();
const DRAFT = "companion";
const settingsHref = href(paths.settings()) + "?section=companion";

function dayLabel(ms: number): string {
  const d = new Date(ms);
  const today = new Date();
  const y = new Date();
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
}

/** Who wrote it, as the timeline names them. */
function who(e: CompanionEntry): string {
  if (e.from === "owner") return "you";
  if (e.from === "companion") return "Companion";
  if (e.from === "tmx") return "tmx";
  return e.agent_name || (e.from === "head" ? "the head" : "an agent");
}

/** The head or an agent as Companion's lines name them ("to the head", "for worker-1"). */
function party(e: CompanionEntry): string {
  return e.from === "head" ? "the head" : who(e);
}

/** Whom Companion's entry is for, when not the owner: a message to the head, a reply to an agent. */
function addressee(entries: CompanionEntry[], e: CompanionEntry): string {
  if (e.from !== "companion") return "";
  if (e.kind === "message" && !e.task_id) return "the head";
  const o = originOf(entries, e);
  return o && o.from !== "owner" ? party(o) : "";
}

/** Entries that share a header with the one before: same author and addressee within 5 minutes. */
function sameRun(entries: CompanionEntry[], a: CompanionEntry | undefined, b: CompanionEntry): boolean {
  if (!a) return false;
  const plain = (e: CompanionEntry) => e.kind === "message" || e.kind === "reply";
  return plain(a) && plain(b) && a.from === b.from && who(a) === who(b) && addressee(entries, a) === addressee(entries, b) && b.at - a.at < 5 * 60_000;
}

// ---------------------------------------------------------------------------------------------
// entries

function Time({ at, className }: { at: number; className?: string }) {
  return (
    <span className={cn("tabular text-2xs text-subtle-foreground", className)} title={absoluteTime(at)}>
      {clockTime(at)}
    </span>
  );
}

/** A message's state under it: a spinner while in flight, nothing once it went through. */
function MessageState({ state }: { state?: EntryState }) {
  if (!state || state === "completed" || state === "answered") return null;
  if (state === "sending" || state === "working")
    return (
      <span className="flex items-center gap-1 text-2xs text-muted-foreground" data-in-flight>
        <Loader2 className="size-3 animate-spin" />
        {state === "sending" ? "Sending" : "Companion is working"}
      </span>
    );
  if (state === "queued")
    return (
      <span className="flex items-center gap-1 text-2xs text-muted-foreground" data-in-flight>
        <Clock3 className="size-3" />
        Queued
      </span>
    );
  const bad = state === "failed" || state === "refused" || state === "unconfirmed";
  const asks = state === "input_required" || state === "awaiting_approval";
  return (
    <span className={cn("flex items-center gap-1 text-2xs", bad ? "text-state-error" : "text-muted-foreground")}>
      {asks && <span className="size-1.5 rounded-full bg-state-waiting" />}
      {TASK_STATE_LABEL[state] ?? state}
    </span>
  );
}

function AgentName({ e }: { e: CompanionEntry }) {
  const id = useStore((s) => {
    if (e.from === "head") return s.headId ?? "";
    return Object.values(s.agents).find((a) => a.name === e.agent_name)?.id ?? "";
  });
  const name = who(e);
  if (!id) return <span className="font-medium text-foreground/85">{name}</span>;
  return (
    <a href={href(paths.agent(id))} className="font-medium text-foreground/85 hover:underline">
      {name}
    </a>
  );
}

/** tmx's side: the owner's message (a filled bubble) or the head's / an agent's (outlined, named). */
function OurMessage({ e, first, answer }: { e: CompanionEntry; first: boolean; answer: boolean }) {
  const owner = e.from === "owner";
  return (
    <div className="flex flex-col items-end gap-1 pl-[12%]" data-entry={e.kind} data-from={e.from}>
      {(answer || (!owner && first)) && (
        <div className="flex items-center gap-1.5 text-2xs text-muted-foreground">
          {answer ? <CornerDownRight className="size-3" /> : <Bot className="size-3 text-subtle-foreground" />}
          {answer ? (
            owner ? (
              "Your answer"
            ) : (
              <span>
                <AgentName e={e} /> answers
              </span>
            )
          ) : (
            <span>
              <AgentName e={e} /> to Companion
            </span>
          )}
        </div>
      )}
      <div
        title={absoluteTime(e.at)}
        className={cn(
          "max-w-full rounded-2xl rounded-br-md px-3.5 py-2 text-sm leading-relaxed [overflow-wrap:anywhere]",
          owner ? "bg-bubble whitespace-pre-wrap text-foreground" : "border bg-card",
        )}
      >
        {owner ? e.text : <Markdown text={e.text} className="text-[0.8438rem] leading-relaxed" />}
      </div>
      <div className="flex items-center gap-2">
        <MessageState state={e.state} />
        <Time at={e.at} />
      </div>
    </div>
  );
}

/** A task handed to Companion: who asked, the goal, its state. */
function TaskCard({ e }: { e: CompanionEntry }) {
  const state = e.state && e.state !== "answered" ? e.state : undefined;
  return (
    <div className="flex justify-end pl-[12%]" data-entry="task">
      <div className={cn("w-full max-w-[28.75rem] rounded-xl border bg-card px-3 py-2.5", state && inFlight(e) && "border-border-strong")}>
        <div className="flex items-center gap-2 text-2xs text-muted-foreground">
          <ListTodo className="size-3.5 shrink-0 text-subtle-foreground" />
          <span className="min-w-0 truncate">
            Task from {e.from === "owner" ? <span className="font-medium text-foreground/85">you</span> : <AgentName e={e} />}
          </span>
          <Time at={e.at} className="ml-auto shrink-0" />
        </div>
        <p className="mt-1.5 line-clamp-3 text-[0.8125rem] leading-snug [overflow-wrap:anywhere]">{e.text}</p>
        {state && (
          <div className="mt-2 flex items-center gap-3">
            <TaskState state={state} className="text-2xs" />
            {state === "awaiting_approval" && (
              <a className="text-2xs text-brand hover:underline" href={href(paths.inbox())}>
                Decide in Needs you
              </a>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/** Companion's reply or message. */
function CompanionMessage({ e, first, to }: { e: CompanionEntry; first: boolean; to: string }) {
  return (
    <div className="flex gap-2.5 pr-[8%]" data-entry={e.kind} data-from="companion">
      <div className="w-7 shrink-0">{first && <CompanionAvatar />}</div>
      <div className="min-w-0 flex-1">
        {first && (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pb-0.5">
            <span className="text-[0.8125rem] font-semibold">Companion</span>
            {to && <span className="text-2xs text-muted-foreground">to {to}</span>}
            <Time at={e.at} />
          </div>
        )}
        <Markdown text={e.text} className="text-[0.8438rem] leading-relaxed [overflow-wrap:anywhere]" />
      </div>
    </div>
  );
}

/** Companion's question: highlighted while it waits; Answer puts the composer into reply mode. */
function Question({ e, about, connected, answering }: { e: CompanionEntry; about?: CompanionEntry; connected: boolean; answering: boolean }) {
  const waiting = pendingQuestion(e);
  return (
    <div className="flex gap-2.5 pr-[8%]" data-entry="question" data-waiting={waiting || undefined}>
      <div className="w-7 shrink-0">
        <CompanionAvatar />
      </div>
      <div
        className={cn(
          "min-w-0 flex-1 rounded-xl border px-3 py-2.5",
          waiting ? "border-state-waiting/50 bg-state-waiting/10" : "bg-muted/30",
          answering && "ring-2 ring-brand/30",
        )}
      >
        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
          <MessageCircleQuestion className={cn("size-3.5 shrink-0", waiting ? "text-state-waiting" : "text-subtle-foreground")} />
          <span className="text-[0.8125rem] font-semibold">Companion asks</span>
          {about && about.from !== "owner" && <span className="text-2xs text-muted-foreground">for {party(about)}</span>}
          <Time at={e.at} />
        </div>
        <Markdown text={e.text} className="mt-1 text-[0.8438rem] leading-relaxed [overflow-wrap:anywhere]" />
        {about && <p className="mt-1 truncate text-2xs text-muted-foreground">About: {about.text}</p>}
        <div className="mt-2 flex items-center gap-2">
          {waiting ? (
            <Button size="xs" variant={answering ? "secondary" : "default"} disabled={!connected || answering} onClick={() => companionChat.answer(e)}>
              <CornerDownRight />
              {answering ? "Answering" : "Answer"}
            </Button>
          ) : (
            <span className="flex items-center gap-1 text-2xs text-muted-foreground">
              <Check className="size-3" />
              Answered
            </span>
          )}
        </div>
      </div>
    </div>
  );
}

function ErrorNote({ e }: { e: CompanionEntry }) {
  return (
    <div className="flex justify-center px-4" data-entry="error">
      <p className="flex max-w-[85%] items-start gap-1.5 text-xs text-muted-foreground">
        <AlertCircle className="mt-px size-3.5 shrink-0 text-state-error/70" />
        <span className="[overflow-wrap:anywhere]">
          {e.text} <Time at={e.at} className="ml-1" />
        </span>
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// the timeline

function Timeline({ connected }: { connected: boolean }) {
  const entries = useCompanionChat((s) => s.entries);
  const loaded = useCompanionChat((s) => s.loaded);
  const error = useCompanionChat((s) => s.error);
  const replyTo = useCompanionChat((s) => s.replyTo);
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [atBottom, setAtBottom] = useState(true);
  const [unseen, setUnseen] = useState(false);
  const lastId = entries.at(-1)?.id ?? "";

  // Answers: an owner's (or head's) message on a task Companion asked about before it.
  const answers = useMemo(() => {
    const asked = new Set<string>();
    const out = new Set<string>();
    for (const e of entries) {
      if (e.kind === "question" && e.task_id) asked.add(e.task_id);
      else if (e.kind === "message" && e.from !== "companion" && e.task_id && asked.has(e.task_id)) out.add(e.id);
    }
    return out;
  }, [entries]);

  const toBottom = (smooth = false) => {
    const el = scroller.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  };

  // New entries: follow them while at the bottom, otherwise offer to jump.
  useLayoutEffect(() => {
    if (stick.current) toBottom();
    else setUnseen(true);
  }, [lastId, entries.length]);

  // Content that grows later (markdown, fonts), and the list getting shorter (the composer grows
  // with a reply chip or a long draft), keep the bottom pinned.
  useEffect(() => {
    const el = content.current;
    const box = scroller.current;
    if (!el || !box) return;
    const ro = new ResizeObserver(() => stick.current && toBottom());
    ro.observe(el);
    ro.observe(box);
    return () => ro.disconnect();
  }, []);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 64;
    stick.current = bottom;
    if (bottom !== atBottom) setAtBottom(bottom);
    if (bottom) setUnseen(false);
  };

  let body: React.ReactNode;
  if (!loaded) {
    body = (
      <div className="space-y-5 pt-2" aria-busy="true">
        <Skeleton className="ml-auto h-9 w-2/3 rounded-2xl" />
        <div className="flex gap-2.5">
          <Skeleton className="size-7 shrink-0 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-11/12" />
            <Skeleton className="h-3.5 w-3/5" />
          </div>
        </div>
        <Skeleton className="ml-auto h-16 w-1/2 rounded-xl" />
      </div>
    );
  } else if (entries.length === 0 && error) {
    body = (
      <EmptyState
        icon={AlertCircle}
        title="Couldn't load the conversation"
        className="mt-6 border-none bg-transparent"
        actions={
          <Button variant="outline" size="sm" onClick={() => void companionChat.load()}>
            <RefreshCw />
            Try again
          </Button>
        }
      >
        {error}
      </EmptyState>
    );
  } else if (entries.length === 0) {
    body = (
      <div className="flex flex-col items-center px-6 pt-14 pb-6 text-center" data-companion-empty>
        <CompanionAvatar size="lg" />
        <p className="mt-3 text-[0.875rem] font-medium">Talk with Companion</p>
        <p className="mt-1 max-w-sm text-sm text-muted-foreground">
          {connected
            ? "Write to Companion here. You also see what the head and Companion say to each other, and the tasks tmx hands it."
            : "Connect Companion in Settings to write to it here."}
        </p>
        {!connected && (
          <Button variant="outline" size="sm" asChild className="mt-4">
            <a href={settingsHref}>Open Settings</a>
          </Button>
        )}
      </div>
    );
  } else {
    body = entries.map((e, i) => {
      const prev = entries[i - 1];
      const newDay = !prev || new Date(prev.at).toDateString() !== new Date(e.at).toDateString();
      const run = !newDay && sameRun(entries, prev, e);
      let row: React.ReactNode;
      if (e.kind === "error" || e.from === "tmx") row = <ErrorNote e={e} />;
      else if (e.kind === "task") row = <TaskCard e={e} />;
      else if (e.kind === "question")
        row = <Question e={e} about={originOf(entries, e)} connected={connected} answering={!!replyTo && replyTo.taskId === e.task_id && pendingQuestion(e)} />;
      else if (e.from === "companion") row = <CompanionMessage e={e} first={!run} to={addressee(entries, e)} />;
      else row = <OurMessage e={e} first={!run} answer={answers.has(e.id)} />;
      return (
        <div key={e.id} className={cn(i > 0 && !newDay && (run ? "mt-1.5" : "mt-4"))}>
          {newDay && (
            <div className={cn("mb-3 flex items-center gap-3 text-2xs font-medium text-subtle-foreground", i > 0 && "mt-5")}>
              <span className="h-px flex-1 bg-border" />
              {dayLabel(e.at)}
              <span className="h-px flex-1 bg-border" />
            </div>
          )}
          {row}
        </div>
      );
    });
  }

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div ref={scroller} onScroll={onScroll} data-testid="companion-scroll" className="scroll-thin min-h-0 flex-1 overflow-y-auto">
        <div ref={content} className="mx-auto max-w-[51.25rem] px-4 pt-4 pb-3 md:px-6">
          {body}
        </div>
      </div>
      {!atBottom && entries.length > 0 && (
        <div className="pointer-events-none absolute inset-x-0 bottom-2 flex justify-center">
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
            {unseen ? "New messages" : "Jump to latest"}
          </Button>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// the composer

function Composer({ connected, loaded }: { connected: boolean; loaded: boolean }) {
  const off = loaded && !connected; // known to be off (not just loading)
  const replyTo = useCompanionChat((s) => s.replyTo);
  const [text, setText] = useState(() => drafts.get(DRAFT) ?? "");
  const [sending, setSending] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 200) + "px";
  }, [text]);

  // Answer moves the cursor here.
  useEffect(() => {
    if (replyTo) ta.current?.focus();
  }, [replyTo]);

  const update = (v: string) => {
    setText(v);
    drafts.set(DRAFT, v);
  };
  const canSend = connected && !sending && !!text.trim();

  async function send() {
    if (!canSend) return;
    setSending(true);
    try {
      await sendCompanionMessage(text, replyTo?.taskId);
      setText("");
      drafts.delete(DRAFT);
    } catch (e) {
      toast.error((e as Error).message || "The message wasn't sent.");
    } finally {
      setSending(false);
      ta.current?.focus();
    }
  }

  const hint = !loaded ? (
    ""
  ) : off ? (
    <>
      Not connected ·{" "}
      <a className="text-brand hover:underline" href={settingsHref}>
        connect it in Settings
      </a>
    </>
  ) : replyTo ? (
    "Goes to Companion as your answer · Esc to cancel"
  ) : (
    "Enter to send · Shift+Enter for a new line"
  );

  return (
    <div className="shrink-0 px-4 pt-2 pb-4 md:px-6">
      <div className="mx-auto max-w-[51.25rem]">
        <div
          className={cn(
            "relative rounded-xl border border-border-strong bg-card transition-[border-color,box-shadow] duration-150 focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/25",
            off && "opacity-70",
          )}
        >
          {replyTo && (
            <div className="mx-2 mt-2 flex items-start gap-2 rounded-lg border-l-2 border-brand/60 bg-muted/60 py-1.5 pr-1 pl-2.5" data-reply-chip>
              <div className="min-w-0 flex-1">
                <div className="truncate text-2xs font-semibold text-brand">Answering Companion</div>
                <div className="truncate text-xs text-muted-foreground">{replyTo.text}</div>
              </div>
              <Button variant="ghost" size="icon-xs" aria-label="Don't answer" className="text-muted-foreground" onClick={() => companionChat.clearReply()}>
                <X />
              </Button>
            </div>
          )}
          <textarea
            ref={ta}
            value={text}
            rows={1}
            disabled={!connected}
            onChange={(e) => update(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && replyTo) {
                e.preventDefault();
                companionChat.clearReply();
                return;
              }
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void send();
              }
            }}
            placeholder={off ? "Companion isn't connected" : replyTo ? "Your answer…" : "Message Companion…"}
            aria-label="Message Companion"
            className="scroll-thin block max-h-52 w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-sm leading-relaxed outline-none placeholder:text-subtle-foreground disabled:cursor-not-allowed"
          />
          <div className="flex items-center gap-1 px-2 pb-2">
            <DictateButton
              target={ta}
              value={text}
              draft={{ id: "companion", label: "Companion", path: paths.companion(), drafts, draftKey: DRAFT }}
              disabled={!connected}
              onChange={update}
            />
            <span className="ml-1 min-w-0 flex-1 truncate text-2xs text-subtle-foreground">{hint}</span>
            <Button size="icon-sm" onClick={() => void send()} disabled={!canSend} aria-label={sending ? "Sending…" : "Send"} className="ml-auto rounded-lg">
              {sending ? <Loader2 className="size-4 animate-spin" /> : <ArrowUp className="size-4" />}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// the page

function ConnectionLine({ connected, busy }: { connected: boolean; busy: boolean }) {
  const st = useCompanion((s) => s.status);
  let dot = "bg-state-ok";
  let label = "Connected";
  if (!connected) {
    dot = "border-[1.5px] border-state-stopped";
    label = "Not connected";
  } else if (st?.connected && st.key_ok === false) {
    dot = "bg-state-error";
    label = "Its key no longer works";
  } else if (st?.connected && st.checked && !st.reachable) {
    dot = "bg-state-waiting";
    label = "Not answering · messages wait";
  } else if (busy) {
    dot = "bg-state-working animate-pulse-dot";
    label = "Working…";
  }
  return (
    <span className="flex items-center gap-1.5" data-connection={connected ? "connected" : "off"}>
      <span className={cn("inline-flex size-2 shrink-0 rounded-full", dot)} />
      {label}
    </span>
  );
}

export function CompanionPage() {
  const connected = useCompanionChat((s) => s.connected);
  const busy = useCompanionChat((s) => s.busy);
  const loaded = useCompanionChat((s) => s.loaded);
  const available = useCompanionChat((s) => s.available);
  const entries = useCompanionChat((s) => s.entries);
  useNow(60_000); // day labels

  useEffect(() => {
    void companionChat.load();
  }, []);

  // Being here (with the window visible) marks Companion's entries seen.
  useEffect(() => {
    const mark = () => document.visibilityState === "visible" && companionChat.markSeen();
    mark();
    document.addEventListener("visibilitychange", mark);
    return () => document.removeEventListener("visibilitychange", mark);
  }, [entries]);

  if (available === false) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <EmptyState icon={Sparkles} title="This tmx can't talk with Companion yet" className="w-full max-w-md border-none bg-transparent">
          Update tmx to talk with Companion here.
        </EmptyState>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        actions={
          <>
            {loaded && !connected && (
              <Button size="sm" variant="outline" asChild>
                <a href={settingsHref}>
                  <span className="hidden sm:inline">Connect in Settings</span>
                  <span className="sm:hidden">Connect</span>
                </a>
              </Button>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="ghost" size="icon-sm" className="text-muted-foreground" asChild>
                  <a href={settingsHref} aria-label="Companion settings">
                    <Settings className="size-4" />
                  </a>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Settings → Companion</TooltipContent>
            </Tooltip>
          </>
        }
      >
        <CompanionAvatar />
        <div className="min-w-0">
          <h1 className="truncate text-[0.875rem] leading-tight font-semibold tracking-tight">Companion</h1>
          <div className="truncate text-2xs leading-tight text-muted-foreground">{loaded ? <ConnectionLine connected={connected} busy={busy} /> : "Loading…"}</div>
        </div>
      </PageHeader>
      <Timeline connected={connected} />
      <Composer connected={connected} loaded={loaded} />
    </div>
  );
}

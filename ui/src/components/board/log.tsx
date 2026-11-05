// A string's log on the board canvas (the head's threads and the chat topics): short lines
// worth keeping (decision, action, info, link); append-only, a correction strikes the old line
// through. Where a line is saved is the page's (onSave).
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowRightLeft, CheckCheck, CheckCircle2, CircleCheck, CirclePause, Gavel, Info, Link2, MessageCircleQuestion, OctagonX, Play, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useNow } from "@/hooks/use-now";
import { LOG_KINDS } from "@/lib/board";
import { absoluteTime, relativeTime } from "@/lib/format";
import type { LogKind } from "@/lib/types";
import { cn } from "@/lib/utils";
import type { LogLine } from "./canvas";

const ENTRY_ICON = {
  decision: Gavel,
  action: CircleCheck,
  info: Info,
  link: Link2,
  result: CheckCircle2,
  blocker: OctagonX,
  handoff: ArrowRightLeft,
  ask: MessageCircleQuestion,
  start: Play,
  pause: CirclePause,
  done: CheckCheck,
} as const;
const ENTRY_TONE = {
  decision: "text-brand",
  action: "text-state-ok",
  info: "text-muted-foreground",
  link: "text-brand",
  result: "text-state-ok",
  blocker: "text-destructive",
  handoff: "text-brand",
  ask: "text-brand",
  start: "text-muted-foreground",
  pause: "text-muted-foreground",
  done: "text-state-ok",
} as const;

export function EntryIcon({ kind, className }: { kind: LogKind; className?: string }) {
  const Icon = ENTRY_ICON[kind] ?? Info;
  return <Icon className={cn("size-3.5 shrink-0", ENTRY_TONE[kind], className)} aria-label={kind} />;
}

/** The first sentence of a text, for "Save as decision" (at most 400 characters). */
export function firstSentence(s: string): string {
  const t = s.trim();
  const m = /^(.{12,}?[.!?])(\s|$)/s.exec(t);
  const one = (m ? m[1] : t).replace(/\s+/g, " ");
  return one.length > 400 ? one.slice(0, 399).trimEnd() + "…" : one;
}

function isURL(s: string) {
  return /^https?:\/\//i.test(s);
}

/** What a log form saves: the page files it (with its card, if any). */
export type EntryDraft = { kind: LogKind; text: string; link?: string; card?: number; corrects?: string };

/** The log, oldest first; corrected lines are struck through. `who` names a line's author. */
export function LogView<T extends LogLine>({
  entries,
  archived,
  adding,
  who,
  empty,
  onSave,
}: {
  entries: T[];
  archived?: boolean;
  adding: boolean;
  who: (e: T) => string;
  empty: ReactNode;
  onSave: (e: EntryDraft) => Promise<unknown>;
}) {
  useNow(30_000);
  const [fixing, setFixing] = useState<T | null>(null);
  const correctedBy = useMemo(() => new Map(entries.filter((e) => e.corrects).map((e) => [e.corrects!, e])), [entries]);
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => {
    end.current?.scrollIntoView({ block: "end" });
  }, [entries.length]);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div data-scrolls className="scroll-thin min-h-0 flex-1 overflow-y-auto px-5 py-4">
        {!entries.length ? (
          <p className="text-sm text-muted-foreground">{empty}</p>
        ) : (
          <ol className="space-y-3">
            {entries.map((e) => {
              const fixedBy = correctedBy.get(e.id);
              return (
                <li key={e.id} className="group flex items-start gap-2.5">
                  <EntryIcon kind={e.kind} className="mt-1" />
                  <div className="min-w-0 flex-1">
                    <p className={cn("text-sm leading-snug [overflow-wrap:anywhere] whitespace-pre-line", fixedBy && "text-muted-foreground line-through")}>{e.text}</p>
                    {e.link &&
                      (isURL(e.link) ? (
                        <a href={e.link} target="_blank" rel="noreferrer noopener" className="mt-0.5 block truncate text-xs text-brand hover:underline">
                          {e.link}
                        </a>
                      ) : e.link.startsWith("#/") ? (
                        // a page of this app, e.g. a recording (#/recordings/<id>)
                        <a href={e.link} className="mt-0.5 block truncate text-xs text-brand hover:underline">
                          {e.link.startsWith("#/recordings/") ? "Open the recording" : e.link}
                        </a>
                      ) : (
                        <code className="mt-0.5 block truncate text-xs text-muted-foreground">{e.link}</code>
                      ))}
                    <p className="mt-0.5 text-2xs text-muted-foreground">
                      <span className="capitalize">{e.kind}</span> · {who(e)} · <span title={absoluteTime(e.at)}>{relativeTime(e.at)}</span>
                      {e.corrects && " · a correction"}
                      {fixedBy && " · corrected"}
                    </p>
                  </div>
                  {!fixedBy && !archived && (
                    <Button
                      variant="ghost"
                      size="xs"
                      className="shrink-0 text-muted-foreground opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                      onClick={() => setFixing(e)}
                    >
                      <Undo2 /> Correct
                    </Button>
                  )}
                </li>
              );
            })}
          </ol>
        )}
        <div ref={end} />
      </div>
      {!archived && (
        <div className="shrink-0 border-t p-3">
          <EntryForm
            key={fixing?.id ?? "new"}
            initial={fixing ? { kind: fixing.kind, text: fixing.text, link: fixing.link, corrects: fixing.id } : undefined}
            autoFocus={adding || !!fixing}
            onSave={onSave}
            onDone={() => setFixing(null)}
          />
        </div>
      )}
    </div>
  );
}

export function EntryForm({
  initial,
  autoFocus,
  cancelable,
  onSave,
  onDone,
}: {
  initial?: EntryDraft;
  autoFocus?: boolean;
  /** shows Cancel (corrections and "Save as decision" always do) */
  cancelable?: boolean;
  onSave: (e: EntryDraft) => Promise<unknown>;
  onDone: (saved: boolean) => void;
}) {
  const [kind, setKind] = useState<LogKind>(initial?.kind ?? "decision");
  const [text, setText] = useState(initial?.text ?? "");
  const [link, setLink] = useState(initial?.link ?? "");
  const [showLink, setShowLink] = useState(!!initial?.link || kind === "link");
  const [busy, setBusy] = useState(false);
  const left = 400 - text.length;

  async function save() {
    if (!text.trim() || left < 0 || busy) return;
    setBusy(true);
    try {
      await onSave({ kind, text: text.trim(), link: link.trim() || undefined, card: initial?.card, corrects: initial?.corrects });
      setText("");
      setLink("");
      onDone(true);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      className="space-y-2"
    >
      {initial?.corrects && <p className="text-2xs text-muted-foreground">Correcting a line: the old one stays, struck through.</p>}
      <div className="flex flex-wrap gap-1" role="radiogroup" aria-label="Kind">
        {LOG_KINDS.map((k) => (
          <button
            key={k.kind}
            type="button"
            role="radio"
            aria-checked={kind === k.kind}
            onClick={() => {
              setKind(k.kind);
              if (k.kind === "link") setShowLink(true);
            }}
            className={cn(
              "inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs transition-colors",
              kind === k.kind ? "border-border-strong bg-accent text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            <EntryIcon kind={k.kind} />
            {k.label}
          </button>
        ))}
      </div>
      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            void save();
          } else if (e.key === "Escape") onDone(false);
        }}
        autoFocus={autoFocus}
        rows={2}
        placeholder={kind === "decision" ? "What was decided, in a line or two" : kind === "action" ? "What was done or is to be done" : kind === "link" ? "What the link is" : "What is worth knowing"}
        aria-label="Log line"
        className="min-h-0 resize-none text-sm"
      />
      {showLink && <Input value={link} onChange={(e) => setLink(e.target.value)} placeholder="https://… or a file path" aria-label="Link" className="h-8 text-sm" />}
      <div className="flex items-center gap-2">
        {!showLink && (
          <button type="button" onClick={() => setShowLink(true)} className="text-2xs text-muted-foreground hover:text-foreground">
            + Add a link
          </button>
        )}
        <span className={cn("ml-auto tabular text-2xs", left < 0 ? "text-state-error" : "text-subtle-foreground")}>{left}</span>
        {(initial?.corrects || initial?.card || cancelable) && (
          <Button type="button" variant="ghost" size="sm" onClick={() => onDone(false)}>
            Cancel
          </Button>
        )}
        <Button type="submit" size="sm" disabled={!text.trim() || left < 0 || busy}>
          {initial?.corrects ? "Save correction" : "Add to log"}
        </Button>
      </div>
    </form>
  );
}

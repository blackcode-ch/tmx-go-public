// The head on the Matrix (DESIGN.md "Matrix", "The head's view"): what the Board was until 3 Nov.
// The head's turns as cards on a thread, its open questions, a turn read in full, writing to the
// head in a thread, moving a turn to another thread, saving it as a decision, the thread's log and
// the thread's own menu. The Matrix's thread panel shows these; nothing here is a page of its own.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowUp,
  BookmarkPlus,
  BellRing,
  CircleHelp,
  FolderPlus,
  Mail,
  MessagesSquare,
  MoreHorizontal,
  Pencil,
  Rocket,
  Send,
  Sparkles,
  Trash2,
  TriangleAlert,
  User,
  Wrench,
} from "lucide-react";
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
import { Skeleton } from "@/components/ui/skeleton";
import { after, ColourSubmenu } from "@/components/board/canvas";
import { RenameDialog } from "@/components/board/dialogs";
import { EntryForm, firstSentence, LogView } from "@/components/board/log";
import { MoveMenu, SidePanelFrame } from "@/components/board/side-panel";
import { buildItems } from "@/components/conversation/build";
import { Composer } from "@/components/conversation/composer";
import { ItemView, spacing } from "@/components/conversation/conversation";
import { useNow } from "@/hooks/use-now";
import { boardApi, loadBoard, threadColor } from "@/lib/board";
import { absoluteTime, relativeTime } from "@/lib/format";
import { href, navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import type { Agent, Approval, BoardCard, Thread, ThreadEntry, TimelineEntry } from "@/lib/types";
import { cn } from "@/lib/utils";

// ---------------------------------------------------------------------------------------------
// a thread's menu (the Board's string header menu) and its urgency place

export function ThreadMenu({
  thread,
  cards,
  onUp,
  onDown,
}: {
  thread: Thread;
  /** the head's turns on it (the delete dialog says where they go) */
  cards: number;
  /** move it up or down the owner's urgency order */
  onUp?: () => void;
  onDown?: () => void;
}) {
  const projects = useStore((s) => s.projects);
  const [rename, setRename] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const project = thread.project_id ? projects[thread.project_id] : undefined;

  async function patch(p: { title?: string; color?: string; archived?: boolean }) {
    try {
      await boardApi.patchThread(thread.id, p);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function remove() {
    try {
      await boardApi.deleteThread(thread.id);
      toast.success(`Deleted ${thread.title}; its cards are under General.`);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-xs" aria-label="Thread options">
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {(onUp || onDown) && (
            <>
              <DropdownMenuItem disabled={!onUp} onSelect={() => onUp?.()}>
                <ArrowUp /> More urgent
              </DropdownMenuItem>
              <DropdownMenuItem disabled={!onDown} onSelect={() => onDown?.()}>
                <ArrowDown /> Less urgent
              </DropdownMenuItem>
              <DropdownMenuSeparator />
            </>
          )}
          {project && (
            <DropdownMenuItem onSelect={() => navigate(paths.project(project.id))}>
              <FolderPlus /> Open the project
            </DropdownMenuItem>
          )}
          {thread.chat_id && (
            <DropdownMenuItem onSelect={() => navigate(paths.chat(thread.chat_id!))}>
              <MessagesSquare /> Open the chat
            </DropdownMenuItem>
          )}
          {thread.kind === "topic" && (
            <DropdownMenuItem onSelect={after(() => setRename(true))}>
              <Pencil /> Rename
            </DropdownMenuItem>
          )}
          <ColourSubmenu onPick={(color) => void patch({ color })} />
          {thread.kind !== "general" && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => void patch({ archived: !thread.archived })}>
                {thread.archived ? <ArchiveRestore /> : <Archive />}
                {thread.archived ? "Restore" : "Archive"}
              </DropdownMenuItem>
            </>
          )}
          {thread.kind === "topic" && (
            <DropdownMenuItem variant="destructive" onSelect={after(() => setConfirmDelete(true))}>
              <Trash2 /> Delete topic…
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <RenameDialog open={rename} onOpenChange={setRename} title={thread.title} onSave={(title) => patch({ title })} />
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {thread.title}?</AlertDialogTitle>
            <AlertDialogDescription>
              {cards ? `Its ${cards} ${cards === 1 ? "card moves" : "cards move"} to General; nothing in the head's conversation is deleted.` : "The topic has no cards yet."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction className="bg-destructive text-white hover:bg-destructive/90" onClick={() => void remove()}>
              Delete topic
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// ---------------------------------------------------------------------------------------------
// cards

const KIND_ICON = { owner: User, mail: Mail, chat: MessagesSquare, start: Sparkles } as const;

function senderLabel(c: BoardCard): string {
  if (c.kind === "owner") return "You";
  if (c.kind === "start") return "Start-up";
  if (c.kind === "chat") {
    if (c.sender === "owner") return "You, in the chat";
    const s = c.sender
      .split(", ")
      .map((x) => (x === "owner" ? "you" : x))
      .join(", ");
    return s.charAt(0).toUpperCase() + s.slice(1);
  }
  return c.sender;
}

export function CardView({ card: c, selected, working, onOpen, first }: { card: BoardCard; selected: boolean; working: boolean; onOpen: () => void; first: boolean }) {
  useNow(30_000);
  const Icon = KIND_ICON[c.kind] ?? Mail;
  return (
    <button
      data-board-item
      onClick={onOpen}
      aria-label={`${senderLabel(c)}: ${c.prompt.slice(0, 80)}`}
      aria-pressed={selected}
      className={cn(
        "group w-full cursor-pointer rounded-xl border bg-card text-left shadow-sm transition-[box-shadow,border-color] duration-150 hover:border-border-strong hover:shadow-md focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:outline-none",
        selected && "border-[var(--lane)] shadow-md ring-2 ring-[var(--lane)]/25",
        c.error && "border-state-error/50",
      )}
    >
      <div className="px-3.5 pt-3 pb-2.5">
        <div className="flex items-center gap-1.5 text-2xs text-muted-foreground">
          <Icon className={cn("size-3.5", c.kind === "owner" && "text-foreground")} />
          <span className={cn("truncate font-medium", c.kind === "owner" ? "text-foreground" : "")}>{senderLabel(c)}</span>
          {first && c.kind === "mail" && <span className="truncate">wrote in</span>}
          <span className="ml-auto shrink-0 tabular" title={absoluteTime(c.at)}>
            {relativeTime(c.at)}
          </span>
        </div>
        <p className="mt-1.5 line-clamp-3 text-[0.8125rem] leading-snug [overflow-wrap:anywhere] whitespace-pre-line">{c.prompt || "(attachment)"}</p>
      </div>
      {(c.reply || working || c.notes?.length) && (
        <div className="border-t bg-muted/35 px-3.5 py-2.5">
          {c.reply ? (
            <p className="line-clamp-4 text-xs leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
              <span className="font-medium text-foreground">Head: </span>
              {plain(c.reply)}
            </p>
          ) : c.notes?.length ? (
            <p className="line-clamp-3 text-xs leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
              <span className="font-medium text-foreground">Head told you: </span>
              {plain(c.notes[c.notes.length - 1])}
            </p>
          ) : null}
          {working && (
            <p className={cn("flex items-center gap-1.5 text-xs text-muted-foreground", c.reply && "mt-1.5")}>
              <span className="flex gap-0.5">
                {[0, 1, 2].map((i) => (
                  <span key={i} className="size-1 animate-pulse-dot rounded-full bg-state-working" style={{ animationDelay: `${i * 160}ms` }} />
                ))}
              </span>
              The head is on it
            </p>
          )}
        </div>
      )}
      <Chips card={c} />
    </button>
  );
}

function Chips({ card: c }: { card: BoardCard }) {
  const chips: { icon: typeof Rocket; text: string; tone?: string; title: string }[] = [];
  if (c.projects?.length) chips.push({ icon: FolderPlus, text: c.projects.join(", "), title: "Created the project" });
  if (c.launched?.length) chips.push({ icon: Rocket, text: c.launched.join(", "), title: "Launched" });
  if (c.sent_to?.length) chips.push({ icon: Send, text: c.sent_to.join(", "), title: "Messaged" });
  if (c.notes?.length && c.reply) chips.push({ icon: BellRing, text: `${c.notes.length} ${c.notes.length === 1 ? "update" : "updates"}`, title: "Updates sent to you" });
  if (c.asked?.length) chips.push({ icon: CircleHelp, text: `asked you`, tone: "text-state-waiting", title: c.asked.join("\n") });
  if (c.error) chips.push({ icon: TriangleAlert, text: "error", tone: "text-state-error", title: "The turn ended with an error" });
  if (!chips.length && !c.tools) return null;
  return (
    <div className="flex flex-wrap items-center gap-1 border-t px-3 py-2">
      {chips.map((ch, i) => (
        <span key={i} title={ch.title} className={cn("inline-flex max-w-full items-center gap-1 rounded-md bg-muted px-1.5 py-0.5 text-2xs text-muted-foreground", ch.tone)}>
          <ch.icon className="size-3 shrink-0" />
          <span className="truncate">{ch.text}</span>
        </span>
      ))}
      {c.tools > 0 && (
        <span className="ml-auto inline-flex items-center gap-1 text-2xs text-subtle-foreground" title="Tool calls in this turn">
          <Wrench className="size-3" />
          {c.tools}
        </span>
      )}
    </div>
  );
}

export function QuestionCard({ q }: { q: Approval }) {
  return (
    <div data-board-item className="w-full rounded-xl border border-state-waiting/60 bg-card shadow-sm">
      <div className="px-3.5 pt-3 pb-2.5">
        <div className="flex items-center gap-1.5 text-2xs font-medium text-state-waiting">
          <CircleHelp className="size-3.5" />
          The head asks you
        </div>
        <p className="mt-1.5 line-clamp-4 text-[0.8125rem] leading-snug [overflow-wrap:anywhere]">{q.question}</p>
      </div>
      <div className="flex items-center gap-2 border-t px-3 py-2">
        <span className="text-2xs text-muted-foreground">{relativeTime(q.created_at)}</span>
        <Button size="xs" className="ml-auto" onClick={() => navigate(paths.inbox() + "?id=" + q.id)}>
          Answer
        </Button>
      </div>
    </div>
  );
}

/** Markdown to a one-line-ish preview. */
function plain(md: string): string {
  return md
    .replace(/```[\s\S]*?```/g, " [code] ")
    .replace(/^\s*\|?(\s*:?-{3,}:?\s*\|)+\s*:?-*:?\s*$/gm, "") // table separator rows
    .replace(/^\s*\|(.*)\|\s*$/gm, (_, row: string) => row.split("|").map((c) => c.trim()).join(" · ") + ";")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/\*\*([^*]+)\*\*|__([^_]+)__/g, "$1$2")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^#+\s*/gm, "")
    .replace(/^\s*[-*]\s+/gm, "• ")
    .replace(/\s+/g, " ")
    .trim();
}

// ---------------------------------------------------------------------------------------------
// the head panel (the Board's side panel): a whole turn, a thread's log, or writing in a thread

export function HeadPanel({
  head,
  thread,
  card,
  log,
  threads,
  onClose,
}: {
  head: Agent;
  thread: Thread;
  card?: BoardCard;
  /** the thread's log view instead of a card */
  log?: { entries: ThreadEntry[]; adding: boolean };
  threads: Thread[];
  onClose: () => void;
}) {
  const projects = useStore((s) => s.projects);
  const targets = threads.filter((t) => !t.archived && t.id !== thread.id);
  const [saving, setSaving] = useState(false);
  const addEntry = (e: { kind: ThreadEntry["kind"]; text: string; link?: string; card?: number; corrects?: string }) => boardApi.addEntry({ thread: thread.id, ...e });

  async function move(to: Thread) {
    if (!card) return;
    try {
      await boardApi.moveCard(card.id, to.id);
      toast.success(`Moved to ${titleOf(to, projects)}.`);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <SidePanelFrame
      color={threadColor(thread)}
      title={thread.title}
      sub={log ? "Log" : undefined}
      label={card ? "Card" : log ? `Log of ${thread.title}` : `Write in ${thread.title}`}
      onClose={onClose}
      actions={
        <>
          {card && !thread.archived && (
            <Button variant={saving ? "secondary" : "ghost"} size="sm" className="gap-1.5 text-muted-foreground" onClick={() => setSaving((v) => !v)} aria-pressed={saving}>
              <BookmarkPlus className="size-3.5" />
              Save as decision
            </Button>
          )}
          {card && (
            <MoveMenu
              targets={targets.map((t) => ({ id: t.id, title: titleOf(t, projects), color: threadColor(t) }))}
              onMove={(id) => void move(targets.find((t) => t.id === id)!)}
            />
          )}
        </>
      }
    >
      {card && saving && (
        <div className="shrink-0 border-b bg-muted/30 p-3">
          <EntryForm
            initial={{ kind: "decision", text: firstSentence(card.reply ? plain(card.reply) : card.prompt), card: card.id }}
            autoFocus
            onSave={addEntry}
            onDone={(saved) => {
              setSaving(false);
              if (saved) toast.success(`Saved to the log of ${thread.title}.`);
            }}
          />
        </div>
      )}
      {log ? (
        <LogView
          entries={log.entries}
          archived={thread.archived}
          adding={log.adding}
          who={(e) => (e.by === "owner" ? "you" : e.by)}
          onSave={addEntry}
          empty="Nothing logged yet. The log keeps what is worth keeping: decisions, actions, information and links. You, the head and the project's agents add to it; lines are never edited, a correction strikes the old line through."
        />
      ) : card ? (
        <Turn head={head} card={card} />
      ) : (
        <div className="flex flex-1 flex-col justify-end p-5">
          <p className="text-sm text-muted-foreground">
            Your message starts a new card on <span className="font-medium text-foreground">{thread.title}</span>. The head sees which topic you write in and keeps
            its answer there.
          </p>
        </div>
      )}
      <div className={cn("shrink-0 border-t p-3", log && "hidden")}>
        <Composer
          agent={head}
          thread={thread.id}
          autoFocus={!card}
          placeholder={`Write to the head in ${thread.title}…`}
          onSent={() => void loadBoard()}
        />
        {head.state !== "idle" && head.state !== "working" && head.state !== "waiting" && (
          <p className="mt-1.5 text-2xs text-muted-foreground">The head is {head.state}; it reads your message when it runs.</p>
        )}
      </div>
    </SidePanelFrame>
  );
}

function titleOf(t: Thread, projects: Record<string, { name: string }>): string {
  return (t.project_id && projects[t.project_id]?.name) || t.title;
}

/** The entries of one turn, fetched by seq range and kept current while it runs. */
function Turn({ head, card }: { head: Agent; card: BoardCard }) {
  const transport = useStore((s) => s.transport);
  const [entries, setEntries] = useState<TimelineEntry[] | null>(null);
  const [failed, setFailed] = useState(false);
  const scroller = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!transport) return;
    let gone = false;
    transport
      .request<{ entries: TimelineEntry[] | null }>("GET", `/api/agents/${encodeURIComponent(head.id)}/timeline?after=${card.id - 1}&limit=400`)
      .then((r) => {
        if (!gone) setEntries((r.entries ?? []).filter((e) => e.seq <= card.to));
      })
      .catch(() => !gone && setFailed(true));
    return () => {
      gone = true;
    };
  }, [transport, head.id, card.id, card.to]);

  const items = useMemo(() => buildItems(entries ?? []), [entries]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && entries && !card.done) el.scrollTop = el.scrollHeight;
  }, [entries, card.done]);

  return (
    <div ref={scroller} data-scrolls className="scroll-thin min-h-0 flex-1 overflow-y-auto px-5 py-5">
      {failed ? (
        <p className="text-sm text-muted-foreground">
          This turn could not be loaded.{" "}
          <a className="text-brand hover:underline" href={href(paths.agent(head.id))}>
            Open the head's conversation
          </a>
        </p>
      ) : !entries ? (
        <div className="space-y-3">
          <Skeleton className="ml-auto h-12 w-3/4 rounded-xl" />
          <Skeleton className="h-20 w-full rounded-xl" />
        </div>
      ) : (
        items.map((it, i) => (
          <div key={i} className={spacing(items[i - 1], it)}>
            <ItemView item={it} cwd={head.cwd} />
          </div>
        ))
      )}
      {entries && entries.length > 0 && entries[entries.length - 1].seq < card.to && (
        <p className="mt-4 text-xs text-muted-foreground">This turn is long; the rest is in the head's conversation.</p>
      )}
    </div>
  );
}

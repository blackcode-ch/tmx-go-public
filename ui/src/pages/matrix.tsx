// The Matrix (DESIGN.md "Matrix"): the owner's master data view. Three views of the same
// threads: a canvas (one string per project, a card per thread, the Board's sheet), a windowed
// list (thousands scroll smoothly; the phone's view), and a grid (threads x the values of an
// axis). "See by" any axis, and a thread's panel with everything on it and its events (not its
// chatter). The same data the agents read with their matrix tool. Since 3 Nov the head's view (the
// old Board) lives here too: on each thread, the head's turns, its open questions, a composer to
// the head, the thread's log and menu, and the owner's urgency order (components/matrix/head).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Archive, ArchiveRestore, CircleHelp, MessageSquarePlus, MoreHorizontal, NotebookPen, Plus, Search, Waypoints, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Canvas, Knot, LaneFrame } from "@/components/board/canvas";
import { EntryIcon } from "@/components/board/log";
import { NewTopicDialog } from "@/components/board/dialogs";
import { CardView, HeadPanel, QuestionCard, ThreadMenu } from "@/components/matrix/head";
import { PageHeader, PageTitle } from "@/components/page";
import { boardApi, colorValue, loadBoard, THREAD_COLORS, useBoard } from "@/lib/board";
import { relativeTime } from "@/lib/format";
import { cardsByThread, legacyPath, questionsByThread, reorder, withBoard } from "@/lib/matrix-head";
import { ROW_H, visibleRange } from "@/lib/matrix-window";
import { remScale } from "@/lib/text-size";
import { LINK_AXES, MATRIX_EVENTS, matrixApi, statusLabel, type PivotRow, type ThreadCard, type ThreadView } from "@/lib/matrix";
import { navigate, paths } from "@/lib/router";
import { onBusEvent, useStore } from "@/lib/store";
import type { Approval, BoardCard, LinkAxis, ThreadLink } from "@/lib/types";
import { cn } from "@/lib/utils";

type By = "thread" | LinkAxis;
type Look = "canvas" | "list" | "grid";
/** what the head panel shows: a turn, writing to the head in a thread, or a thread's log */
type HeadSel = { card: number } | { compose: string } | { log: string; adding?: boolean } | null;

const LOOK_KEY = "tmx.matrix.look";

function firstLook(): Look {
  const saved = localStorage.getItem(LOOK_KEY);
  if (saved === "canvas" || saved === "list" || saved === "grid") return saved;
  return window.matchMedia("(min-width: 768px)").matches ? "canvas" : "list";
}

export function MatrixPage() {
  const [cards, setCards] = useState<ThreadCard[] | null>(null);
  const [by, setBy] = useState<By>("thread");
  const [pivot, setPivot] = useState<PivotRow[]>([]);
  const [only, setOnly] = useState<{ label: string; ids: Set<string> } | null>(null);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [look, setLookState] = useState<Look>(firstLook);
  const setLook = (l: Look) => {
    setLookState(l);
    localStorage.setItem(LOOK_KEY, l);
  };
  const connected = useStore((s) => s.transport != null);
  const status = useStore((s) => s.status);
  const approvals = useStore((s) => s.approvals);
  const board = useBoard((s) => s.board);
  const head = useStore((s) => (board?.head_id ? s.agents[board.head_id] : s.headId ? s.agents[s.headId] : undefined));
  const [showArchived, setShowArchived] = useState(false);
  const [needsYou, setNeedsYou] = useState(false);
  const [headSel, setHeadSel] = useState<HeadSel>(null);
  const [newTopic, setNewTopic] = useState(false);

  // The Board's old address lands here.
  useEffect(() => {
    const to = legacyPath(window.location.hash.replace(/^#/, ""));
    if (to) history.replaceState(null, "", "#" + to);
  }, []);

  useEffect(() => {
    if (status === "ready") void loadBoard();
  }, [status]);

  const reload = useCallback(async () => {
    try {
      setCards(await matrixApi.mine());
      if (by !== "thread") setPivot(await matrixApi.pivot(by));
    } catch (e) {
      toast.error(`Matrix: ${(e as Error).message}`);
    }
  }, [by]);

  useEffect(() => {
    if (connected) void reload();
  }, [connected, reload]);

  // Live: reload once per burst of thread events.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const off = onBusEvent((ev) => {
      if (!MATRIX_EVENTS.has(ev.type)) return;
      clearTimeout(timer);
      timer = setTimeout(() => void reload(), 300);
    });
    return () => {
      off();
      clearTimeout(timer);
    };
  }, [reload]);

  const ordered = useMemo(() => withBoard(cards ?? [], board, showArchived), [cards, board, showArchived]);
  const questions = useMemo(() => questionsByThread(approvals, board?.head_id ?? head?.id), [approvals, board?.head_id, head?.id]);
  const turns = useMemo(() => cardsByThread(board), [board]);
  const waiting = [...questions.values()].reduce((n, l) => n + l.length, 0);
  const order = ordered.filter((c) => !c.thread.archived).map((c) => c.thread.id);
  const move = (id: string, delta: number) => {
    const ids = reorder(order, id, delta);
    return ids ? () => void boardApi.setOrder(ids).catch((e) => toast.error((e as Error).message)) : undefined;
  };
  const headWorking = head?.state === "working" || head?.state === "starting" || head?.state === "waiting";
  const lastCard = board?.cards.length ? board.cards[board.cards.length - 1].id : -1;

  const selCard = headSel && "card" in headSel ? board?.cards.find((c) => c.id === headSel.card) : undefined;
  const selThreadId = !headSel ? undefined : "card" in headSel ? selCard?.thread_id : "log" in headSel ? headSel.log : headSel.compose;
  const selThread = board?.threads.find((t) => t.id === selThreadId);
  useEffect(() => {
    if (headSel && board && !selThread) setHeadSel(null); // the card or thread is gone
  }, [headSel, board, selThread]);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return ordered.filter(
      (c) =>
        (!needsYou || questions.has(c.thread.id)) &&
        (!only || only.ids.has(c.thread.id)) &&
        (!needle || c.thread.title.toLowerCase().includes(needle) || (c.thread.intent_text ?? "").toLowerCase().includes(needle)),
    );
  }, [ordered, q, only, needsYou, questions]);

  const controls = (
    <>
      <div className="ml-2 flex rounded-md border p-0.5" role="radiogroup" aria-label="View">
        {(["canvas", "list", "grid"] as const).map((l) => (
          <button
            key={l}
            type="button"
            role="radio"
            aria-checked={look === l}
            onClick={() => setLook(l)}
            className={cn("rounded px-2 py-0.5 text-xs capitalize", look === l ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground")}
          >
            {l}
          </button>
        ))}
      </div>
      <label className="ml-2 flex items-center gap-1.5 text-xs text-muted-foreground">
        See by
        <select
          aria-label="See by"
          className="h-7 rounded-md border bg-background px-1.5 text-xs text-foreground"
          value={by}
          onChange={(e) => {
            setBy(e.target.value as By);
            setOnly(null);
          }}
        >
          <option value="thread">threads</option>
          {LINK_AXES.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </select>
      </label>
    </>
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        actions={
          <>
            {waiting > 0 && (
              <Button variant={needsYou ? "secondary" : "ghost"} size="sm" onClick={() => setNeedsYou((v) => !v)} aria-pressed={needsYou} aria-label="Needs you" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-state-waiting" />
                <span className="hidden md:inline">Needs you</span>
                <span className="tabular text-muted-foreground">{waiting}</span>
              </Button>
            )}
            <Button size="sm" onClick={() => setCreating(true)} aria-label="New thread">
              <Plus /> <span className="hidden md:inline">New thread</span>
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label="Matrix options">
                  <MoreHorizontal className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setShowArchived((v) => !v)}>
                  {showArchived ? <Archive /> : <ArchiveRestore />}
                  {showArchived ? "Hide archived threads" : "Show archived threads"}
                </DropdownMenuItem>
                <DropdownMenuItem disabled={!board} onSelect={() => setTimeout(() => setNewTopic(true), 0)}>
                  <Plus /> New topic with the head
                </DropdownMenuItem>
                {head && board && (
                  <DropdownMenuItem onSelect={() => setHeadSel({ compose: "general" })}>
                    <MessageSquarePlus /> Write to the head
                  </DropdownMenuItem>
                )}
                {head && (
                  <DropdownMenuItem onSelect={() => navigate(paths.agent(head.id))}>
                    <Waypoints /> The head's full conversation
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      >
        <PageTitle className="shrink-0">Matrix</PageTitle>
        <div className="hidden items-center sm:flex">{controls}</div>
        <div className="relative ml-2 hidden w-56 sm:block">
          <Search className="absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a thread" className="h-7 pl-7 text-xs" />
        </div>
      </PageHeader>
      <div className="flex flex-wrap items-center gap-y-1.5 border-b px-3 py-1.5 sm:hidden">{controls}</div>

      {creating && <NewThreadForm onDone={() => setCreating(false)} onCreated={() => void reload()} />}

      <div className="relative flex min-h-0 flex-1">
        {by !== "thread" && (
          <PivotList
            axis={by}
            rows={pivot}
            selected={only?.label}
            onPick={(r) => setOnly(only?.label === r.ref ? null : { label: r.ref, ids: new Set(r.threads) })}
          />
        )}
        <div className="flex min-w-0 flex-1 flex-col">
          {only && (
            <div className="flex items-center gap-2 border-b px-4 py-1.5 text-xs text-muted-foreground">
              Threads with {by} <span className="font-medium text-foreground">{only.label}</span>
              <Button variant="ghost" size="icon-xs" aria-label="Show all" onClick={() => setOnly(null)}>
                <X />
              </Button>
            </div>
          )}
          {cards === null ? (
            <p className="p-6 text-sm text-muted-foreground">Loading…</p>
          ) : shown.length === 0 ? (
            <p className="p-6 text-sm text-muted-foreground">{needsYou ? "Nothing waits for you." : "No thread here yet."}</p>
          ) : look === "canvas" ? (
            <ThreadCanvas cards={shown} open={open} onOpen={setOpen} questions={questions} turns={turns} />
          ) : look === "grid" ? (
            <ThreadGrid cards={shown} axis={by === "thread" ? null : by} rows={pivot} open={open} onOpen={setOpen} />
          ) : (
            <ThreadList cards={shown} open={open} onOpen={setOpen} />
          )}
        </div>
        {open && (
          <ThreadPanel
            key={open}
            id={open}
            onClose={() => setOpen(null)}
            head={
              head && board
                ? {
                    cards: turns.get(open) ?? [],
                    questions: questions.get(open) ?? [],
                    selected: selCard?.id ?? null,
                    working: headWorking ? lastCard : -1,
                    known: board.threads.some((t) => t.id === open),
                    onCard: (c) => setHeadSel({ card: c.id }),
                    onCompose: () => setHeadSel({ compose: open }),
                    onLog: (adding) => setHeadSel({ log: open, adding }),
                    onUp: move(open, -1),
                    onDown: move(open, 1),
                  }
                : undefined
            }
          />
        )}
        {headSel && selThread && head && board && (
          <div className="fixed inset-0 z-40 md:contents">
          <HeadPanel
            key={"card" in headSel ? `c${headSel.card}` : "log" in headSel ? `l${headSel.log}${headSel.adding ? "+" : ""}` : `t${headSel.compose}`}
            head={head}
            thread={selThread}
            card={selCard}
            log={"log" in headSel ? { entries: board.entries.filter((e) => e.thread_id === headSel.log), adding: !!headSel.adding } : undefined}
            threads={board.threads}
            onClose={() => setHeadSel(null)}
          />
          </div>
        )}
      </div>
      <NewTopicDialog
        open={newTopic}
        onOpenChange={setNewTopic}
        description="A new thread with the head. What you and the head say in it stays together."
        create={async (title, color) => {
          const t = await boardApi.createThread(title, color);
          setNeedsYou(false);
          void reload();
          setOpen(t.id);
          setHeadSel({ compose: t.id });
        }}
      />
    </div>
  );
}

/** A windowed list: only the rows in view (plus a few) are in the DOM. */
function ThreadList({ cards, open, onOpen }: { cards: ThreadCard[]; open: string | null; onOpen: (id: string) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [top, setTop] = useState(0);
  const [height, setHeight] = useState(600);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setHeight(el.clientHeight));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // Rows are ROW_H at the old text size; they grow with it (lib/text-size.ts).
  const k = remScale();
  const rowH = ROW_H * k;
  const { first, last } = visibleRange(top / k, height / k, cards.length);
  return (
    <div ref={ref} className="scroll-thin min-h-0 flex-1 overflow-y-auto" onScroll={(e) => setTop(e.currentTarget.scrollTop)}>
      <div style={{ height: cards.length * rowH, position: "relative" }}>
        {cards.slice(first, last).map((c, i) => (
          <ThreadRow key={c.thread.id} card={c} top={(first + i) * rowH} height={rowH} active={open === c.thread.id} onOpen={onOpen} />
        ))}
      </div>
    </div>
  );
}

function ThreadRow({ card, top, height, active, onOpen }: { card: ThreadCard; top: number; height: number; active: boolean; onOpen: (id: string) => void }) {
  const th = card.thread;
  const counts = LINK_AXES.filter((a) => (card.counts[a] ?? 0) > 0).map((a) => `${card.counts[a]} ${a}`);
  return (
    <button
      type="button"
      onClick={() => onOpen(th.id)}
      style={{ top, height }}
      className={cn(
        "absolute inset-x-0 flex items-center gap-3 border-b px-4 text-left text-sm hover:bg-accent/60",
        active && "bg-accent",
      )}
    >
      <span className="min-w-0 flex-1 truncate font-medium">{th.title}</span>
      {(card.silent_hours ?? 0) > 0 && (
        <span className="shrink-0 rounded-full border border-destructive px-2 text-2xs text-destructive">
          silent {Math.round((card.silent_hours ?? 0) / 24)}d
        </span>
      )}
      <span className="hidden shrink-0 truncate text-xs text-muted-foreground md:block md:max-w-[22rem]">{counts.join(" · ")}</span>
      <span className="w-28 shrink-0 text-right text-xs text-muted-foreground">
        {statusLabel(th)}
        {th.lifecycle === "ongoing" ? " · ongoing" : ""}
      </span>
    </button>
  );
}

/** The canvas: one string per project (and one for threads under none), a card per thread. */
function ThreadCanvas({
  cards,
  open,
  onOpen,
  questions,
  turns,
}: {
  cards: ThreadCard[];
  open: string | null;
  onOpen: (id: string) => void;
  questions: Map<string, Approval[]>;
  turns: Map<string, BoardCard[]>;
}) {
  const projects = useStore((s) => s.projects);
  const lanes = useMemo(() => {
    const m = new Map<string, ThreadCard[]>();
    for (const c of cards) {
      const key = c.thread.project_id ?? "";
      m.set(key, [...(m.get(key) ?? []), c]);
    }
    return [...m.entries()].sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : 0));
  }, [cards]);
  return (
    <Canvas storageKey="tmx.matrix.view">
      {lanes.map(([pid, list], i) => {
        const title = pid ? (projects[pid]?.name ?? "Project") : "Everything else";
        const color = colorValue(THREAD_COLORS[(i + 1) % THREAD_COLORS.length]);
        return (
          <LaneFrame key={pid || "none"} title={title} color={color} lifted={false} shift={0}>
            <p className="relative z-10 mt-2 rounded-md bg-background px-2 text-xs font-semibold">{title}</p>
            {list.map((c) => (
              <Knot key={c.thread.id}>
                <ThreadCardView card={c} active={open === c.thread.id} onOpen={onOpen} asks={questions.get(c.thread.id)?.length ?? 0} turns={turns.get(c.thread.id)?.length ?? 0} />
              </Knot>
            ))}
          </LaneFrame>
        );
      })}
    </Canvas>
  );
}

function ThreadCardView({ card, active, onOpen, asks, turns }: { card: ThreadCard; active: boolean; onOpen: (id: string) => void; asks: number; turns: number }) {
  const th = card.thread;
  const counts = LINK_AXES.filter((a) => (card.counts[a] ?? 0) > 0).map((a) => `${card.counts[a]} ${a}`);
  return (
    <button
      type="button"
      data-board-item
      onClick={() => onOpen(th.id)}
      className={cn(
        "w-[16.5rem] rounded-lg border bg-card p-3 text-left shadow-sm hover:border-border-strong",
        active && "ring-2 ring-[var(--lane)]",
        th.status === "done" && "opacity-60",
      )}
    >
      <span className="block text-sm font-medium">{th.title}</span>
      {th.intent_text && <span className="mt-1 line-clamp-2 block text-xs text-muted-foreground">“{th.intent_text}”</span>}
      <span className="mt-2 flex flex-wrap items-center gap-x-2 text-2xs text-muted-foreground">
        <span className="text-foreground">{statusLabel(th)}</span>
        {th.lifecycle === "ongoing" && <span>ongoing</span>}
        {(card.silent_hours ?? 0) > 0 && <span className="text-destructive">silent {Math.round((card.silent_hours ?? 0) / 24)}d</span>}
        {card.last_event_at ? <span>{relativeTime(card.last_event_at)}</span> : null}
        {th.archived && <span>archived</span>}
      </span>
      {(asks > 0 || turns > 0) && (
        <span className="mt-1 flex items-center gap-2 text-2xs text-muted-foreground">
          {turns > 0 && <span>{turns} with the head</span>}
          {asks > 0 && (
            <span className="inline-flex items-center gap-1 text-state-waiting">
              <CircleHelp className="size-3" /> {asks} for you
            </span>
          )}
        </span>
      )}
      {counts.length > 0 && <span className="mt-1 block text-2xs text-muted-foreground">{counts.join(" · ")}</span>}
    </button>
  );
}

const GRID_COLS = 24;

/** The grid: a row per thread, a column per value of the "see by" axis, a dot where they meet. */
function ThreadGrid({
  cards,
  axis,
  rows,
  open,
  onOpen,
}: {
  cards: ThreadCard[];
  axis: LinkAxis | null;
  rows: PivotRow[];
  open: string | null;
  onOpen: (id: string) => void;
}) {
  if (!axis) {
    return <p className="p-6 text-sm text-muted-foreground">Pick an axis in "See by" to cross the threads with it.</p>;
  }
  const cols = [...rows].sort((a, b) => b.threads.length - a.threads.length).slice(0, GRID_COLS);
  const on = new Map(cols.map((c) => [c.ref, new Set(c.threads)]));
  return (
    <div className="scroll-thin min-h-0 flex-1 overflow-auto">
      <table className="border-collapse text-xs">
        <thead className="sticky top-0 bg-background">
          <tr>
            <th className="sticky left-0 bg-background px-3 py-2 text-left font-medium">thread ↓ / {axis} →</th>
            {cols.map((c) => (
              <th key={c.ref} className="max-w-[7rem] truncate px-2 py-2 text-left font-normal text-muted-foreground" title={c.ref}>
                {c.label || c.ref}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {cards.map((c) => (
            <tr key={c.thread.id} className={cn("border-t hover:bg-accent/40", open === c.thread.id && "bg-accent")}>
              <td className="sticky left-0 max-w-[16rem] truncate bg-background px-3 py-1.5">
                <button type="button" className="hover:underline" onClick={() => onOpen(c.thread.id)}>
                  {c.thread.title}
                </button>
              </td>
              {cols.map((col) => (
                <td key={col.ref} className="px-2 text-center">
                  {on.get(col.ref)?.has(c.thread.id) ? "●" : ""}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length > GRID_COLS && <p className="p-3 text-xs text-muted-foreground">The {GRID_COLS} most used of {rows.length}; see the side list for all.</p>}
    </div>
  );
}

function PivotList({ axis, rows, selected, onPick }: { axis: LinkAxis; rows: PivotRow[]; selected?: string; onPick: (r: PivotRow) => void }) {
  return (
    <aside className="scroll-thin hidden w-64 shrink-0 overflow-y-auto border-r sm:block" aria-label={`By ${axis}`}>
      {rows.length === 0 && <p className="p-4 text-xs text-muted-foreground">No {axis} on any thread yet.</p>}
      {rows.map((r) => (
        <button
          key={r.ref}
          type="button"
          onClick={() => onPick(r)}
          className={cn("flex w-full items-center gap-2 border-b px-3 py-2 text-left text-xs hover:bg-accent/60", selected === r.ref && "bg-accent")}
        >
          <span className="min-w-0 flex-1 truncate">{r.label || r.ref}</span>
          <span className="shrink-0 text-muted-foreground">{r.threads.length}</span>
        </button>
      ))}
    </aside>
  );
}

/** The head on one thread (the old Board's string): its turns, its open questions, writing to it. */
interface HeadOnThread {
  cards: BoardCard[];
  questions: Approval[];
  selected: number | null;
  working: number;
  /** the head's board knows the thread (else it has no turns, log or menu) */
  known: boolean;
  onCard: (c: BoardCard) => void;
  onCompose: () => void;
  onLog: (adding: boolean) => void;
  onUp?: () => void;
  onDown?: () => void;
}

const HEAD_SHOWN = 6; // turns before "N earlier"

function ThreadPanel({ id, onClose, head }: { id: string; onClose: () => void; head?: HeadOnThread }) {
  const [allTurns, setAllTurns] = useState(false);
  const [v, setV] = useState<ThreadView | null>(null);
  const [all, setAll] = useState(false);
  const load = useCallback(() => {
    matrixApi
      .view(id, { all })
      .then(setV)
      .catch((e: Error) => toast.error(e.message));
  }, [id, all]);
  useEffect(load, [load]);
  useEffect(() => onBusEvent((ev) => MATRIX_EVENTS.has(ev.type) && load()), [load]);

  const byAxis = useMemo(() => {
    const m = new Map<LinkAxis, ThreadLink[]>();
    for (const l of v?.links ?? []) m.set(l.axis, [...(m.get(l.axis) ?? []), l]);
    return m;
  }, [v]);

  if (!v) return <aside className="w-[24rem] shrink-0 border-l p-4 text-sm text-muted-foreground">Loading…</aside>;
  const th = v.thread;
  return (
    <aside className="scroll-thin fixed inset-0 z-30 overflow-y-auto bg-background p-4 md:static md:z-auto md:w-[26rem] md:shrink-0 md:border-l">
      <div className="mb-2 flex items-start gap-2">
        <h2 className="min-w-0 flex-1 text-[0.9375rem] font-semibold">{th.title}</h2>
        {head?.known && <ThreadMenu thread={th} cards={head.cards.length} onUp={head.onUp} onDown={head.onDown} />}
        <Button variant="ghost" size="icon-xs" aria-label="Close" onClick={onClose}>
          <X />
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        {statusLabel(th)}
        {th.lifecycle === "ongoing" ? " · ongoing" : ""} · {th.kind}
      </p>
      {th.intent_text && (
        <blockquote className="mt-3 border-l-2 pl-3 text-sm">
          “{th.intent_text}”{th.intent_source && <span className="block text-xs text-muted-foreground">{th.intent_source}</span>}
        </blockquote>
      )}
      {th.story && <p className="mt-2 text-sm text-muted-foreground">{th.story}</p>}
      <StateButtons id={th.id} status={th.status ?? "active"} />

      {head?.known && (
        <section aria-label="The head" className="mt-5">
          <div className="mb-1 flex items-center gap-2">
            <h3 className="flex-1 text-xs font-semibold tracking-wide text-muted-foreground uppercase">The head</h3>
            <Button variant="ghost" size="xs" className="gap-1 text-muted-foreground" onClick={() => head.onLog(false)}>
              <NotebookPen className="size-3.5" /> Log
            </Button>
          </div>
          <div className="flex flex-col gap-2">
            {head.questions.map((q) => (
              <QuestionCard key={q.id} q={q} />
            ))}
            {!allTurns && head.cards.length > HEAD_SHOWN && (
              <button type="button" className="text-left text-xs text-muted-foreground hover:text-foreground" onClick={() => setAllTurns(true)}>
                {head.cards.length - HEAD_SHOWN} earlier
              </button>
            )}
            {(allTurns ? head.cards : head.cards.slice(-HEAD_SHOWN)).map((c, i) => (
              <CardView key={c.id} card={c} selected={head.selected === c.id} working={head.working === c.id} onOpen={() => head.onCard(c)} first={i === 0} />
            ))}
            {head.cards.length === 0 && head.questions.length === 0 && <p className="text-xs text-muted-foreground">Nothing with the head on this thread yet.</p>}
            {!th.archived && (
              <Button variant="outline" size="sm" className="gap-1.5 self-start" onClick={head.onCompose}>
                <MessageSquarePlus className="size-3.5" />
                {head.cards.length ? "Write to the head here" : "Start with the head"}
              </Button>
            )}
          </div>
        </section>
      )}

      <h3 className="mt-5 mb-1 text-xs font-semibold tracking-wide text-muted-foreground uppercase">On this thread</h3>
      {v.links.length === 0 && <p className="text-xs text-muted-foreground">Nothing attached yet.</p>}
      {LINK_AXES.filter((a) => byAxis.has(a)).map((a) => (
        <div key={a} className="mb-2">
          <p className="text-xs font-medium">{a}</p>
          <ul className="text-xs text-muted-foreground">
            {byAxis.get(a)!.map((l) => (
              <li key={l.id} className="truncate" title={l.why || l.ref}>
                {l.axis === "agent" ? (
                  <a className="text-foreground hover:underline" href={`#/agents/${encodeURIComponent(l.ref)}`}>
                    {l.label || l.ref}
                  </a>
                ) : (
                  <span className="text-foreground">{l.label || l.ref}</span>
                )}
                {l.role ? ` · ${l.role}` : ""}
              </li>
            ))}
          </ul>
        </div>
      ))}

      <div className="mt-5 mb-1 flex items-center gap-2">
        <h3 className="flex-1 text-xs font-semibold tracking-wide text-muted-foreground uppercase">Events</h3>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> info too
        </label>
      </div>
      {v.more ? <p className="text-xs text-muted-foreground">{v.more} earlier</p> : null}
      {v.events.length === 0 && <p className="text-xs text-muted-foreground">No events yet.</p>}
      <ul className="flex flex-col gap-1.5">
        {v.events.map((e) => (
          <li key={e.id} className="flex gap-2 text-sm">
            <EntryIcon kind={e.kind} className="mt-0.5" />
            <span className="min-w-0 flex-1">
              {e.text}
              <span className="block text-2xs text-muted-foreground">
                {e.kind} · {e.by} · {relativeTime(e.at)}
              </span>
            </span>
          </li>
        ))}
      </ul>
    </aside>
  );
}

const STATES = [
  ["active", "Active"],
  ["paused", "Pause"],
  ["blocked", "Blocked"],
  ["done", "Done"],
] as const;

function StateButtons({ id, status }: { id: string; status: string }) {
  return (
    <div className="mt-3 flex flex-wrap gap-1">
      {STATES.map(([s, label]) => (
        <Button
          key={s}
          size="xs"
          variant={status === s ? "secondary" : "outline"}
          disabled={status === s}
          onClick={() => {
            matrixApi.setState(id, { status: s }).catch((e: Error) => toast.error(e.message));
          }}
        >
          {label}
        </Button>
      ))}
    </div>
  );
}

function NewThreadForm({ onDone, onCreated }: { onDone: () => void; onCreated: () => void }) {
  const [title, setTitle] = useState("");
  const [words, setWords] = useState("");
  const [story, setStory] = useState("");
  const [ongoing, setOngoing] = useState(false);
  const [busy, setBusy] = useState(false);
  async function save() {
    setBusy(true);
    try {
      await matrixApi.create({
        title,
        intent_text: words || undefined,
        intent_source: words ? `owner, ${new Date().toLocaleString()}` : undefined,
        story: story || undefined,
        lifecycle: ongoing ? "ongoing" : "task",
      });
      onCreated();
      onDone();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="flex flex-col gap-2 border-b bg-card p-4">
      <Input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What it's about, in a few words" />
      <Textarea value={words} onChange={(e) => setWords(e.target.value)} placeholder="What you want, in your own words" rows={2} />
      <Input value={story} onChange={(e) => setStory(e.target.value)} placeholder="As the owner, … I want … so that …" />
      <div className="flex items-center gap-3">
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <input type="checkbox" checked={ongoing} onChange={(e) => setOngoing(e.target.checked)} /> ongoing (never ends)
        </label>
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={onDone}>
          Cancel
        </Button>
        <Button size="sm" disabled={!title.trim() || busy} onClick={() => void save()}>
          Open thread
        </Button>
      </div>
    </div>
  );
}

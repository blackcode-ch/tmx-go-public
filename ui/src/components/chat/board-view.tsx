// A chat's board (DESIGN.md "Chat boards"): the chat's messages as cards on strings, one string
// per topic, on the same canvas as the head's board. Everyone in the chat shares the topics,
// which message is on which, and the topics' logs; the order of the strings is yours.
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Archive, ArchiveRestore, BookmarkPlus, MessagesSquare, MoreHorizontal, Paperclip, Pencil, Plus, TriangleAlert, Waypoints } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/page";
import { Markdown } from "@/components/markdown";
import { AttachmentList } from "@/components/conversation/attachments";
import { after, Canvas, ColourSubmenu, ComposeKnot, EarlierKnot, Knot, LaneFrame, LaneHeader, laneShift, SHOWN, useLaneOrder, type Drag, type Dragging } from "@/components/board/canvas";
import { NewTopicDialog, RenameDialog } from "@/components/board/dialogs";
import { EntryForm, firstSentence, LogView, type EntryDraft } from "@/components/board/log";
import { MoveMenu, SidePanelFrame } from "@/components/board/side-panel";
import { useNow } from "@/hooks/use-now";
import { formatSize, type AttachedFile } from "@/lib/attachments";
import { threadColor } from "@/lib/board";
import { chatBoardApi, chatLanes, entryBy, loadChatBoard, useChatBoards, type ChatCard, type ChatLane } from "@/lib/chat-board";
import type { Chat, ChatFile, ChatMessage, ChatTopic } from "@/lib/chat-types";
import { CHAT_GENERAL_TOPIC } from "@/lib/chat-types";
import { chatApi, senderName } from "@/lib/chats";
import { absoluteTime, clockTime, relativeTime } from "@/lib/format";
import { withMentionLinks } from "@/lib/mentions";
import { navigate, paths } from "@/lib/router";
import { cn } from "@/lib/utils";
import { DeliveryState, PersonAvatar, SenderBadge } from "./chat-bits";

type Selection = { card: string } | { compose: string } | { log: string; adding?: boolean } | null;

/** The chat's composer, writing in one topic (the chat page passes its own). */
export type TopicComposer = (o: { topic: string; title: string; autoFocus: boolean }) => ReactNode;

export function ChatBoardView({ chat, composer }: { chat: Chat; composer: TopicComposer }) {
  const board = useChatBoards((s) => s.boards[chat.id]);
  const error = useChatBoards((s) => s.errors[chat.id]);
  const [showArchived, setShowArchived] = useState(false);
  const [sel, setSel] = useState<Selection>(null);
  const [newTopic, setNewTopic] = useState(false);
  const active = chat.status === "active";

  useEffect(() => {
    void loadChatBoard(chat.id);
  }, [chat.id]);

  const lanes = useMemo(() => (board ? chatLanes(board, showArchived) : []), [board, showArchived]);
  const cards = useMemo(() => new Map(lanes.flatMap((l) => l.cards.map((c) => [c.id, { card: c, lane: l }] as const))), [lanes]);
  const selCard = sel && "card" in sel ? cards.get(sel.card) : undefined;
  const selTopicId = !sel ? undefined : "card" in sel ? selCard?.lane.topic.id : "log" in sel ? sel.log : sel.compose;
  const selLane = lanes.find((l) => l.topic.id === selTopicId);
  const archived = board?.topics.filter((x) => x.archived).length ?? 0;

  const reorderable = lanes.length > 1;
  const { drag, dragging, saveOrder } = useLaneOrder(
    lanes.map((l) => l.topic.id),
    (ids) => chatBoardApi.setOrder(chat.id, ids).catch((e) => toast.error((e as Error).message)),
  );
  useEffect(() => {
    if (sel && board && !selLane) setSel(null); // the card or topic is gone
  }, [sel, board, selLane]);

  const empty = board && lanes.length <= 1 && !lanes[0]?.cards.length && !lanes[0]?.entries.length;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b px-4">
        <Waypoints className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate text-xs text-muted-foreground">
          <span className="hidden sm:inline">Shared with everyone in {chat.name} · </span>
          {board ? `${board.topics.length - 1 - archived} ${board.topics.length - 1 - archived === 1 ? "topic" : "topics"}` : "Loading…"}
          <span className="hidden md:inline"> · the order of the strings is yours</span>
        </span>
        <div className="ml-auto flex items-center gap-1">
          <Button variant="outline" size="xs" onClick={() => setNewTopic(true)} disabled={!board || !active} className="gap-1">
            <Plus className="size-3.5" />
            Topic
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-xs" aria-label="Board options">
                <MoreHorizontal className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => setShowArchived((v) => !v)} disabled={!archived && !showArchived}>
                {showArchived ? <Archive /> : <ArchiveRestore />}
                {showArchived ? "Hide archived topics" : `Show archived topics${archived ? ` (${archived})` : ""}`}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => navigate(paths.chat(chat.id))}>
                <MessagesSquare /> The whole chat
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        {!board ? (
          <div className="flex flex-1 items-start gap-10 p-10">
            {!error ? (
              [0, 1, 2].map((i) => <Skeleton key={i} className="h-72 w-[18.5rem] rounded-xl" />)
            ) : (
              <EmptyState icon={TriangleAlert} title="The board did not load" className="mx-auto w-full max-w-md">
                {error}
                <div className="mt-4">
                  <Button size="sm" variant="outline" onClick={() => void loadChatBoard(chat.id)}>
                    Try again
                  </Button>
                </div>
              </EmptyState>
            )}
          </div>
        ) : empty ? (
          <div className="flex flex-1 items-center justify-center p-6">
            <EmptyState
              icon={Waypoints}
              title="Nothing on this board yet"
              className="w-full max-w-md"
              actions={
                active && (
                  <>
                    <Button size="sm" onClick={() => setNewTopic(true)}>
                      New topic
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => setSel({ compose: CHAT_GENERAL_TOPIC })}>
                      Write a message
                    </Button>
                  </>
                )
              }
            >
              Open topics to keep the chat in order: everyone in {chat.name} sees the same strings, can move messages between them and keep a short log of what was
              decided. Messages without a topic hang on General.
            </EmptyState>
          </div>
        ) : (
          <Canvas storageKey={`tmx.chatboard.view.${chat.id}`}>
            {lanes.map((l, i) => (
              <TopicLane
                key={l.topic.id}
                chat={chat}
                lane={l}
                index={i}
                count={lanes.length}
                priority={board.ordered ? i + 1 : 0}
                drag={drag}
                dragging={reorderable ? dragging : undefined}
                onMove={reorderable ? (to) => saveOrder(i, to) : undefined}
                selected={selCard?.card.id ?? null}
                composing={sel && "compose" in sel ? sel.compose : null}
                onOpen={(c) => setSel({ card: c.id })}
                onCompose={() => setSel({ compose: l.topic.id })}
                onLog={(adding) => setSel({ log: l.topic.id, adding })}
              />
            ))}
          </Canvas>
        )}
        {sel && selLane && board && (
          <TopicPanel
            key={"card" in sel ? `c${sel.card}` : "log" in sel ? `l${sel.log}${sel.adding ? "+" : ""}` : `t${sel.compose}`}
            chat={chat}
            lane={selLane}
            card={selCard?.card}
            log={"log" in sel ? { adding: !!sel.adding } : undefined}
            topics={board.topics}
            composer={composer}
            onClose={() => setSel(null)}
          />
        )}
      </div>
      <NewTopicDialog
        open={newTopic}
        onOpenChange={setNewTopic}
        description={`A new string on the board of ${chat.name}. Everyone in the chat sees it and can write in it.`}
        create={async (title, color) => {
          const t = await chatBoardApi.createTopic(chat.id, title, color);
          setSel({ compose: t.id });
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// a topic's string

function TopicLane({
  chat,
  lane,
  index,
  count,
  priority,
  drag,
  dragging,
  onMove,
  selected,
  composing,
  onOpen,
  onCompose,
  onLog,
}: {
  chat: Chat;
  lane: ChatLane;
  index: number;
  count: number;
  priority: number;
  drag: Drag | null;
  dragging?: Dragging;
  onMove?: (to: number) => void;
  selected: string | null;
  composing: string | null;
  onOpen: (c: ChatCard) => void;
  onCompose: () => void;
  onLog: (adding: boolean) => void;
}) {
  const { topic, cards } = lane;
  const [all, setAll] = useState(false);
  const hidden = all ? 0 : Math.max(0, cards.length - SHOWN);
  const visible = cards.slice(hidden);
  const { lifted, shift } = laneShift(drag, topic.id, index);
  const older = lane.total - lane.loaded;

  return (
    <LaneFrame title={topic.title} color={threadColor(topic)} lifted={lifted} shift={shift}>
      <TopicHeader
        chat={chat}
        lane={lane}
        priority={priority}
        lifted={lifted}
        onDrag={dragging && { move: (dx) => dragging.move(topic.id, index, dx), end: dragging.end }}
        onMoveLeft={onMove && index > 0 ? () => onMove(index - 1) : undefined}
        onMoveRight={onMove && index < count - 1 ? () => onMove(index + 1) : undefined}
        onLog={onLog}
      />
      {hidden > 0 && <EarlierKnot hidden={hidden} onShow={() => setAll(true)} />}
      {!hidden && older > 0 && (
        <Knot>
          <button
            data-board-item
            onClick={() => navigate(paths.chat(chat.id))}
            className="rounded-full border bg-card px-2.5 py-1 text-2xs text-muted-foreground shadow-xs hover:text-foreground"
          >
            {older} older {older === 1 ? "message is" : "messages are"} in the chat
          </button>
        </Knot>
      )}
      {visible.map((c) => (
        <Knot key={c.id} tilt={tiltOf(c.id)}>
          <MessageCard card={c} selected={selected === c.id} onOpen={() => onOpen(c)} />
        </Knot>
      ))}
      <ComposeKnot
        label={cards.length ? "Write in this topic" : "Start this topic"}
        active={composing === topic.id}
        disabled={!!topic.archived || chat.status !== "active"}
        onClick={onCompose}
      />
    </LaneFrame>
  );
}

function tiltOf(id: string): number {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return (h % 3) - 1;
}

function TopicHeader({
  chat,
  lane,
  priority,
  lifted,
  onDrag,
  onMoveLeft,
  onMoveRight,
  onLog,
}: {
  chat: Chat;
  lane: ChatLane;
  priority: number;
  lifted: boolean;
  onDrag?: { move: (dx: number) => void; end: (commit: boolean) => void };
  onMoveLeft?: () => void;
  onMoveRight?: () => void;
  onLog: (adding: boolean) => void;
}) {
  const { topic, entries, total } = lane;
  const [rename, setRename] = useState(false);
  const general = topic.kind === "general";
  const canChange = chat.status === "active" && !general;

  async function patch(p: { title?: string; color?: string; archived?: boolean }) {
    try {
      await chatBoardApi.patchTopic(chat.id, topic.id, p);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <LaneHeader
      title={topic.title}
      archived={topic.archived}
      entries={entries}
      priority={priority}
      lifted={lifted}
      onDrag={onDrag}
      onMoveLeft={onMoveLeft}
      onMoveRight={onMoveRight}
      onLog={onLog}
      subtitle={
        <>
          {general ? "Everything else" : "Topic"}
          {" · "}
          {total} {total === 1 ? "message" : "messages"}
        </>
      }
      menu={
        <>
          <DropdownMenuItem onSelect={() => navigate(paths.chat(chat.id))}>
            <MessagesSquare /> Open the chat
          </DropdownMenuItem>
          {canChange && (
            <>
              <DropdownMenuItem onSelect={after(() => setRename(true))}>
                <Pencil /> Rename
              </DropdownMenuItem>
              <ColourSubmenu onPick={(color) => void patch({ color })} />
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => void patch({ archived: !topic.archived })}>
                {topic.archived ? <ArchiveRestore /> : <Archive />}
                {topic.archived ? "Restore" : "Archive for everyone"}
              </DropdownMenuItem>
            </>
          )}
        </>
      }
    >
      {canChange && <RenameDialog open={rename} onOpenChange={setRename} title={topic.title} onSave={(title) => patch({ title })} />}
    </LaneHeader>
  );
}

// ---------------------------------------------------------------------------------------------
// cards: a message, or a run of one sender's messages close in time

function MessageCard({ card, selected, onOpen }: { card: ChatCard; selected: boolean; onOpen: () => void }) {
  useNow(30_000);
  const m = card.messages[0];
  const name = senderName(m);
  const files = card.messages.flatMap((x) => x.files ?? []);
  const lastMine = [...card.messages].reverse().find((x) => x.sender === "me");
  return (
    <button
      data-board-item
      onClick={onOpen}
      aria-label={`${name}: ${m.body.slice(0, 80)}`}
      aria-pressed={selected}
      className={cn(
        "group w-full cursor-pointer rounded-xl border bg-card text-left shadow-sm transition-[box-shadow,border-color] duration-150 hover:border-border-strong hover:shadow-md focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:outline-none",
        selected && "border-[var(--lane)] shadow-md ring-2 ring-[var(--lane)]/25",
        m.mentions_me && "shadow-[inset_2px_0_0_var(--brand)]",
      )}
    >
      <div className="px-3.5 pt-3 pb-2.5">
        <div className="flex min-w-0 items-center gap-1.5 text-2xs text-muted-foreground">
          <PersonAvatar name={m.sender === "me" ? "You" : m.sender_name || "?"} size="sm" className={cn(m.sender_agent && "ring-2 ring-brand/40 ring-offset-1 ring-offset-card")} />
          <span className={cn("truncate font-medium", m.sender === "me" && !m.sender_agent ? "text-foreground" : "")}>{name}</span>
          {m.sender_agent && <SenderBadge m={m} />}
          <span className="ml-auto shrink-0 tabular" title={absoluteTime(card.at)}>
            {relativeTime(card.at)}
          </span>
        </div>
        <div className="mt-1.5 space-y-1">
          {card.messages.map((x) => (
            <p key={x.id} className={cn("text-[0.8125rem] leading-snug [overflow-wrap:anywhere] whitespace-pre-line", card.messages.length > 1 ? "line-clamp-2" : "line-clamp-4")}>
              {plainText(x.body) || (x.files?.length ? "(attachment)" : "")}
            </p>
          ))}
        </div>
      </div>
      {(files.length > 0 || lastMine?.state === "queued" || lastMine?.state === "failed") && (
        <div className="flex flex-wrap items-center gap-1 border-t px-3 py-2">
          {files.slice(0, 3).map((f) => (
            <span key={f.id} title={f.name} className="inline-flex max-w-full items-center gap-1 rounded-md bg-muted px-1.5 py-0.5 text-2xs text-muted-foreground">
              <Paperclip className="size-3 shrink-0" />
              <span className="truncate">{f.name}</span>
            </span>
          ))}
          {files.length > 3 && <span className="text-2xs text-muted-foreground">+{files.length - 3}</span>}
          {lastMine && (lastMine.state === "queued" || lastMine.state === "failed") && (
            <span className="ml-auto">
              <DeliveryState m={lastMine} />
            </span>
          )}
        </div>
      )}
    </button>
  );
}

/** Markdown to a one-line-ish preview (cards). */
function plainText(md: string): string {
  return md
    .replace(/```[\s\S]*?```/g, " [code] ")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/\*\*([^*]+)\*\*|__([^_]+)__/g, "$1$2")
    .replace(/(^|\s)[*_]([^*_\n]+)[*_]/g, "$1$2")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^#+\s*/gm, "")
    .trim();
}

// ---------------------------------------------------------------------------------------------
// the side panel: a card's messages in full, a topic's log, or writing in a topic

function TopicPanel({
  chat,
  lane,
  card,
  log,
  topics,
  composer,
  onClose,
}: {
  chat: Chat;
  lane: ChatLane;
  card?: ChatCard;
  log?: { adding: boolean };
  topics: ChatTopic[];
  composer: TopicComposer;
  onClose: () => void;
}) {
  const { topic } = lane;
  const active = chat.status === "active";
  const [saving, setSaving] = useState(false);
  const targets = topics.filter((x) => !x.archived && x.id !== topic.id);
  const addEntry = (e: EntryDraft) => chatBoardApi.addEntry(chat.id, { topic: topic.id, kind: e.kind, text: e.text, link: e.link, corrects: e.corrects });

  async function move(to: string) {
    if (!card) return;
    try {
      await chatBoardApi.move(
        chat.id,
        card.messages.map((m) => m.id),
        to,
      );
      toast.success(`Moved to ${topics.find((x) => x.id === to)?.title ?? "General"} for everyone in ${chat.name}.`);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <SidePanelFrame
      color={threadColor(topic)}
      title={topic.title}
      sub={log ? "Log" : undefined}
      label={card ? "Card" : log ? `Log of ${topic.title}` : `Write in ${topic.title}`}
      onClose={onClose}
      actions={
        <>
          {card && active && !topic.archived && (
            <Button variant={saving ? "secondary" : "ghost"} size="sm" className="gap-1.5 text-muted-foreground" onClick={() => setSaving((v) => !v)} aria-pressed={saving}>
              <BookmarkPlus className="size-3.5" />
              <span className="hidden sm:inline">Save as decision</span>
              <span className="sm:hidden">Save</span>
            </Button>
          )}
          {card && active && <MoveMenu targets={targets.map((x) => ({ id: x.id, title: x.title, color: threadColor(x) }))} onMove={(id) => void move(id)} />}
        </>
      }
    >
      {card && saving && (
        <div className="shrink-0 border-b bg-muted/30 p-3">
          <EntryForm
            initial={{ kind: "decision", text: firstSentence(plainText(card.messages.map((m) => m.body).join(" "))) }}
            autoFocus
            cancelable
            onSave={addEntry}
            onDone={(saved) => {
              setSaving(false);
              if (saved) toast.success(`Saved to the log of ${topic.title}.`);
            }}
          />
        </div>
      )}
      {log ? (
        <LogView
          entries={lane.entries}
          archived={topic.archived || !active}
          adding={log.adding}
          who={entryBy}
          onSave={addEntry}
          empty={`Nothing logged yet. Everyone in ${chat.name} sees this log and can add to it: decisions, actions, information and links. Lines are never edited; a correction strikes the old line through.`}
        />
      ) : card ? (
        <div data-scrolls className="scroll-thin min-h-0 flex-1 overflow-y-auto px-5 py-4">
          {card.messages.map((m, i) => (
            <PanelMessage key={m.id} chat={chat} m={m} first={i === 0} />
          ))}
        </div>
      ) : (
        <div className="flex flex-1 flex-col justify-end p-5">
          <p className="text-sm text-muted-foreground">
            Your message goes to everyone in {chat.name}, on <span className="font-medium text-foreground">{topic.title}</span>.
          </p>
        </div>
      )}
      {active && !topic.archived && <div className={cn("shrink-0 border-t pt-3", log && "hidden")}>{composer({ topic: topic.id, title: topic.title, autoFocus: !card })}</div>}
    </SidePanelFrame>
  );
}

function PanelMessage({ chat, m, first }: { chat: Chat; m: ChatMessage; first: boolean }) {
  return (
    <div className={cn("flex gap-3", !first && "mt-3")}>
      <PersonAvatar name={m.sender === "me" ? "You" : m.sender_name || "?"} className={cn(m.sender_agent && "ring-2 ring-brand/40 ring-offset-1 ring-offset-background")} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pb-0.5">
          <span className="text-[0.8125rem] font-semibold">{senderName(m)}</span>
          <SenderBadge m={m} />
          <span className="tabular text-2xs text-subtle-foreground" title={absoluteTime(m.at)}>
            {clockTime(m.received_at || m.at)}
          </span>
          {m.sender === "me" && <DeliveryState m={m} />}
        </div>
        {m.body.trim() && <Markdown text={withMentionLinks(chat, m)} className="text-[0.8438rem] leading-relaxed [overflow-wrap:anywhere]" />}
        {m.files && m.files.length > 0 && <AttachmentList files={m.files.map(asAttached)} loadImage={loaderFor(chat.id)} className="justify-start pt-1.5" />}
      </div>
    </div>
  );
}

// Images of chat files come through the API as base64; cached per file.
const images = new Map<string, Promise<string>>();
const loaders = new Map<string, (id: string) => Promise<string>>();
function loaderFor(chatId: string) {
  let l = loaders.get(chatId);
  if (!l) {
    l = (id: string) => {
      const key = chatId + "/" + id;
      let p = images.get(key);
      if (!p) {
        p = chatApi.file(chatId, id, true).then((f) => {
          const bin = atob(f.data ?? "");
          const bytes = new Uint8Array(bin.length);
          for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
          return URL.createObjectURL(new Blob([bytes], { type: f.type }));
        });
        p.catch(() => images.delete(key));
        images.set(key, p);
      }
      return p;
    };
    loaders.set(chatId, l);
  }
  return l;
}

function asAttached(f: ChatFile): AttachedFile {
  return { id: f.id, name: f.name, type: f.type, size: formatSize(f.size), path: f.path ?? "", image: !!f.image, state: f.state, error: f.error };
}

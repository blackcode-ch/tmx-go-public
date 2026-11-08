// One chat with other people's tmx: a calm message list (who wrote it: a person or their
// agent; delivery state; time), the composer, the agent taking part, and the members /
// contact panel.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { PinMessageButton } from "@/components/message-pins";
import { ArrowUp, AtSign, Bot, Clock3, Download, Ellipsis, Info, Loader2, LogOut, MessagesSquare, PanelRight, QrCode, Trash2, Users, Waypoints } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ChatAvatar, DeliveryState, PersonAvatar, ReachDot, reachLabel, SenderBadge } from "@/components/chat/chat-bits";
import { AgentPicker, ContactPanel, MembersPanel } from "@/components/chat/panels";
import { TopicChip, TopicPicker, useTopicChoice } from "@/components/chat/board-bits";
import { ChatBoardView } from "@/components/chat/board-view";
import { Markdown } from "@/components/markdown";
import { AttachButton, AttachChips, DropOverlay, useAttachments } from "@/components/attach";
import { DictateButton } from "@/components/dictate-button";
import { AttachmentList } from "@/components/conversation/attachments";
import { formatSize, type AttachedFile } from "@/lib/attachments";
import { matchMentionables, mentionQuery, withMentionLinks } from "@/lib/mentions";
import { EmptyState, PageHeader } from "@/components/page";
import { useMedia } from "@/hooks/use-media";
import { useNow } from "@/hooks/use-now";
import { absoluteTime, clockTime } from "@/lib/format";
import type { Chat, ChatFile, ChatMessage, Mentionable } from "@/lib/chat-types";
import { CHAT_MAX_FILE_BYTES, CHAT_MAX_FILES, CHAT_SYSTEM, currentMembers } from "@/lib/chat-types";
import { chatApi, chatUI, loadEarlierMessages, loadMessages, senderName, useChats } from "@/lib/chats";
import { href, navigate, paths } from "@/lib/router";
import { cn } from "@/lib/utils";

const drafts = new Map<string, string>();
const PANEL_KEY = "tmx.chat.panel";

function dayLabel(ms: number): string {
  const d = new Date(ms);
  const today = new Date();
  const y = new Date();
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
}

function sameAuthor(a: ChatMessage, b: ChatMessage): boolean {
  return a.sender === b.sender && a.sender_agent === b.sender_agent && a.sender !== CHAT_SYSTEM && b.received_at - a.received_at < 5 * 60_000;
}

// Chat images come through the API as base64 (every transport carries that); cached per file.
const chatImages = new Map<string, Promise<string>>();

function chatImageLoader(chatId: string) {
  return (id: string) => {
    const key = chatId + "/" + id;
    let p = chatImages.get(key);
    if (!p) {
      p = chatApi.file(chatId, id, true).then((f) => {
        const bin = atob(f.data ?? "");
        const bytes = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        return URL.createObjectURL(new Blob([bytes], { type: f.type }));
      });
      p.catch(() => chatImages.delete(key));
      chatImages.set(key, p);
    }
    return p;
  };
}
const loaders = new Map<string, (id: string) => Promise<string>>();
function loaderFor(chatId: string) {
  let l = loaders.get(chatId);
  if (!l) loaders.set(chatId, (l = chatImageLoader(chatId)));
  return l;
}

function asAttached(f: ChatFile): AttachedFile {
  return { id: f.id, name: f.name, type: f.type, size: formatSize(f.size), path: f.path ?? "", image: !!f.image, state: f.state, error: f.error };
}

function DownloadChatFile({ chatId, file }: { chatId: string; file: ChatFile }) {
  const [busy, setBusy] = useState(false);
  async function download() {
    setBusy(true);
    try {
      const nativeSave = window.go?.desktop?.App?.SaveChatFile;
      if (nativeSave) {
        await nativeSave(chatId, file.id);
        return;
      }
      const f = await chatApi.file(chatId, file.id, true);
      const bin = atob(f.data ?? "");
      const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
      const url = URL.createObjectURL(new Blob([bytes], { type: "application/octet-stream" }));
      const link = document.createElement("a");
      link.href = url;
      link.download = f.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not download file");
    } finally {
      setBusy(false);
    }
  }
  return <Button variant="ghost" size="sm" disabled={busy || file.state !== "ready"} onClick={download} aria-label={`Download ${file.name}`}>
    {busy ? <Loader2 className="size-3 animate-spin" /> : <Download className="size-3" />}<span className="max-w-48 truncate">{file.name}</span>
  </Button>;
}

function MessageRow({ chat, m, first }: { chat: Chat; m: ChatMessage; first: boolean }) {
  if (m.sender === CHAT_SYSTEM) {
    return (
      <div className="flex justify-center py-2">
        <span className="max-w-[80%] rounded-full bg-muted/70 px-3 py-1 text-center text-xs text-muted-foreground">{m.body}</span>
      </div>
    );
  }
  const mine = m.sender === "me";
  const name = senderName(m);
  const t = m.received_at || m.at;
  return (
    <div
      className={cn(
        "group/msg relative flex gap-3 rounded-lg px-2 py-0.5 hover:bg-muted/35",
        first && "mt-3 pt-1.5",
        m.mentions_me && "bg-brand-soft hover:bg-brand-soft shadow-[inset_2px_0_0_var(--brand)]",
      )}
    >
      <div className="w-7 shrink-0">
        {first && <PersonAvatar name={mine ? "You" : m.sender_name || "?"} className={cn(m.sender_agent && "ring-2 ring-brand/40 ring-offset-1 ring-offset-background")} />}
      </div>
      <div className="min-w-0 flex-1">
        <PinMessageButton selection={{ source: `chat:${chat.id}:${m.id}`, label: chat.name, href: href(paths.chat(chat.id)), agent_id: chat.agent_id, text: m.body || m.files?.map((f) => f.name).join(", ") || "Attached files", message_at: m.at }} />
        {first && (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pb-0.5">
            <span className="text-[0.8125rem] font-semibold">{name}</span>
            <SenderBadge m={m} />
            <span className="tabular text-2xs text-subtle-foreground" title={absoluteTime(m.at)}>
              {clockTime(t)}
            </span>
            <TopicChip chatId={chat.id} topic={m.topic} />
          </div>
        )}
        <div className="flex items-end gap-2">
          {m.body.trim() ? (
            <Markdown text={withMentionLinks(chat, m)} className="min-w-0 flex-1 text-[0.8438rem] leading-relaxed [overflow-wrap:anywhere]" />
          ) : (
            <span className="flex-1" />
          )}
          <span className="flex shrink-0 items-center gap-1.5 pb-0.5">
            {!first && (
              <span className="tabular hidden text-2xs text-subtle-foreground group-hover/msg:inline" title={absoluteTime(m.at)}>
                {clockTime(t)}
              </span>
            )}
            {mine && <DeliveryState m={m} />}
          </span>
        </div>
        {m.files && m.files.length > 0 && (
          <div>
            <AttachmentList files={m.files.map(asAttached)} loadImage={loaderFor(chat.id)} className="justify-start pt-1.5 pb-1" />
            <div className="flex flex-wrap gap-1">{m.files.map((file) => <DownloadChatFile key={file.id} chatId={chat.id} file={file} />)}</div>
          </div>
        )}
      </div>
    </div>
  );
}

function Banner({ chat }: { chat: Chat }) {
  const admin = currentMembers(chat).find((m) => m.role === "admin");
  let text = "";
  if (chat.status === "pending") text = `Joining: waiting for ${admin?.name ?? "the admin"}'s tmx to send the member list.`;
  else if (chat.status === "left") text = chat.kind === "group" ? "You left this group. Nothing more arrives here." : "This chat is over.";
  else if (chat.status === "removed")
    text = chat.kind === "dm" ? "You removed this contact. Send a new invite to talk again." : "You're no longer in this group.";
  if (!text) return null;
  return (
    <div className="flex items-center justify-center gap-2 border-b bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
      {chat.status === "pending" && <Loader2 className="size-3.5 animate-spin" />}
      {text}
    </div>
  );
}

/** The chat's composer. On the board it writes in one topic (topic set); in the chat a picker
 * chooses the topic. */
function ChatComposer({ chat, topic: fixedTopic, placeholder, autoFocus }: { chat: Chat; topic?: string; placeholder?: string; autoFocus?: boolean }) {
  const [pickedTopic, setPickedTopic] = useTopicChoice(chat.id);
  const topic = fixedTopic ?? pickedTopic;
  const [text, setText] = useState(() => drafts.get(chat.id) ?? "");
  const [sending, setSending] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const contact = useChats((s) => (chat.peer_id ? s.contacts[chat.peer_id] : undefined));
  const active = chat.status === "active";
  const queuedCount = useChats((s) => s.messages[chat.id]?.list.filter((m) => m.state === "queued").length ?? 0);
  const att = useAttachments("chat:" + chat.id, (f) => chatApi.uploadFile(chat.id, f), {
    maxFiles: CHAT_MAX_FILES,
    maxBytes: CHAT_MAX_FILE_BYTES,
    disabled: !active,
  });

  // @mentions: who can be mentioned here, and the suggestion list while typing "@…".
  const [people, setPeople] = useState<Mentionable[]>([]);
  const [mq, setMq] = useState<{ start: number; query: string } | null>(null);
  const [pick, setPick] = useState(0);
  const memberKey = (chat.members ?? []).map((m) => m.peer_id + m.left_at).join(",") + chat.agent_id;
  useEffect(() => {
    let live = true;
    chatApi.mentionable(chat.id).then(
      (r) => live && setPeople(r.mentionable ?? []),
      () => live && setPeople([]),
    );
    return () => {
      live = false;
    };
  }, [chat.id, memberKey]);
  const hits = mq ? matchMentionables(people, mq.query) : [];
  const open = hits.length > 0;

  function track(value: string, caret: number) {
    const q = mentionQuery(value, caret);
    setMq(q);
    setPick(0);
  }

  function insertMention(x: Mentionable) {
    const el = ta.current;
    if (!el || !mq) return;
    const caret = el.selectionStart;
    const next = text.slice(0, mq.start) + "@" + x.label + " " + text.slice(caret);
    const pos = mq.start + x.label.length + 2;
    setText(next);
    drafts.set(chat.id, next);
    setMq(null);
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(pos, pos);
    });
  }

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 200) + "px";
  }, [text]);

  const canSend = active && !sending && !att.uploading && (!!text.trim() || att.readyIds.length > 0);

  async function send() {
    const body = text.trim();
    if (!canSend) return;
    setSending(true);
    try {
      await chatApi.send(chat.id, body, att.readyIds, topic === "general" ? "" : topic);
      setText("");
      drafts.delete(chat.id);
      att.clear();
      setMq(null);
    } catch (e) {
      toast.error((e as Error).message || "The message wasn't sent.");
    } finally {
      setSending(false);
      ta.current?.focus();
    }
  }

  const offline = chat.kind === "dm" && contact && contact.reachability === "offline";
  const hint = !active
    ? ""
    : queuedCount > 0
      ? `${queuedCount} waiting to go out · ${
          chat.kind === "dm"
            ? `delivered when ${chat.name} is back online`
            : chat.admin
              ? "delivered when the offline members are back"
              : "delivered when the group's admin is back online"
        }`
      : offline
        ? `${chat.name} is offline · messages wait and go out when they're back`
        : chat.agent_name
          ? `Everyone here reads it · your ${chat.agent_name} takes part · @ to mention`
          : "Everyone here reads it · @ to mention · Enter to send";

  return (
    <div className={cn("shrink-0", fixedTopic === undefined ? "px-4 pt-2 pb-4 md:px-6" : "px-3 pb-3")}>
      <div className="mx-auto max-w-[51.25rem]">
        <div
          className={cn(
            "relative rounded-xl border border-border-strong bg-card transition-[border-color,box-shadow] duration-150 focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/25",
            att.dragging && "border-ring ring-2 ring-ring/25",
            !active && "opacity-60",
          )}
          {...att.dropProps}
        >
          <DropOverlay show={att.dragging} />
          {open && (
            <div
              role="listbox"
              aria-label="Mention someone"
              className="absolute bottom-full left-2 z-20 mb-1.5 w-72 max-w-[calc(100%-1rem)] overflow-hidden rounded-xl border bg-popover p-1 text-popover-foreground shadow-lg"
            >
              {hits.map((x, i) => (
                <button
                  key={x.peer_id + "/" + (x.agent ?? "")}
                  type="button"
                  role="option"
                  aria-selected={i === pick}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    insertMention(x);
                  }}
                  onMouseEnter={() => setPick(i)}
                  className={cn(
                    "flex w-full min-w-0 items-center gap-2.5 rounded-lg px-2 py-1.5 text-left text-sm outline-none",
                    i === pick && "bg-accent text-accent-foreground",
                  )}
                >
                  {x.kind === "person" ? (
                    <PersonAvatar name={x.label} size="sm" />
                  ) : (
                    <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-brand-soft text-brand">
                      <Bot className="size-3" />
                    </span>
                  )}
                  <span className="min-w-0 flex-1 truncate font-medium">{x.label}</span>
                  <span className="shrink-0 text-2xs text-subtle-foreground">
                    {x.kind === "person" ? "person" : x.mine ? "your agent" : "their agent"}
                  </span>
                </button>
              ))}
            </div>
          )}
          <AttachChips files={att.files} onRemove={att.remove} />
          <textarea
            ref={ta}
            value={text}
            rows={1}
            disabled={!active}
            onChange={(e) => {
              setText(e.target.value);
              drafts.set(chat.id, e.target.value);
              track(e.target.value, e.target.selectionStart);
            }}
            onSelect={(e) => track(e.currentTarget.value, e.currentTarget.selectionStart)}
            onBlur={() => setMq(null)}
            onPaste={(e) => {
              if (!active || !e.clipboardData.files.length) return;
              e.preventDefault();
              att.add(e.clipboardData.files);
            }}
            onKeyDown={(e) => {
              if (open) {
                if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                  e.preventDefault();
                  setPick((i) => (i + (e.key === "ArrowDown" ? 1 : hits.length - 1)) % hits.length);
                  return;
                }
                if ((e.key === "Enter" || e.key === "Tab") && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  insertMention(hits[Math.min(pick, hits.length - 1)]);
                  return;
                }
                if (e.key === "Escape") {
                  e.preventDefault();
                  setMq(null);
                  return;
                }
              }
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void send();
              }
            }}
            autoFocus={autoFocus}
            placeholder={active ? (placeholder ?? `Message ${chat.name}…`) : "You can't write here any more"}
            aria-label={`Message ${chat.name}`}
            aria-autocomplete="list"
            aria-expanded={open}
            className="scroll-thin block max-h-52 w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-sm leading-relaxed outline-none placeholder:text-subtle-foreground disabled:cursor-not-allowed"
          />
          <div className="flex items-center gap-1 px-2 pb-2">
            <AttachButton
              disabled={!active}
              hint="Attach files (up to 25 MB) · or paste / drop them here"
              onFiles={(f) => {
                att.add(f);
                ta.current?.focus();
              }}
            />
            <DictateButton
              target={ta}
              value={text}
              draft={{ id: "chat:" + chat.id, label: chat.name, path: paths.chat(chat.id), drafts, draftKey: chat.id }}
              disabled={!active}
              onChange={(v) => {
                setText(v);
                drafts.set(chat.id, v);
              }}
            />
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  disabled={!active || people.length === 0}
                  aria-label="Mention someone"
                  className="shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
                  onClick={() => {
                    const el = ta.current;
                    if (!el) return;
                    const caret = el.selectionStart;
                    const pre = text.slice(0, caret);
                    const ins = (pre && !/\s$/.test(pre) ? " " : "") + "@";
                    const next = pre + ins + text.slice(caret);
                    setText(next);
                    drafts.set(chat.id, next);
                    const pos = caret + ins.length;
                    setMq({ start: pos - 1, query: "" });
                    setPick(0);
                    requestAnimationFrame(() => {
                      el.focus();
                      el.setSelectionRange(pos, pos);
                    });
                  }}
                >
                  <AtSign className="size-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Mention someone · or type @</TooltipContent>
            </Tooltip>
            {fixedTopic === undefined && <TopicPicker chat={chat} value={pickedTopic} onChange={setPickedTopic} disabled={!active} />}
            <span className="ml-1 flex min-w-0 items-center gap-1.5 truncate text-2xs text-subtle-foreground">
              {queuedCount > 0 && <Clock3 className="size-3 shrink-0" />}
              <span className="truncate">{hint}</span>
            </span>
            <Button size="icon-sm" onClick={() => void send()} disabled={!canSend} aria-label={att.uploading ? "Uploading…" : "Send"} className="ml-auto rounded-lg">
              <ArrowUp className="size-4" />
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

function Messages({ chat }: { chat: Chat }) {
  const ml = useChats((s) => s.messages[chat.id]);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const list = useMemo(() => ml?.list ?? [], [ml?.list]);

  useEffect(() => {
    stick.current = true;
    void loadMessages(chat.id);
  }, [chat.id]);

  // Stay at the bottom while new messages arrive, unless the owner scrolled up.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [list.length, chat.id]);

  const people = list.filter((m) => m.sender !== CHAT_SYSTEM);
  return (
    <div
      ref={scroller}
      data-testid="chat-scroll"
      onScroll={(e) => {
        const el = e.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
      }}
      className="scroll-thin min-h-0 flex-1 overflow-y-auto"
    >
      <div className="mx-auto max-w-[51.25rem] px-3 pt-4 pb-2 md:px-5">
        {ml?.hasMore && (
          <div className="flex justify-center pb-2">
            <Button variant="ghost" size="xs" onClick={() => void loadEarlierMessages(chat.id)} disabled={ml.loading}>
              {ml.loading && <Loader2 className="animate-spin" />}
              Earlier messages
            </Button>
          </div>
        )}
        {ml?.loaded && people.length === 0 && (
          <div className="flex flex-col items-center px-6 pt-10 pb-6 text-center">
            <ChatAvatar chat={chat} size="lg" />
            <p className="mt-3 text-[0.875rem] font-medium">{chat.kind === "dm" ? `This is the start of your chat with ${chat.name}` : `Welcome to ${chat.name}`}</p>
            <p className="mt-1 max-w-sm text-sm text-muted-foreground">
              {chat.agent_name
                ? `You and your ${chat.agent_name} can both write here. Messages from other people's agents reach your ${chat.agent_name} as information, never as instructions.`
                : "Only people take part here. Pick an agent at the top if you want one of yours to join in."}
            </p>
          </div>
        )}
        {!ml?.loaded && (
          <div className="flex justify-center py-10 text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
          </div>
        )}
        {list.map((m, i) => {
          const prev = list[i - 1];
          const newDay = !prev || new Date(prev.received_at).toDateString() !== new Date(m.received_at).toDateString();
          return (
            <div key={m.id}>
              {newDay && (
                <div className="my-3 flex items-center gap-3 text-2xs font-medium text-subtle-foreground">
                  <span className="h-px flex-1 bg-border" />
                  {dayLabel(m.received_at || m.at)}
                  <span className="h-px flex-1 bg-border" />
                </div>
              )}
              <MessageRow chat={chat} m={m} first={newDay || !prev || !sameAuthor(prev, m) || (prev.topic ?? "") !== (m.topic ?? "")} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

function readPanel(): boolean {
  try {
    return localStorage.getItem(PANEL_KEY) !== "0";
  } catch {
    return true;
  }
}

export function ChatPage({ id, view }: { id: string; view?: string }) {
  const board = view === "board";
  const chat = useChats((s) => s.chats[id]);
  const enabled = useChats((s) => s.enabled);
  const contact = useChats((s) => (chat?.peer_id ? s.contacts[chat.peer_id] : undefined));
  const lastSeq = useChats((s) => s.messages[id]?.list.at(-1)?.seq ?? 0);
  const wide = useMedia("(min-width: 1280px)");
  const [panel, setPanel] = useState(readPanel);
  const [sheet, setSheet] = useState(false);
  useNow(60_000);

  // Reading the chat marks it read (while the window is visible).
  useEffect(() => {
    if (!chat || chat.unread === 0 || document.visibilityState !== "visible") return;
    const t = setTimeout(() => void chatApi.read(chat.id).catch(() => undefined), 400);
    return () => clearTimeout(t);
  }, [chat?.id, chat?.unread, lastSeq]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!chat) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <EmptyState icon={MessagesSquare} title={enabled === null ? "Loading…" : "This chat isn't here"} className="w-full max-w-md border-none bg-transparent">
          {enabled !== null && (
            <>
              It may have been deleted.
              <div className="mt-4">
                <Button variant="outline" size="sm" asChild>
                  <a href={href(paths.chats())}>All chats</a>
                </Button>
              </div>
            </>
          )}
        </EmptyState>
      </div>
    );
  }

  const members = currentMembers(chat);
  const over = chat.status === "left" || chat.status === "removed";
  const sub =
    chat.kind === "dm" ? (
      <span className="flex items-center gap-1.5">
        <ReachDot r={contact?.reachability} />
        {reachLabel(contact?.reachability)}
      </span>
    ) : (
      <span>
        {members.length} {members.length === 1 ? "member" : "members"}
      </span>
    );
  const togglePanel = () => {
    if (!wide || board) return setSheet(true);
    setPanel((v) => {
      try {
        localStorage.setItem(PANEL_KEY, v ? "0" : "1");
      } catch {
        /* ignore */
      }
      return !v;
    });
  };
  const panelBody = chat.kind === "dm" ? <ContactPanel contact={contact} chat={chat} /> : <MembersPanel chat={chat} />;

  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <PageHeader
          actions={
            <>
              <ViewSwitch chat={chat} board={board} />
              <AgentPicker chat={chat} className="hidden sm:flex" />
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="ghost" size="icon-sm" className="text-muted-foreground" aria-label={chat.kind === "dm" ? "Contact details" : "Members"} onClick={togglePanel}>
                    {chat.kind === "dm" ? <Info className="size-4" /> : wide ? <PanelRight className="size-4" /> : <Users className="size-4" />}
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{chat.kind === "dm" ? "Contact details" : "Members"}</TooltipContent>
              </Tooltip>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon-sm" aria-label="Chat actions" className="text-muted-foreground">
                    <Ellipsis className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-52">
                  {chat.kind === "group" && chat.admin && !over && (
                    <DropdownMenuItem onSelect={() => chatUI.openInvite("group", chat.id)}>
                      <QrCode /> Invite people
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem onSelect={() => (wide && !board ? setPanel(true) : setSheet(true))}>
                    {chat.kind === "dm" ? <Info /> : <Users />} {chat.kind === "dm" ? "Contact details" : "Members"}
                  </DropdownMenuItem>
                  {chat.kind === "group" && !over && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" onSelect={() => (wide ? setPanel(true) : setSheet(true))}>
                        <LogOut /> Leave the group…
                      </DropdownMenuItem>
                    </>
                  )}
                  {over && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        variant="destructive"
                        onSelect={async () => {
                          try {
                            await chatApi.deleteChat(chat.id);
                            navigate(paths.chats());
                          } catch (e) {
                            toast.error((e as Error).message);
                          }
                        }}
                      >
                        <Trash2 /> Delete the history
                      </DropdownMenuItem>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          }
        >
          <ChatAvatar chat={chat} />
          <div className="min-w-0">
            <h1 className="truncate text-[0.875rem] leading-tight font-semibold tracking-tight">{chat.name}</h1>
            <div className="truncate text-2xs leading-tight text-muted-foreground">{sub}</div>
          </div>
        </PageHeader>
        <div className="flex items-center gap-2 border-b px-4 py-1.5 sm:hidden">
          <span className="text-2xs text-muted-foreground">Takes part:</span>
          <AgentPicker chat={chat} className="h-7" />
        </div>
        <Banner chat={chat} />
        {board ? (
          <ChatBoardView
            chat={chat}
            composer={({ topic, title, autoFocus }) => (
              <ChatComposer key={chat.id + "#" + topic} chat={chat} topic={topic} autoFocus={autoFocus} placeholder={`Write in ${title}…`} />
            )}
          />
        ) : (
          <>
            <Messages chat={chat} />
            <ChatComposer key={chat.id} chat={chat} />
          </>
        )}
      </div>
      {wide && panel && !board && <aside className="scroll-thin w-[18.75rem] shrink-0 overflow-y-auto border-l bg-sidebar/40">{panelBody}</aside>}
      {(!wide || board) && (
        <Sheet open={sheet} onOpenChange={setSheet}>
          <SheetContent side="right" className="w-[20rem] gap-0 overflow-y-auto p-0 sm:max-w-[20rem]">
            <SheetTitle className="sr-only">{chat.kind === "dm" ? "Contact details" : "Members"}</SheetTitle>
            <SheetDescription className="sr-only">{chat.name}</SheetDescription>
            {panelBody}
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}

/** Chat | Board: the chat's messages as a list, or on its board by topic. */
function ViewSwitch({ chat, board }: { chat: Chat; board: boolean }) {
  const item = "flex h-7 items-center gap-1.5 rounded-[7px] px-2 text-xs font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring";
  return (
    <div role="tablist" aria-label="View" className="flex items-center rounded-lg border bg-muted/40 p-0.5">
      <a
        role="tab"
        aria-selected={!board}
        href={href(paths.chat(chat.id))}
        className={cn(item, !board ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}
      >
        <MessagesSquare className="size-3.5" />
        <span className="hidden sm:inline">Chat</span>
      </a>
      <a
        role="tab"
        aria-selected={board}
        href={href(paths.chatBoard(chat.id))}
        className={cn(item, board ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}
      >
        <Waypoints className="size-3.5" />
        <span className="hidden sm:inline">Board</span>
      </a>
    </div>
  );
}

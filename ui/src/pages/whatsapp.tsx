// WhatsApp: #/whatsapp lists the chats (or shows the linking flow while nothing is linked);
// #/whatsapp/<jid> is one chat: its messages with media, replies, reactions and ticks, the
// composer, and any message an agent is waiting to send there.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Bell, BellOff, ChevronRight, Ellipsis, Loader2, MessageCircle, Pin, Search, Settings, TriangleAlert, Unplug, Archive } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { InlineApproval } from "@/components/approval";
import { EmptyState, PageBody, PageHeader, PageTitle } from "@/components/page";
import { dayLabel, SyncBar, WaAvatar, WaUnread } from "@/components/whatsapp/wa-bits";
import { WaComposer } from "@/components/whatsapp/wa-composer";
import { sameRun, WaMessageRow } from "@/components/whatsapp/wa-message";
import { WaLinkPanel } from "@/components/whatsapp/wa-settings";
import { useNow } from "@/hooks/use-now";
import { relativeTime } from "@/lib/format";
import { href, navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";
import {
  isLinked,
  loadEarlierWaMessages,
  loadWaMessages,
  markWaRead,
  sortedWaChats,
  toggleWaChatSetting,
  useWa,
  waApi,
  waPhone,
  waPreview,
  waSendApprovals,
} from "@/lib/whatsapp";
import type { WaChat, WaMessage, WaStatus } from "@/lib/wa-types";

const settingsHref = href(paths.settings()) + "?section=whatsapp";

function SettingsButton() {
  return (
    <Button variant="ghost" size="sm" asChild className="text-muted-foreground">
      <a href={settingsHref}>
        <Settings />
        <span className="hidden sm:inline">Settings</span>
      </a>
    </Button>
  );
}

/** While the account is linked but WhatsApp isn't connected right now. */
function ConnectionBanner({ s }: { s: WaStatus }) {
  if (s.state === "connected") return null;
  return (
    <div className="flex items-center justify-center gap-2 border-b bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
      {s.state === "error" ? <TriangleAlert className="size-3.5 shrink-0 text-state-waiting" /> : <Loader2 className="size-3.5 shrink-0 animate-spin" />}
      <span className="truncate">{s.state === "error" ? `WhatsApp isn't reachable${s.error ? `: ${s.error}` : ""}. tmx keeps trying.` : "Connecting to WhatsApp…"}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// the list

function ChatRow({ c }: { c: WaChat }) {
  return (
    <li>
      <a href={href(paths.waChat(c.jid))} className="flex items-center gap-3 px-4 py-3 outline-none transition-colors hover:bg-muted/40 focus-visible:bg-muted/50">
        <WaAvatar chat={c} size="lg" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className={cn("truncate text-[0.8438rem]", c.unread > 0 && !c.muted ? "font-semibold" : "font-medium")}>{c.name}</span>
            {c.kind === "group" && <span className="text-2xs text-subtle-foreground">group</span>}
            <span className="tabular ml-auto shrink-0 text-2xs text-subtle-foreground">{c.last ? relativeTime(c.last.at) : ""}</span>
          </div>
          <div className="mt-0.5 flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{waPreview(c) || "No messages yet"}</span>
            {c.muted && <BellOff className="size-3 shrink-0 text-subtle-foreground" aria-label="muted" />}
            {c.pinned && <Pin className="size-3 shrink-0 text-subtle-foreground" aria-label="pinned" />}
            <WaUnread n={c.unread} muted={c.muted} />
          </div>
        </div>
      </a>
    </li>
  );
}

function WaHome() {
  const available = useWa((s) => s.available);
  const off = useWa((s) => s.offMessage);
  const status = useWa((s) => s.status);
  const chats = useWa((s) => s.chats);
  const loaded = useWa((s) => s.chatsLoaded);
  const [q, setQ] = useState("");
  const [found, setFound] = useState<WaChat[] | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const list = useMemo(() => sortedWaChats(chats), [chats]);
  const archived = useMemo(() => sortedWaChats(chats, { archived: true }), [chats]);
  useNow(30_000);

  // Searching: names here at once, message text through the daemon (debounced).
  const term = q.trim();
  useEffect(() => {
    if (term.length < 2) return setFound(null);
    let live = true;
    const t = setTimeout(() => {
      waApi.chats(term, 100).then(
        (r) => live && setFound(r ?? []),
        () => live && setFound(null),
      );
    }, 250);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [term]);
  const results = useMemo(() => {
    if (!term) return null;
    const low = term.toLowerCase();
    const byName = [...list, ...archived].filter((c) => c.name.toLowerCase().includes(low) || waPhone(c.jid).includes(low));
    const seen = new Set(byName.map((c) => c.jid));
    return [...byName, ...(found ?? []).filter((c) => !seen.has(c.jid)).map((c) => chats[c.jid] ?? c)];
  }, [term, list, archived, found, chats]);

  const linked = isLinked(status);
  let body: React.ReactNode;
  if (available === false) {
    body = (
      <EmptyState icon={MessageCircle} title="This tmx has no WhatsApp yet">
        Update tmx to link your WhatsApp here.
      </EmptyState>
    );
  } else if (off) {
    body = (
      <EmptyState icon={Unplug} title="WhatsApp is off">
        This tmx runs without WhatsApp right now.
        <p className="mt-2 text-xs text-subtle-foreground">{off}</p>
      </EmptyState>
    );
  } else if (!status) {
    body = (
      <div className="flex justify-center py-16 text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
      </div>
    );
  } else if (!linked) {
    body = (
      <div className="mx-auto max-w-[38.75rem] rounded-xl border bg-card p-5 sm:p-6">
        <WaLinkPanel />
      </div>
    );
  } else {
    const syncing = status.history?.syncing;
    body = (
      <div className="space-y-4">
        {syncing && (
          <div className="flex flex-col gap-2 rounded-xl border bg-card px-4 py-3 sm:flex-row sm:items-center sm:gap-4">
            <span className="flex items-center gap-2 text-sm">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
              Bringing in your chats from the phone…
            </span>
            <SyncBar progress={status.history.progress} className="sm:ml-auto sm:w-56" />
          </div>
        )}
        {(list.length > 0 || archived.length > 0) && (
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-subtle-foreground" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search chats and messages" aria-label="Search WhatsApp chats" className="pl-8" />
          </div>
        )}
        {results ? (
          results.length === 0 ? (
            <p className="rounded-xl border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">{found === null && term.length >= 2 ? "Searching…" : "Nothing matches."}</p>
          ) : (
            <ul className="divide-y overflow-hidden rounded-xl border bg-card">
              {results.map((c) => (
                <ChatRow key={c.jid} c={c} />
              ))}
            </ul>
          )
        ) : list.length === 0 && archived.length === 0 ? (
          !syncing &&
          loaded && (
            <EmptyState icon={MessageCircle} title="No chats yet">
              Chats show up here as messages come in. Older ones arrive with the history sync after linking.
            </EmptyState>
          )
        ) : (
          <>
            <ul className="divide-y overflow-hidden rounded-xl border bg-card">
              {list.map((c) => (
                <ChatRow key={c.jid} c={c} />
              ))}
            </ul>
            {archived.length > 0 && (
              <div>
                <button
                  type="button"
                  onClick={() => setShowArchived((v) => !v)}
                  aria-expanded={showArchived}
                  className="flex h-8 items-center gap-1.5 rounded-md px-1 text-xs font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <ChevronRight className={cn("size-3.5 transition-transform duration-150", showArchived && "rotate-90")} />
                  <Archive className="size-3.5" />
                  Archived · {archived.length}
                </button>
                {showArchived && (
                  <ul className="mt-1 divide-y overflow-hidden rounded-xl border bg-card">
                    {archived.map((c) => (
                      <ChatRow key={c.jid} c={c} />
                    ))}
                  </ul>
                )}
              </div>
            )}
          </>
        )}
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader actions={available !== false && !off ? <SettingsButton /> : undefined}>
        <MessageCircle className="size-4 text-muted-foreground" />
        <PageTitle>WhatsApp</PageTitle>
        {linked && status?.phone && <span className="hidden truncate font-mono text-2xs text-subtle-foreground sm:inline">{status.phone}</span>}
      </PageHeader>
      {linked && status && <ConnectionBanner s={status} />}
      <PageBody width="max-w-[51.25rem]">{body}</PageBody>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// one chat

function Messages({ chat, onReply }: { chat: WaChat; onReply: (m: WaMessage) => void }) {
  const ml = useWa((s) => s.messages[chat.jid]);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const keepFrom = useRef<number | null>(null);
  const [flash, setFlash] = useState("");
  const list = useMemo(() => ml?.list ?? [], [ml?.list]);

  useEffect(() => {
    stick.current = true;
    void loadWaMessages(chat.jid);
  }, [chat.jid]);

  // Stay at the bottom while new messages arrive, unless the owner scrolled up; keep the place
  // when earlier messages come in above.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (keepFrom.current !== null) {
      el.scrollTop = el.scrollHeight - keepFrom.current;
      keepFrom.current = null;
    } else if (stick.current) el.scrollTop = el.scrollHeight;
  }, [list, chat.jid]);

  function earlier() {
    const el = scroller.current;
    if (el) keepFrom.current = el.scrollHeight - el.scrollTop;
    void loadEarlierWaMessages(chat.jid);
  }

  function jump(id: string) {
    const el = document.getElementById(`wa-${id}`);
    if (!el) {
      toast("That message is further back. Load earlier messages to see it.");
      return;
    }
    el.scrollIntoView({ block: "center", behavior: "smooth" });
    setFlash(id);
    setTimeout(() => setFlash((f) => (f === id ? "" : f)), 1400);
  }

  return (
    <div
      ref={scroller}
      data-testid="wa-scroll"
      onScroll={(e) => {
        const el = e.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
      }}
      className="scroll-thin min-h-0 flex-1 overflow-y-auto"
    >
      <div className="mx-auto max-w-[51.25rem] px-3 pt-4 pb-2 md:px-5">
        {ml?.hasMore && (
          <div className="flex justify-center pb-2">
            <Button variant="ghost" size="xs" onClick={earlier} disabled={ml.loading}>
              {ml.loading && <Loader2 className="animate-spin" />}
              Earlier messages
            </Button>
          </div>
        )}
        {ml?.loaded && list.length === 0 && (
          <div className="flex flex-col items-center px-6 pt-10 pb-6 text-center">
            <WaAvatar chat={chat} size="lg" />
            <p className="mt-3 text-[0.875rem] font-medium">No messages here yet</p>
            <p className="mt-1 max-w-sm text-sm text-muted-foreground">New messages show up as they're sent. Older ones may still come with the history sync.</p>
          </div>
        )}
        {!ml?.loaded && (
          <div className="flex justify-center py-10 text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
          </div>
        )}
        {list.map((m, i) => {
          const prev = list[i - 1];
          const newDay = !prev || new Date(prev.at).toDateString() !== new Date(m.at).toDateString();
          return (
            <div key={m.id}>
              {newDay && (
                <div className="my-3 flex items-center gap-3 text-2xs font-medium text-subtle-foreground">
                  <span className="h-px flex-1 bg-border" />
                  {dayLabel(m.at)}
                  <span className="h-px flex-1 bg-border" />
                </div>
              )}
              <WaMessageRow chat={chat} m={m} first={newDay || !prev || !sameRun(prev, m)} flash={flash === m.id} onReply={onReply} onJump={jump} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Messages agents are waiting to send in this chat: Allow or Deny right here. */
function AgentSends({ chat }: { chat: WaChat }) {
  const approvals = useStore((s) => s.approvals);
  const agents = useStore((s) => s.agents);
  const asks = useMemo(() => waSendApprovals(approvals, chat), [approvals, chat]);
  if (!asks.length) return null;
  return (
    <div className="shrink-0 px-4 pt-2 md:px-6">
      <div className="mx-auto max-w-[51.25rem] space-y-2">
        {asks.map((a) => {
          const agent = agents[a.agent_id];
          return agent ? <InlineApproval key={a.id} a={a} agent={agent} /> : null;
        })}
      </div>
    </div>
  );
}

function ChatMenu({ chat, s }: { chat: WaChat; s: WaStatus }) {
  const set = s.settings;
  const notify = set.notify_head_chats?.includes(chat.jid) ?? false;
  const allAllowed = set.agents_send === "allowed";
  const allowed = allAllowed || (set.allowed_chats?.includes(chat.jid) ?? false);
  const toggle = (key: "allowed_chats" | "notify_head_chats", on: boolean) =>
    toggleWaChatSetting(key, chat.jid, on).catch((e: Error) => toast.error(e.message || "That didn't save."));
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label="Chat options" className="text-muted-foreground">
          <Ellipsis className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <DropdownMenuCheckboxItem checked={notify} onCheckedChange={(v) => void toggle("notify_head_chats", !!v)}>
          Tell the head about new messages
        </DropdownMenuCheckboxItem>
        <DropdownMenuCheckboxItem checked={allowed} disabled={allAllowed} onCheckedChange={(v) => void toggle("allowed_chats", !!v)}>
          <span className="flex flex-col">
            Agents may send without asking
            {allAllowed && <span className="text-2xs text-muted-foreground">On for every chat in Settings</span>}
          </span>
        </DropdownMenuCheckboxItem>
        <DropdownMenuSeparator />
        {chat.unread > 0 && <DropdownMenuItem onSelect={() => void markWaRead(chat.jid)}>Mark as read</DropdownMenuItem>}
        <DropdownMenuItem onSelect={() => navigate(paths.settings() + "?section=whatsapp")}>
          <Settings /> WhatsApp settings
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function WaChatPage({ jid }: { jid: string }) {
  const chat = useWa((s) => s.chats[jid]);
  const status = useWa((s) => s.status);
  const loaded = useWa((s) => s.chatsLoaded);
  const lastAt = useWa((s) => s.messages[jid]?.list.at(-1)?.at ?? 0);
  const [reply, setReply] = useState<WaMessage>();
  useNow(60_000);

  // Reading the chat marks it read (while the window is visible); receipts only if Settings says so.
  useEffect(() => {
    if (!chat || chat.unread === 0 || document.visibilityState !== "visible") return;
    const t = setTimeout(() => void markWaRead(chat.jid), 400);
    return () => clearTimeout(t);
  }, [chat?.jid, chat?.unread, lastAt]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!chat || !status || !isLinked(status)) {
    const waiting = !status || (isLinked(status) && !loaded);
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <EmptyState
          icon={MessageCircle}
          title={waiting ? "Loading…" : !status || !isLinked(status) ? "WhatsApp isn't linked" : "This chat isn't here"}
          className="w-full max-w-md border-none bg-transparent"
        >
          {!waiting && (
            <>
              {isLinked(status) ? "It may still be on its way with the history sync." : "Link your WhatsApp to see your chats here."}
              <div className="mt-4">
                <Button variant="outline" size="sm" asChild>
                  <a href={href(paths.whatsapp())}>{isLinked(status) ? "All WhatsApp chats" : "Link WhatsApp"}</a>
                </Button>
              </div>
            </>
          )}
        </EmptyState>
      </div>
    );
  }

  const notify = status.settings.notify_head_chats?.includes(chat.jid);
  const phone = waPhone(chat.jid);
  const ready = status.state === "connected";
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader actions={<ChatMenu chat={chat} s={status} />}>
        <WaAvatar chat={chat} />
        <div className="min-w-0">
          <h1 className="truncate text-[0.875rem] leading-tight font-semibold tracking-tight">{chat.name}</h1>
          <div className="flex min-w-0 items-center gap-1.5 text-2xs leading-tight text-muted-foreground">
            <span className="shrink-0">{chat.kind === "group" ? "WhatsApp group" : phone ? `WhatsApp · ${phone}` : "WhatsApp"}</span>
            {chat.muted && (
              <span className="flex shrink-0 items-center gap-0.5" title="Muted on your phone">
                · <BellOff className="size-3" />
                <span className="hidden sm:inline">muted</span>
              </span>
            )}
            {notify && (
              <span className="flex min-w-0 items-center gap-0.5" title="New messages here reach the head as a short note">
                · <Bell className="size-3 shrink-0" />
                <span className="hidden truncate sm:inline">the head hears about new messages</span>
              </span>
            )}
          </div>
        </div>
      </PageHeader>
      <ConnectionBanner s={status} />
      {chat.archived && <div className="border-b bg-muted/40 px-4 py-1.5 text-center text-xs text-muted-foreground">Archived on your phone.</div>}
      <Messages chat={chat} onReply={setReply} />
      <AgentSends chat={chat} />
      <WaComposer
        key={chat.jid}
        chat={chat}
        ready={ready}
        why={status.state === "connecting" ? "Connecting to WhatsApp…" : "WhatsApp isn't reachable right now"}
        replyTo={reply}
        onClearReply={() => setReply(undefined)}
      />
    </div>
  );
}

export function WhatsAppPage({ jid }: { jid?: string }) {
  return jid ? <WaChatPage key={jid} jid={jid} /> : <WaHome />;
}

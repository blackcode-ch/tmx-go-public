// Chats home: every chat with other people's tmx, the contacts, open invites; the empty state
// explains how to start (send someone an invite).
import { useEffect, useMemo, useState } from "react";
import { Link2, MessageSquare, MessagesSquare, QrCode, Unplug, Users, Waypoints } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { ChatAvatar, PersonAvatar, ReachDot, reachLabel } from "@/components/chat/chat-bits";
import { MentionBadge, UnreadBadge } from "@/components/chat/sidebar-chats";
import { EmptyState, PageBody, PageHeader, PageTitle, Section } from "@/components/page";
import { useNow } from "@/hooks/use-now";
import { relativeTime } from "@/lib/format";
import { api } from "@/lib/api";
import type { Invite } from "@/lib/chat-types";
import { chatApi, chatUI, expiresIn, previewLine, sortedChats, useChats } from "@/lib/chats";
import { savedVsLiveBanner, shouldPollP2P } from "@/lib/p2p-format";
import { href, navigate, paths } from "@/lib/router";
import type { P2PStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

export function ChatsPage() {
  const enabled = useChats((s) => s.enabled);
  const offMessage = useChats((s) => s.offMessage);
  const chats = useChats((s) => s.chats);
  const contacts = useChats((s) => s.contacts);
  const list = useMemo(() => sortedChats(chats), [chats]);
  const people = useMemo(() => Object.values(contacts).sort((a, b) => a.name.localeCompare(b.name)), [contacts]);
  const [invites, setInvites] = useState<Invite[]>([]);
  const inviteOpen = useChats((s) => s.invite.open);
  const [p2pStatus, setP2PStatus] = useState<P2PStatus | null>(null);
  useNow(30_000);

  useEffect(() => {
    if (enabled) chatApi.invites().then((l) => setInvites((l ?? []).filter((i) => !i.revoked && i.expires_at > Date.now())), () => undefined);
  }, [enabled, inviteOpen]);

  // Only relevant off-screen (banner shows while enabled === false too, since a saved "on" can
  // still be waiting on a restart); errors (e.g. not the owner) just leave the banner unshown.
  useEffect(() => {
    let active = true;
    const load = () => { void api.getP2P().then((s) => { if (active) setP2PStatus(s); }, () => {}); };
    load();
    const onFocus = () => load();
    const onVisible = () => { if (document.visibilityState === "visible") load(); };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      active = false;
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);

  useEffect(() => {
    if (!shouldPollP2P(p2pStatus)) return;
    let active = true;
    const timer = setInterval(() => {
      void api.getP2P().then((s) => { if (active) setP2PStatus(s); }, () => {});
    }, 5000);
    return () => { active = false; clearInterval(timer); };
    // Only re-arm when restart_to_apply itself flips, not on every status refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p2pStatus?.restart_to_apply]);

  const p2pBanner = p2pStatus ? savedVsLiveBanner(p2pStatus) : null;

  const actions =
    enabled !== false ? (
      <>
        <Button variant="ghost" size="sm" onClick={() => chatUI.openJoin()}>
          <Link2 /> Join
        </Button>
        <Button size="sm" onClick={() => chatUI.openInvite("dm")}>
          <QrCode /> Invite someone
        </Button>
      </>
    ) : undefined;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader actions={actions}>
        <PageTitle>Chats</PageTitle>
      </PageHeader>
      <PageBody width="max-w-[51.25rem]">
        {enabled === false ? (
          <EmptyState icon={Unplug} title="Peer-to-peer is off">
            Chats connect your tmx directly with other people's. This tmx runs without it right now.
            {offMessage && <p className="mt-2 text-xs text-subtle-foreground">{offMessage}</p>}
            {p2pBanner && <p className="mt-2 text-xs font-medium text-amber-700 dark:text-amber-400">{p2pBanner}</p>}
          </EmptyState>
        ) : list.length === 0 && people.length === 0 ? (
          <EmptyState
            icon={MessagesSquare}
            title="Chat with other people's tmx"
            actions={
              <>
                <Button onClick={() => chatUI.openInvite("dm")}>
                  <QrCode /> Invite someone
                </Button>
                <Button variant="outline" onClick={() => chatUI.openJoin()}>
                  <Link2 /> Join with a link
                </Button>
              </>
            }
          >
            Send someone an invite: a QR code or a link. When they join, your two tmx connect directly. No accounts, nothing to set up. You, they and your
            agents can then write to each other; groups work the same way.
          </EmptyState>
        ) : (
          <div className="space-y-8">
            <Section
              title="Chats"
              right={
                <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={() => chatUI.openInvite("group")}>
                  <Users /> New group
                </Button>
              }
            >
              {list.length === 0 ? (
                <p className="rounded-xl border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">No chats yet.</p>
              ) : (
                <ul className="divide-y overflow-hidden rounded-xl border bg-card">
                  {list.map((c) => (
                    <li key={c.id} className="group/row relative">
                      <a
                        href={href(paths.chat(c.id))}
                        className="flex items-center gap-3 px-4 py-3 outline-none transition-colors hover:bg-muted/40 focus-visible:bg-muted/50"
                      >
                        <ChatAvatar chat={c} size="lg" />
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2">
                            <span className={cn("truncate text-[0.8438rem]", c.unread > 0 ? "font-semibold" : "font-medium")}>{c.name}</span>
                            {c.kind === "group" && <span className="text-2xs text-subtle-foreground">group</span>}
                            {c.status !== "active" && (
                              <span className="text-2xs text-subtle-foreground">{c.status === "pending" ? "joining" : c.status}</span>
                            )}
                            <span className="tabular ml-auto shrink-0 text-2xs text-subtle-foreground">{relativeTime(c.last_at || c.created_at)}</span>
                          </div>
                          <div className="mt-0.5 flex items-center gap-2">
                            <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{previewLine(c.last_message) || "No messages yet"}</span>
                            <MentionBadge n={c.mentions} />
                            <UnreadBadge n={c.unread} />
                            <span className="w-7 shrink-0" aria-hidden />
                          </div>
                        </div>
                      </a>
                      <a
                        href={href(paths.chatBoard(c.id))}
                        aria-label={`Board of ${c.name}`}
                        title="The chat's board: its messages by topic"
                        className="absolute right-3 bottom-2.5 flex size-7 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <Waypoints className="size-3.5" />
                      </a>
                    </li>
                  ))}
                </ul>
              )}
            </Section>

            {people.length > 0 && (
              <Section title="Contacts">
                <ul className="divide-y overflow-hidden rounded-xl border bg-card">
                  {people.map((p) => (
                    <li key={p.peer_id} className="flex items-center gap-3 px-4 py-2.5">
                      <PersonAvatar name={p.name} />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-[0.8125rem] font-medium">{p.name}</div>
                        <div className="flex items-center gap-1.5 text-2xs text-muted-foreground">
                          <ReachDot r={p.reachability} />
                          {reachLabel(p.reachability)}
                          <span className="text-subtle-foreground">· added {relativeTime(p.added_at)}</span>
                        </div>
                      </div>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={async () => {
                          try {
                            const c = await chatApi.openDM(p.peer_id);
                            useChats.setState((s) => ({ chats: { ...s.chats, [c.id]: c } }));
                            navigate(paths.chat(c.id));
                          } catch (e) {
                            toast.error((e as Error).message);
                          }
                        }}
                      >
                        <MessageSquare /> Message
                      </Button>
                    </li>
                  ))}
                </ul>
              </Section>
            )}

            {invites.length > 0 && (
              <Section title="Open invites">
                <ul className="divide-y overflow-hidden rounded-xl border bg-card">
                  {invites.map((i) => (
                    <li key={i.id} className="flex items-center gap-3 px-4 py-2.5 text-[0.8125rem]">
                      <QrCode className="size-4 shrink-0 text-muted-foreground" />
                      <span className="min-w-0 flex-1 truncate">
                        {i.kind === "group" ? `Group “${i.group_name}”` : "One person"}
                        <span className="text-xs text-muted-foreground">
                          {" "}
                          · expires {expiresIn(i.expires_at)}
                          {i.uses > 0 ? ` · used ${i.uses}×` : ""}
                        </span>
                      </span>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-muted-foreground"
                        onClick={async () => {
                          try {
                            await chatApi.revokeInvite(i.id);
                            setInvites((l) => l.filter((x) => x.id !== i.id));
                          } catch (e) {
                            toast.error((e as Error).message);
                          }
                        }}
                      >
                        Revoke
                      </Button>
                    </li>
                  ))}
                </ul>
              </Section>
            )}
          </div>
        )}
      </PageBody>
    </div>
  );
}

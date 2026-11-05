// The sidebar's "Chats" section: Companion first (once it is set up), DMs and groups with other
// people's tmx, unread badges, and the invite / join / new group actions.
import { useMemo } from "react";
import { Link2, MessagesSquare, Plus, QrCode, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { chatUI, sortedChats, useChats } from "@/lib/chats";
import { href, paths } from "@/lib/router";
import { cn } from "@/lib/utils";
import { SidebarSection } from "../sidebar-section";
import { CompanionSidebarItem, useCompanionSidebarUnread } from "../companion/sidebar-item";
import { ChatAvatar } from "./chat-bits";

const SHOWN = 6;

/** Unread messages that @mention you. */
export function MentionBadge({ n, className }: { n: number | undefined; className?: string }) {
  if (!n || n <= 0) return null;
  return (
    <span
      title={`${n} unread ${n === 1 ? "mention" : "mentions"} of you`}
      className={cn("inline-flex size-[1.125rem] items-center justify-center rounded-full bg-brand text-2xs font-semibold text-brand-foreground", className)}
    >
      @
    </span>
  );
}

export function UnreadBadge({ n, className }: { n: number; className?: string }) {
  if (n <= 0) return null;
  return (
    <span
      className={cn(
        "tabular inline-flex h-[1.125rem] min-w-[1.125rem] items-center justify-center rounded-full bg-brand px-1.5 text-2xs font-semibold text-brand-foreground",
        className,
      )}
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}

export function ChatActionsMenu({ children, align = "end" }: { children: React.ReactNode; align?: "start" | "end" }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
      <DropdownMenuContent align={align} className="w-52">
        <DropdownMenuItem onSelect={() => chatUI.openInvite("dm")}>
          <QrCode /> Invite someone
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => chatUI.openInvite("group")}>
          <Users /> New group…
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => chatUI.openJoin()}>
          <Link2 /> Join with a link…
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function SidebarChats({ path, onNavigate, open, onToggle }: { path: string; onNavigate?: () => void; open: boolean; onToggle: () => void }) {
  const enabled = useChats((s) => s.enabled);
  const chats = useChats((s) => s.chats);
  const list = useMemo(() => sortedChats(chats), [chats]);
  const activeId = path.startsWith("/chats/") ? decodeURIComponent(path.slice("/chats/".length).split(/[/?]/)[0]) : "";
  const shown = list.slice(0, SHOWN);
  const active = list.find((c) => c.id === activeId);
  if (active && !shown.includes(active)) shown.push(active);
  const hiddenUnread = list.filter((c) => !shown.includes(c)).reduce((n, c) => n + c.unread, 0);
  const companionUnread = useCompanionSidebarUnread(path);
  const allUnread = list.reduce((n, c) => n + c.unread, 0) + companionUnread;
  const allMentions = list.reduce((n, c) => n + (c.mentions ?? 0), 0);

  return (
    <SidebarSection
      label="Chats"
      open={open}
      onToggle={onToggle}
      closedInfo={
        <>
          <MentionBadge n={allMentions} />
          <UnreadBadge n={allUnread} />
        </>
      }
      actions={
        enabled !== false && (
          <Tooltip>
            <ChatActionsMenu>
              <TooltipTrigger asChild>
                <Button variant="ghost" size="icon-xs" className="text-muted-foreground" aria-label="New chat">
                  <Plus />
                </Button>
              </TooltipTrigger>
            </ChatActionsMenu>
            <TooltipContent>Invite someone, start a group, join</TooltipContent>
          </Tooltip>
        )
      }
    >
      <CompanionSidebarItem path={path} onNavigate={onNavigate} />
      {enabled === false ? (
        <a
          href={href(paths.chats())}
          onClick={onNavigate}
          className="flex h-7 items-center gap-2 rounded-md px-2 text-[0.7812rem] text-subtle-foreground outline-none hover:bg-sidebar-accent/70 focus-visible:ring-2 focus-visible:ring-ring"
        >
          <MessagesSquare className="size-4 shrink-0" />
          Peer-to-peer is off
        </a>
      ) : list.length === 0 ? (
        <button
          type="button"
          onClick={() => chatUI.openInvite("dm")}
          className="flex h-7 w-full items-center gap-2 rounded-md px-2 text-[0.8125rem] text-muted-foreground outline-none hover:bg-sidebar-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        >
          <QrCode className="size-4" />
          Invite someone
        </button>
      ) : (
        <>
          {shown.map((c) => {
            const on = c.id === activeId;
            const over = c.status === "left" || c.status === "removed";
            return (
              <a
                key={c.id}
                href={href(paths.chat(c.id))}
                onClick={onNavigate}
                aria-current={on ? "page" : undefined}
                className={cn(
                  "flex h-7 items-center gap-2 rounded-md px-2 text-[0.8125rem] text-sidebar-foreground outline-none transition-colors duration-150 hover:bg-sidebar-accent/70 focus-visible:ring-2 focus-visible:ring-ring",
                  on && "bg-sidebar-accent font-medium text-sidebar-accent-foreground",
                  over && !on && "text-muted-foreground",
                )}
              >
                <ChatAvatar chat={c} size="sm" />
                <span className={cn("min-w-0 truncate", c.unread > 0 && !on && "font-medium text-foreground")}>{c.name}</span>
                <span className="ml-auto flex shrink-0 items-center gap-1.5">
                  {c.status === "pending" && <span className="text-2xs text-subtle-foreground">joining</span>}
                  <MentionBadge n={c.mentions} />
                  <UnreadBadge n={c.unread} />
                </span>
              </a>
            );
          })}
          {list.length > shown.length && (
            <a
              href={href(paths.chats())}
              onClick={onNavigate}
              className="flex h-7 items-center gap-2 rounded-md px-2 text-[0.7812rem] text-muted-foreground outline-none hover:bg-sidebar-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
            >
              <MessagesSquare className="size-4" />
              All chats
              <UnreadBadge n={hiddenUnread} className="ml-auto" />
            </a>
          )}
        </>
      )}
    </SidebarSection>
  );
}

// The sidebar's "WhatsApp" group: the latest chats with unread counts once linked; "Link
// WhatsApp" before that. Hidden on a daemon without WhatsApp (or with it switched off). The store
// loads with every snapshot (each (re)connect of the event stream).
import { useMemo } from "react";
import { BellOff, Loader2, MessageCircle, Pin, Smartphone } from "lucide-react";
import { href, paths } from "@/lib/router";
import { cn } from "@/lib/utils";
import { isLinked, sortedWaChats, useWa, waUnread } from "@/lib/whatsapp";
import { SidebarSection } from "../sidebar-section";
import { WaAvatar, WaUnread } from "./wa-bits";

const SHOWN = 5;

const row =
  "flex h-7 items-center gap-2 rounded-md px-2 text-[0.8125rem] outline-none transition-colors duration-150 hover:bg-sidebar-accent/70 focus-visible:ring-2 focus-visible:ring-ring";

export function SidebarWhatsApp({ path, onNavigate, open, onToggle }: { path: string; onNavigate?: () => void; open: boolean; onToggle: () => void }) {
  const available = useWa((s) => s.available);
  const off = useWa((s) => s.offMessage);
  const status = useWa((s) => s.status);
  const chats = useWa((s) => s.chats);
  const list = useMemo(() => sortedWaChats(chats), [chats]);

  if (!available || off || !status) return null;
  const activeJid = path.startsWith("/whatsapp/") ? decodeURIComponent(path.slice("/whatsapp/".length).split(/[/?]/)[0]) : "";
  const shown = list.slice(0, SHOWN);
  const active = list.find((c) => c.jid === activeJid);
  if (active && !shown.includes(active)) shown.push(active);
  const hiddenUnread = waUnread(Object.fromEntries(list.filter((c) => !shown.includes(c)).map((c) => [c.jid, c])));
  const linked = isLinked(status);

  return (
    <SidebarSection
      label="WhatsApp"
      open={open}
      onToggle={onToggle}
      closedInfo={<WaUnread n={waUnread(chats)} />}
      actions={
        linked &&
        status.state !== "connected" && (
          <span className="flex shrink-0 items-center gap-1 pr-1 text-2xs text-subtle-foreground" title={status.state === "error" ? status.error : "Connecting to WhatsApp"}>
            <Loader2 className="size-3 animate-spin" />
            {status.state === "error" ? "offline" : "connecting"}
          </span>
        )
      }
    >
      {!linked ? (
        <a href={href(paths.whatsapp())} onClick={onNavigate} className={cn(row, "text-muted-foreground hover:text-foreground")}>
          {status.state === "linking" ? <Loader2 className="size-4 animate-spin" /> : <Smartphone className="size-4" />}
          {status.state === "linking" ? "Linking… scan the code" : status.state === "logged_out" ? "Link WhatsApp again" : "Link WhatsApp"}
        </a>
      ) : (
        <>
          {shown.map((c) => {
            const on = c.jid === activeJid;
            return (
              <a
                key={c.jid}
                href={href(paths.waChat(c.jid))}
                onClick={onNavigate}
                aria-current={on ? "page" : undefined}
                className={cn(row, "text-sidebar-foreground", on && "bg-sidebar-accent font-medium text-sidebar-accent-foreground")}
              >
                <WaAvatar chat={c} size="sm" />
                <span className={cn("min-w-0 truncate", c.unread > 0 && !c.muted && !on && "font-medium text-foreground")}>{c.name}</span>
                <span className="ml-auto flex shrink-0 items-center gap-1.5">
                  {c.muted && <BellOff className="size-3 text-subtle-foreground" aria-label="muted" />}
                  {c.pinned && !c.unread && <Pin className="size-3 text-subtle-foreground" aria-label="pinned" />}
                  <WaUnread n={on ? 0 : c.unread} muted={c.muted} />
                </span>
              </a>
            );
          })}
          {status.history?.syncing && (
            <a href={href(paths.whatsapp())} onClick={onNavigate} className={cn(row, "text-[0.7812rem] text-muted-foreground")}>
              <Loader2 className="size-4 animate-spin" />
              Bringing in chats… {Math.round(status.history.progress ?? 0)}%
            </a>
          )}
          {list.length > shown.length ? (
            <a href={href(paths.whatsapp())} onClick={onNavigate} className={cn(row, "text-[0.7812rem] text-muted-foreground hover:text-foreground")}>
              <MessageCircle className="size-4" />
              All WhatsApp chats
              <WaUnread n={hiddenUnread} className="ml-auto" />
            </a>
          ) : (
            list.length === 0 &&
            !status.history?.syncing && (
              <a href={href(paths.whatsapp())} onClick={onNavigate} className={cn(row, "text-[0.7812rem] text-subtle-foreground")}>
                <MessageCircle className="size-4" />
                No chats yet
              </a>
            )
          )}
        </>
      )}
    </SidebarSection>
  );
}

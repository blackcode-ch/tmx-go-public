// "Companion" as the first item of the sidebar's Chats section, once Companion is set up
// (connected, or tmx installed into it): a dot when Companion wrote something the owner hasn't
// seen (kept per viewer in localStorage), a quiet spinner while something is in flight.
import { Loader2 } from "lucide-react";
import { useCompanionChat, useCompanionSetUp, useCompanionUnread } from "@/lib/companion";
import { href, paths } from "@/lib/router";
import { cn } from "@/lib/utils";
import { CompanionAvatar } from "./bits";

/** Unread entries from Companion for the Chats section's badge (0 while hidden or open). */
export function useCompanionSidebarUnread(path: string): number {
  const shown = useCompanionSetUp();
  const unread = useCompanionUnread();
  return shown && path !== paths.companion() ? unread : 0;
}

export function CompanionSidebarItem({ path, onNavigate }: { path: string; onNavigate?: () => void }) {
  const shown = useCompanionSetUp();
  const unread = useCompanionUnread();
  const busy = useCompanionChat((s) => s.busy);
  if (!shown) return null;
  const on = path === paths.companion();
  const fresh = unread > 0 && !on;
  return (
    <a
      href={href(paths.companion())}
      onClick={onNavigate}
      aria-current={on ? "page" : undefined}
      data-companion-item
      className={cn(
        "flex h-7 items-center gap-2 rounded-md px-2 text-[0.8125rem] text-sidebar-foreground outline-none transition-colors duration-150 hover:bg-sidebar-accent/70 focus-visible:ring-2 focus-visible:ring-ring",
        on && "bg-sidebar-accent font-medium text-sidebar-accent-foreground",
      )}
    >
      <CompanionAvatar size="sm" />
      <span className={cn("min-w-0 truncate", fresh && "font-medium text-foreground")}>Companion</span>
      <span className="ml-auto flex shrink-0 items-center gap-1.5">
        {busy && <Loader2 className="size-3 animate-spin text-subtle-foreground" aria-label="Working" />}
        {fresh && <span className="size-2 rounded-full bg-brand" role="status" aria-label="New from Companion" title="New from Companion" />}
      </span>
    </a>
  );
}

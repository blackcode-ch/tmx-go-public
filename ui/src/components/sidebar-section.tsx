// A collapsible group heading in the sidebar (Chats, WhatsApp): the chevron and the name open and
// close it; what it holds can show next to the name while it is closed (unread counts), and
// actions (a + button) sit at the end, outside the toggle.
import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

export function SidebarSection({
  label,
  open,
  onToggle,
  closedInfo,
  actions,
  children,
}: {
  label: string;
  open: boolean;
  onToggle: () => void;
  /** shown after the name while closed (e.g. unread badges) */
  closedInfo?: ReactNode;
  /** at the end of the heading, outside the toggle */
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="mt-5">
      <div className="flex items-center pr-2 pl-2">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="flex h-6 min-w-0 flex-1 items-center gap-1 rounded-md px-2 text-2xs font-medium tracking-wide text-subtle-foreground uppercase outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight className={cn("-ml-0.5 size-3 shrink-0 transition-transform duration-150", open && "rotate-90")} aria-hidden />
          <span className="truncate">{label}</span>
          {!open && closedInfo && <span className="ml-auto flex shrink-0 items-center gap-1.5 pl-2 normal-case">{closedInfo}</span>}
        </button>
        {actions}
      </div>
      {open && <div className="mt-1 flex flex-col gap-px px-2">{children}</div>}
    </div>
  );
}

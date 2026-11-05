// The board canvas's side panel: a card, a string's log, or writing in a string. The frame
// (colour, title, actions, close with Escape) is shared; what it shows is the page's.
import { useEffect, type CSSProperties, type ReactNode } from "react";
import { ArrowRightLeft, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";

export function SidePanelFrame({
  color,
  title,
  sub,
  label,
  actions,
  onClose,
  children,
}: {
  color: string;
  title: string;
  /** after the title: "· Log" */
  sub?: string;
  label: string;
  actions?: ReactNode;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !(e.target as HTMLElement).closest("textarea, input, [role=menu]")) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <aside
      data-board-item
      className="absolute inset-y-0 right-0 z-20 flex w-full flex-col border-l bg-background shadow-xl sm:w-[28.75rem]"
      style={{ "--lane": color } as CSSProperties}
      aria-label={label}
    >
      <div className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
        <span className="size-2.5 shrink-0 rounded-full bg-[var(--lane)]" />
        <span className="min-w-0 truncate text-[0.8125rem] font-medium">{title}</span>
        {sub && <span className="shrink-0 text-xs text-muted-foreground">· {sub}</span>}
        <div className="ml-auto flex items-center gap-1">
          {actions}
          <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close">
            <X className="size-4" />
          </Button>
        </div>
      </div>
      {children}
    </aside>
  );
}

/** "Move": file a card under another string. */
export function MoveMenu({ targets, onMove }: { targets: { id: string; title: string; color: string }[]; onMove: (id: string) => void }) {
  if (!targets.length) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" className="gap-1.5 text-muted-foreground">
          <ArrowRightLeft className="size-3.5" />
          Move
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-h-80 w-56 overflow-y-auto">
        <DropdownMenuLabel className="text-2xs font-normal text-muted-foreground">Move this card to</DropdownMenuLabel>
        {targets.map((t) => (
          <DropdownMenuItem key={t.id} onSelect={() => onMove(t.id)}>
            <span className="size-2.5 shrink-0 rounded-full" style={{ background: t.color }} />
            <span className="truncate">{t.title}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";

/** True once the event stream has been down for more than `after` ms. */
export function useDisconnected(after = 3000): boolean {
  const conn = useStore((s) => s.conn);
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (conn === "open") {
      setLate(false);
      return;
    }
    const id = setTimeout(() => setLate(true), after);
    return () => clearTimeout(id);
  }, [conn, after]);
  return conn !== "open" && late;
}

export function ConnectionDot({ className }: { className?: string }) {
  const conn = useStore((s) => s.conn);
  const kind = useStore((s) => s.transport?.kind);
  const late = useDisconnected();
  const label =
    conn === "open"
      ? kind === "mock"
        ? "Connected to the mock daemon"
        : kind === "bridge"
          ? "Connected through the desktop bridge"
          : "Connected to the daemon"
      : late
        ? "Reconnecting to the daemon"
        : "Connecting";
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          role="img"
          aria-label={label}
          className={cn(
            "inline-block size-1.5 shrink-0 rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring",
            conn === "open" ? "bg-state-ok" : late ? "bg-state-waiting" : "bg-state-idle",
            className,
          )}
        />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export function ReconnectingPill({ className }: { className?: string }) {
  const late = useDisconnected();
  if (!late) return null;
  return (
    <span
      role="status"
      className={cn(
        "inline-flex items-center gap-1 rounded-full border bg-background px-2 py-0.5 text-2xs font-medium text-muted-foreground",
        className,
      )}
    >
      <Loader2 className="size-3 animate-spin" />
      Reconnecting…
    </span>
  );
}

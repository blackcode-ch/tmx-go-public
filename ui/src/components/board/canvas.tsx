// The board's canvas, shared by the head's board (pages/board.tsx) and the chat boards
// (components/chat/board-view.tsx): strings of cards on a sheet you pan and zoom, a header
// per string (title, menu, log preview) that you drag sideways to set the urgency order.
// What hangs on the strings, and where the data comes from, is the page's.
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { ArrowLeft, ArrowRight, BookmarkPlus, ChevronUp, Maximize, Minus, MoreHorizontal, Palette, Plus, ScrollText } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { colorValue, liveEntries, THREAD_COLORS } from "@/lib/board";
import type { LogKind } from "@/lib/types";
import { cn } from "@/lib/utils";
import { EntryIcon } from "./log";
import { remScale } from "@/lib/text-size";

export const LANE_W = 296;
export const SHOWN = 6; // cards per string before "N earlier"
const MIN_Z = 0.3;
const MAX_Z = 1.6;

type View = { x: number; y: number; z: number };

/** Lanes are LANE_W wide with a 40 px gap: dragging a header moves it by whole strides. */
export const STRIDE = LANE_W + 40;

export type Drag = { id: string; from: number; to: number; dx: number };
export type Dragging = { move: (id: string, from: number, dx: number) => void; end: (commit: boolean) => void };

/** A line of a log, as the canvas shows it (a thread's or a chat topic's). */
export interface LogLine {
  id: string;
  kind: LogKind;
  text: string;
  link?: string;
  at: number;
  corrects?: string;
}

function loadView(key: string): View {
  try {
    const v = JSON.parse(localStorage.getItem(key) ?? "");
    if (typeof v?.x === "number" && typeof v?.y === "number" && typeof v?.z === "number") return v;
  } catch {
    /* private window or nothing saved */
  }
  return { x: 0, y: 0, z: 1 };
}

function saveView(key: string, v: View) {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* ignore */
  }
}

const clampZ = (z: number) => Math.min(MAX_Z, Math.max(MIN_Z, z));

/** A dialog opened from a menu item waits for the menu to close; opened at once, focus going
 * back to the menu button closes it again and leaves the page locked (Radix). */
export const after = (fn: () => void) => () => setTimeout(fn, 0);

/** The urgency order: dragging a string's header left or right (left = most urgent), or Move
 * left/right in its menu. `ids` are the strings shown, in order; save gets the new order. */
export function useLaneOrder(ids: string[], save: (ids: string[]) => void) {
  const [drag, setDrag] = useState<Drag | null>(null);
  const dragRef = useRef<Drag | null>(null);
  function saveOrder(from: number, to: number) {
    if (from === to) return;
    const next = [...ids];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    save(next);
  }
  const dragging: Dragging = {
    move(id: string, from: number, dx: number) {
      const to = Math.min(ids.length - 1, Math.max(0, from + Math.round(dx / (STRIDE * remScale()))));
      dragRef.current = { id, from, to, dx };
      setDrag(dragRef.current);
    },
    end(commit: boolean) {
      const d = dragRef.current;
      dragRef.current = null;
      setDrag(null);
      if (d && commit) saveOrder(d.from, d.to);
    },
  };
  return { drag, dragging, saveOrder };
}

// ---------------------------------------------------------------------------------------------
// canvas: drag the background to pan, wheel to scroll, pinch or Ctrl+wheel to zoom

export function Canvas({ children, storageKey = "tmx.board.view" }: { children: ReactNode; storageKey?: string }) {
  const outer = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<View>(() => loadView(storageKey));
  const viewRef = useRef(view);
  viewRef.current = view;

  const apply = useCallback(
    (v: View) => {
      setView(v);
      saveView(storageKey, v);
    },
    [storageKey],
  );

  const zoomAt = useCallback(
    (z: number, cx: number, cy: number) => {
      const v = viewRef.current;
      const nz = clampZ(z);
      // keep the point under (cx, cy) where it is
      apply({ z: nz, x: cx - ((cx - v.x) * nz) / v.z, y: cy - ((cy - v.y) * nz) / v.z });
    },
    [apply],
  );

  const fit = useCallback(() => {
    const o = outer.current;
    const i = inner.current;
    if (!o || !i) return;
    const w = i.offsetWidth;
    const h = i.offsetHeight;
    const z = clampZ(Math.min(1, o.clientWidth / w, (o.clientHeight - 24) / h));
    apply({ z, x: Math.max(0, (o.clientWidth - w * z) / 2), y: 0 });
  }, [apply]);

  useEffect(() => {
    const el = outer.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if ((e.target as HTMLElement).closest("[data-scrolls]")) return;
      e.preventDefault();
      const r = el.getBoundingClientRect();
      if (e.ctrlKey || e.metaKey) {
        zoomAt(viewRef.current.z * Math.exp(-e.deltaY * 0.01), e.clientX - r.left, e.clientY - r.top);
      } else {
        const v = viewRef.current;
        const dx = e.shiftKey && !e.deltaX ? e.deltaY : e.deltaX;
        const dy = e.shiftKey && !e.deltaX ? 0 : e.deltaY;
        apply({ ...v, x: v.x - dx, y: v.y - dy });
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [apply, zoomAt]);

  // pointer pan (and two-finger pinch on touch screens)
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ d: number; z: number } | null>(null);
  // Only the canvas itself pans: menus and dialogs opened from it render in portals, whose
  // events still bubble here through React; capturing the pointer for them would swallow
  // their clicks.
  const onCanvas = (t: EventTarget) => !!outer.current?.contains(t as Node) && !(t as HTMLElement).closest("[data-board-item]");
  function onPointerDown(e: React.PointerEvent) {
    if (e.button !== 0 || !onCanvas(e.target)) return;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()];
      pinch.current = { d: Math.hypot(a.x - b.x, a.y - b.y), z: viewRef.current.z };
    }
  }
  function onPointerMove(e: React.PointerEvent) {
    const prev = pointers.current.get(e.pointerId);
    if (!prev) return;
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.current.size === 2 && pinch.current) {
      const [a, b] = [...pointers.current.values()];
      const r = outer.current!.getBoundingClientRect();
      zoomAt((pinch.current.z * Math.hypot(a.x - b.x, a.y - b.y)) / pinch.current.d, (a.x + b.x) / 2 - r.left, (a.y + b.y) / 2 - r.top);
      return;
    }
    const v = viewRef.current;
    apply({ ...v, x: v.x + e.clientX - prev.x, y: v.y + e.clientY - prev.y });
  }
  function onPointerUp(e: React.PointerEvent) {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
  }

  const zoomBy = (f: number) => {
    const o = outer.current;
    if (o) zoomAt(viewRef.current.z * f, o.clientWidth / 2, o.clientHeight / 2);
  };

  // The dots move with the canvas, so panning feels like moving a sheet.
  const dot = 22 * view.z;
  const bg: CSSProperties = {
    backgroundImage: "radial-gradient(circle, var(--border-strong) 1px, transparent 1.2px)",
    backgroundSize: `${dot}px ${dot}px`,
    backgroundPosition: `${view.x}px ${view.y}px`,
  };

  return (
    <div
      ref={outer}
      className="relative min-w-0 flex-1 cursor-grab touch-none overflow-hidden bg-background select-none active:cursor-grabbing"
      style={bg}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onDoubleClick={(e) => {
        if (onCanvas(e.target)) fit();
      }}
    >
      <div
        ref={inner}
        className="absolute top-0 left-0 flex items-start gap-10 px-10 pt-8 pb-24"
        style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.z})`, transformOrigin: "0 0" }}
      >
        {children}
      </div>
      <div data-board-item className="absolute bottom-4 left-4 flex items-center gap-0.5 rounded-lg border bg-card/95 p-0.5 shadow-sm backdrop-blur">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" onClick={() => zoomBy(1 / 1.2)} aria-label="Zoom out">
              <Minus className="size-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Zoom out (Ctrl+scroll or pinch)</TooltipContent>
        </Tooltip>
        <button
          className="tabular w-11 rounded-md py-1 text-center text-2xs text-muted-foreground hover:bg-accent hover:text-foreground"
          onClick={() => apply({ ...viewRef.current, z: 1 })}
          aria-label="Zoom to 100%"
        >
          {Math.round(view.z * 100)}%
        </button>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" onClick={() => zoomBy(1.2)} aria-label="Zoom in">
              <Plus className="size-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Zoom in</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" onClick={fit} aria-label="Fit the board">
              <Maximize className="size-3.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Fit everything (or double-click the canvas)</TooltipContent>
        </Tooltip>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// a string

/** How a string sits while a header is dragged: the lifted one follows the pointer, the ones
 * it passes make room. */
export function laneShift(drag: Drag | null, id: string, index: number): { lifted: boolean; shift: number } {
  const lifted = drag?.id === id;
  let shift = 0;
  if (drag && lifted) shift = drag.dx;
  else if (drag && drag.from < drag.to && index > drag.from && index <= drag.to) shift = -STRIDE * remScale();
  else if (drag && drag.to < drag.from && index >= drag.to && index < drag.from) shift = STRIDE * remScale();
  return { lifted, shift };
}

/** A string from its pin down: the header and what hangs on it are its children. */
export function LaneFrame({ title, color, lifted, shift, children }: { title: string; color: string; lifted: boolean; shift: number; children: ReactNode }) {
  const style = {
    "--lane": color,
    width: `${LANE_W / 16}rem`, // scales with the text size (lib/text-size.ts)
    transform: shift ? `translateX(${shift}px)` : undefined,
    transition: lifted ? "none" : "transform 160ms ease",
    zIndex: lifted ? 30 : undefined,
  } as CSSProperties;
  return (
    <section className={cn("relative flex shrink-0 flex-col items-center", lifted && "opacity-95 drop-shadow-xl")} style={style} aria-label={`Topic ${title}`}>
      {/* the string, from the pin to the last knot */}
      <div className="pointer-events-none absolute top-3 bottom-4 left-1/2 w-[2px] -translate-x-1/2 rounded-full bg-[var(--lane)] opacity-45" />
      <div className="relative z-10 size-3.5 rounded-full border-2 border-background bg-[var(--lane)] shadow-[0_1px_3px_rgb(0_0_0/0.25)]" aria-hidden />
      {children}
    </section>
  );
}

/** A spot on the string: a knot, then what hangs from it. */
export function Knot({ children, tilt = 0 }: { children: ReactNode; tilt?: number }) {
  return (
    <div className="relative z-10 mt-5 flex w-full flex-col items-center">
      <span className="mb-1.5 size-2 rounded-full bg-[var(--lane)] ring-2 ring-background" aria-hidden />
      <div className="flex w-full justify-center transition-transform duration-200 hover:rotate-0" style={{ transform: `rotate(${tilt * 0.35}deg)` }}>
        {children}
      </div>
    </div>
  );
}

/** "N earlier": the string shows its last SHOWN cards until this is pressed. */
export function EarlierKnot({ hidden, onShow }: { hidden: number; onShow: () => void }) {
  return (
    <Knot>
      <button
        data-board-item
        onClick={onShow}
        className="flex items-center gap-1 rounded-full border bg-card px-2.5 py-1 text-2xs text-muted-foreground shadow-xs hover:text-foreground"
      >
        <ChevronUp className="size-3" />
        {hidden} earlier
      </button>
    </Knot>
  );
}

/** The last knot: write in this string. */
export function ComposeKnot({ label, active, disabled, onClick }: { label: string; active: boolean; disabled?: boolean; onClick: () => void }) {
  return (
    <Knot>
      <button
        data-board-item
        onClick={onClick}
        className={cn(
          "flex items-center gap-1.5 rounded-full border border-dashed bg-card px-3 py-1.5 text-xs text-muted-foreground shadow-xs transition-colors hover:border-solid hover:text-foreground",
          active && "border-solid border-[var(--lane)] text-foreground",
        )}
        disabled={disabled}
      >
        <Plus className="size-3.5" />
        {label}
      </button>
    </Knot>
  );
}

/** The colours a string can take ("" = automatic), as a menu. */
export function ColourSubmenu({ onPick }: { onPick: (color: string) => void }) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <Palette /> Colour
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        <DropdownMenuItem onSelect={() => onPick("")}>
          <span className="size-3 rounded-full border border-dashed border-muted-foreground" /> Automatic
        </DropdownMenuItem>
        {THREAD_COLORS.map((c) => (
          <DropdownMenuItem key={c} onSelect={() => onPick(c)}>
            <span className="size-3 rounded-full" style={{ background: colorValue(c) }} />
            <span className="capitalize">{c}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

/** A string's header: title, what it is (subtitle), the priority once there is an order, the
 * log's last three lines in force, and a menu (right-click too). Drag it sideways to reorder.
 * `menu` holds the page's own items (after the log and order ones); `children` its dialogs. */
export function LaneHeader({
  title,
  archived,
  subtitle,
  entries,
  priority,
  lifted,
  onDrag,
  onMoveLeft,
  onMoveRight,
  onLog,
  menu,
  children,
}: {
  title: string;
  archived?: boolean;
  subtitle: ReactNode;
  entries: LogLine[];
  priority: number;
  lifted: boolean;
  onDrag?: { move: (dx: number) => void; end: (commit: boolean) => void };
  onMoveLeft?: () => void;
  onMoveRight?: () => void;
  onLog: (adding: boolean) => void;
  menu: ReactNode;
  children?: ReactNode;
}) {
  const live = useMemo(() => liveEntries(entries), [entries]);
  // Drag the header (not its buttons) sideways to reorder; a short press is not a drag.
  const press = useRef<{ x: number; z: number; moved: boolean; id: number } | null>(null);
  function onPointerDown(e: React.PointerEvent<HTMLDivElement>) {
    // The menu and dialogs render in portals but their events bubble here through React:
    // only presses on the header itself start a drag (capturing theirs would eat the click).
    const el = e.currentTarget;
    if (!onDrag || e.button !== 0 || !el.contains(e.target as Node) || (e.target as HTMLElement).closest("button, a, input, textarea")) return;
    const z = el.getBoundingClientRect().width / el.offsetWidth || 1;
    press.current = { x: e.clientX, z, moved: false, id: e.pointerId };
    el.setPointerCapture(e.pointerId);
  }
  function onPointerMove(e: React.PointerEvent) {
    const p = press.current;
    if (!p || p.id !== e.pointerId || !onDrag) return;
    const dx = (e.clientX - p.x) / p.z;
    if (!p.moved && Math.abs(dx) < 6) return;
    p.moved = true;
    onDrag.move(dx);
  }
  function onPointerUp(e: React.PointerEvent, commit: boolean) {
    const p = press.current;
    if (!p || p.id !== e.pointerId) return;
    press.current = null;
    if (p.moved) onDrag?.end(commit);
  }
  const [open, setOpen] = useState(false);

  return (
    <div
      data-board-item
      onContextMenu={(e) => {
        if (!e.currentTarget.contains(e.target as Node)) return; // a right-click inside the open menu
        e.preventDefault();
        setOpen(true);
      }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={(e) => onPointerUp(e, true)}
      onPointerCancel={(e) => onPointerUp(e, false)}
      className={cn(
        "relative z-10 mt-2 w-full rounded-xl border bg-card px-3.5 py-2.5 shadow-sm",
        archived && "opacity-60",
        onDrag && "cursor-grab",
        lifted && "cursor-grabbing ring-2 ring-[var(--lane)]/40",
      )}
      style={{ borderTop: "3px solid var(--lane)" }}
      title={onDrag ? "Drag sideways to reorder: further left is more urgent" : undefined}
    >
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-[0.875rem] font-semibold tracking-tight" title={title}>
            {title}
          </h2>
          <p className="mt-0.5 truncate text-2xs text-muted-foreground">
            {priority > 0 && (
              <span className="font-medium text-foreground" title="Your urgency order: 1 is the most urgent">
                #{priority}
                {" · "}
              </span>
            )}
            {subtitle}
            {archived && " · archived"}
          </p>
        </div>
        <DropdownMenu open={open} onOpenChange={setOpen} modal={false}>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" className="-mr-1.5 -mt-0.5 text-muted-foreground" aria-label={`${title} options`}>
              <MoreHorizontal className="size-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-52" onCloseAutoFocus={(e) => e.preventDefault()}>
            <DropdownMenuItem onSelect={() => onLog(false)}>
              <ScrollText /> Open the log
            </DropdownMenuItem>
            {!archived && (
              <DropdownMenuItem onSelect={() => onLog(true)}>
                <BookmarkPlus /> Add to the log…
              </DropdownMenuItem>
            )}
            {(onMoveLeft || onMoveRight) && <DropdownMenuSeparator />}
            {onMoveLeft && (
              <DropdownMenuItem onSelect={onMoveLeft}>
                <ArrowLeft /> Move left (more urgent)
              </DropdownMenuItem>
            )}
            {onMoveRight && (
              <DropdownMenuItem onSelect={onMoveRight}>
                <ArrowRight /> Move right (less urgent)
              </DropdownMenuItem>
            )}
            <DropdownMenuSeparator />
            {menu}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {live.length > 0 ? (
        <button
          onClick={() => onLog(false)}
          className="-mx-1.5 mt-2 block w-[calc(100%+0.75rem)] rounded-md border-t px-1.5 pt-2 pb-1 text-left hover:bg-muted/50"
          aria-label={`${title}: open the log (${live.length})`}
        >
          <ul className="space-y-1">
            {live.slice(-3).map((e) => (
              <li key={e.id} className="flex items-start gap-1.5 text-xs leading-snug">
                <EntryIcon kind={e.kind} className="mt-0.5" />
                <span className="line-clamp-2 [overflow-wrap:anywhere]">{e.text}</span>
              </li>
            ))}
          </ul>
          <span className="mt-1 block text-2xs text-muted-foreground">{live.length > 3 ? `Log · ${live.length} lines` : "Log"}</span>
        </button>
      ) : (
        !archived && (
          <button onClick={() => onLog(true)} className="mt-1.5 flex items-center gap-1 text-2xs text-muted-foreground hover:text-foreground">
            <BookmarkPlus className="size-3" /> Add a decision
          </button>
        )
      )}
      {children}
    </div>
  );
}

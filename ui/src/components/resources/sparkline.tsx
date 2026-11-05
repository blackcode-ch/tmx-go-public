import { useId } from "react";
import { cn } from "@/lib/utils";

/**
 * A quiet line with a faint area under it. `max` fixes the scale (100 for percentages);
 * otherwise the series' own peak (with headroom) is the top.
 */
export function Sparkline({
  values,
  max,
  className,
  label,
}: {
  values: number[];
  max?: number;
  className?: string;
  label?: string;
}) {
  const id = useId();
  const W = 120;
  const H = 28;
  if (values.length < 2) {
    return <div className={cn("h-7", className)} aria-hidden />;
  }
  const top = max ?? (Math.max(...values) * 1.15 || 1);
  const step = W / (values.length - 1);
  const pts = values.map((v, i) => [i * step, H - 1 - (Math.min(Math.max(v, 0), top) / top) * (H - 3)] as const);
  const line = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
  const area = `${line} L${W},${H} L0,${H} Z`;
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      className={cn("h-7 w-full overflow-visible text-brand", className)}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <defs>
        <linearGradient id={id} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor="currentColor" stopOpacity="0.16" />
          <stop offset="1" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${id})`} />
      <path d={line} fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

/** A thin progress bar (used for the day's writes against the budget). */
export function Meter({ value, max, className }: { value: number; max: number; className?: string }) {
  const p = max > 0 ? Math.min(1, value / max) : 0;
  const tone = p >= 1 ? "bg-state-waiting" : p >= 0.8 ? "bg-[color-mix(in_oklch,var(--state-waiting)_70%,var(--muted-foreground))]" : "bg-muted-foreground/45";
  return (
    <div className={cn("h-1 w-full overflow-hidden rounded-full bg-muted", className)} role="meter" aria-valuenow={Math.round(p * 100)} aria-valuemin={0} aria-valuemax={100}>
      <div className={cn("h-full rounded-full transition-[width] duration-500", tone)} style={{ width: `${Math.max(p * 100, p > 0 ? 2 : 0)}%` }} />
    </div>
  );
}

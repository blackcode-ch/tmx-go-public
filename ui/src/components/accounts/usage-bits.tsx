// Small pieces of the Usage page (pages/usage.tsx): a harness's mark, a quota bar, the install
// hint, and the sidebar's dot for a quota that is almost used up.
import { AlertTriangle, ExternalLink } from "lucide-react";
import { CopyButton } from "@/components/copy-button";
import { fullestLimit, harnessMark, isLink, limitTone, resetLabel, toneWords, useAccounts, usedPct, type AccountLimit, type LimitTone } from "@/lib/accounts";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";

// A hue per harness for its mark: a tint, not a logo.
const HUE: Record<string, number> = { claude: 42, codex: 168, antigravity: 262 };

export function HarnessMark({ harness, label, muted, className }: { harness: string; label?: string; muted?: boolean; className?: string }) {
  const base = `oklch(0.62 0.14 ${HUE[harness] ?? 300})`;
  return (
    <span
      aria-hidden
      className={cn(
        "flex size-9 shrink-0 items-center justify-center rounded-[9px] text-[0.8125rem] font-semibold tracking-tight transition-[filter,opacity] duration-200",
        muted && "opacity-80 grayscale",
        className,
      )}
      style={{
        background: `color-mix(in oklch, ${base} 15%, var(--card))`,
        color: `color-mix(in oklch, ${base} 78%, var(--foreground))`,
        boxShadow: `inset 0 0 0 1px color-mix(in oklch, ${base} 26%, transparent)`,
      }}
    >
      {harnessMark(harness, label)}
    </span>
  );
}

const BAR: Record<LimitTone, string> = { ok: "bg-state-ok", warn: "bg-state-waiting", hot: "bg-state-error" };
const AMBER_TEXT = "text-[color-mix(in_oklch,var(--state-waiting)_72%,var(--foreground))]";
export const TONE_TEXT: Record<LimitTone, string> = { ok: "text-foreground", warn: AMBER_TEXT, hot: "text-state-error" };

function absolute(ms: number): string {
  return new Date(ms).toLocaleString(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

/** One quota: its label, how much is used, a bar, and when it resets. */
export function LimitRow({ limit, now }: { limit: AccountLimit; now: number }) {
  const p = usedPct(limit.used_percent);
  const tone = limitTone(p);
  const words = toneWords(p);
  const reset = resetLabel(limit, now);
  return (
    <div data-limit={limit.id}>
      <div className="flex items-baseline gap-3">
        <span className="min-w-0 flex-1 truncate text-[0.8125rem] text-foreground/90" title={limit.label}>
          {limit.label}
        </span>
        <span className="tabular shrink-0 text-xs text-muted-foreground">
          <span className={cn("text-[0.8125rem] font-semibold", TONE_TEXT[tone])}>{p}%</span> used
        </span>
      </div>
      <div
        role="meter"
        aria-label={limit.label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={p}
        aria-valuetext={[`${p}% used`, words.toLowerCase(), reset].filter(Boolean).join(", ")}
        className="mt-2 h-2 overflow-hidden rounded-full bg-muted dark:bg-white/[0.08]"
      >
        <div className={cn("h-full rounded-full transition-[width] duration-500 ease-out", BAR[tone])} style={{ width: `${p > 0 ? Math.max(p, 2) : 0}%` }} />
      </div>
      <div className="mt-1.5 flex min-h-4 items-center gap-2 text-2xs text-muted-foreground">
        <span className="tabular min-w-0 truncate" title={limit.resets_at > 0 ? absolute(limit.resets_at) : undefined}>
          {reset}
        </span>
        {words && (
          <span className={cn("ml-auto flex shrink-0 items-center gap-1 font-medium", TONE_TEXT[tone])}>
            {tone === "hot" && <AlertTriangle className="size-3" aria-hidden />}
            {words}
          </span>
        )}
      </div>
    </div>
  );
}

/** How to install a harness: a command in a copyable chip, or a link to open. */
export function InstallHint({ text, label }: { text: string; label: string }) {
  const t = text.trim();
  if (isLink(t)) {
    const shown = t.replace(/^https?:\/\//, "").replace(/\/$/, "");
    return (
      <div className="flex min-w-0 items-center gap-1 rounded-lg border bg-code py-1 pr-1 pl-3">
        <a
          href={t}
          target="_blank"
          rel="noopener noreferrer"
          onClick={(e) => {
            const tr = useStore.getState().transport;
            if (!tr) return;
            e.preventDefault();
            tr.openExternal(t);
          }}
          className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-[0.7812rem] text-brand hover:underline"
        >
          <span className="truncate">{shown}</span>
          <ExternalLink className="size-3 shrink-0" aria-hidden />
        </a>
        <CopyButton text={t} label={`Copy the link to ${label}`} />
      </div>
    );
  }
  return (
    <div className="flex min-w-0 items-start gap-1 rounded-lg border bg-code py-1 pr-1 pl-3">
      <code className="min-w-0 flex-1 py-[3px] font-mono text-[0.75rem] leading-[1.125rem] [overflow-wrap:anywhere]">
        <span className="text-subtle-foreground select-none">$ </span>
        {t}
      </code>
      <CopyButton text={t} label="Copy the install command" />
    </div>
  );
}

/** The sidebar's Usage item: a red dot while a connected account's quota is almost used up. */
export function UsageDot() {
  const what = useAccounts((s) => {
    const f = fullestLimit(s.payload?.accounts);
    if (!f || usedPct(f.limit.used_percent) < 90) return "";
    return `${f.account.label}: ${f.limit.label} ${usedPct(f.limit.used_percent)}% used`;
  });
  if (!what) return null;
  return <span className="size-1.5 rounded-full bg-state-error" role="img" aria-label={what} title={what} />;
}

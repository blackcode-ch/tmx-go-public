// Small pieces of the WhatsApp UI: the avatar (the chat's picture or initials), delivery ticks,
// unread badges, the progress of the history sync, and WhatsApp's own text formatting.
import { Fragment, type ReactNode } from "react";
import { AlertCircle, Check, CheckCheck, Clock3, Users } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PersonAvatar } from "@/components/chat/chat-bits";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";
import type { WaChat, WaMessage } from "@/lib/wa-types";

export function WaAvatar({ chat, size = "md", className }: { chat: Pick<WaChat, "kind" | "name" | "avatar">; size?: "sm" | "md" | "lg"; className?: string }) {
  const box = size === "sm" ? "size-5" : size === "md" ? "size-7" : "size-10";
  if (chat.avatar) return <img src={chat.avatar} alt="" aria-hidden className={cn("shrink-0 rounded-full object-cover", box, className)} />;
  if (chat.kind === "dm") return <PersonAvatar name={chat.name} size={size} className={className} />;
  return (
    <span aria-hidden className={cn("flex shrink-0 items-center justify-center rounded-full border bg-muted/60 text-muted-foreground", box, className)}>
      <Users className={size === "lg" ? "size-5" : size === "md" ? "size-3.5" : "size-3"} />
    </span>
  );
}

const TICK: Record<WaMessage["status"], { icon: React.ComponentType<{ className?: string }>; label: string; cls: string }> = {
  pending: { icon: Clock3, label: "Sending…", cls: "text-subtle-foreground" },
  sent: { icon: Check, label: "Sent", cls: "text-subtle-foreground" },
  delivered: { icon: CheckCheck, label: "Delivered", cls: "text-subtle-foreground" },
  read: { icon: CheckCheck, label: "Read", cls: "text-brand" },
  failed: { icon: AlertCircle, label: "Not sent", cls: "text-state-error" },
};

/** WhatsApp's ticks: a clock while sending, one tick sent, two delivered, two in blue read. */
export function WaTicks({ m, className }: { m: Pick<WaMessage, "status" | "error">; className?: string }) {
  const s = TICK[m.status] ?? TICK.sent;
  const Icon = s.icon;
  const label = m.status === "failed" && m.error ? `Not sent: ${m.error}` : s.label;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={cn("inline-flex items-center gap-1", s.cls, className)} aria-label={label}>
          <Icon className="size-3.5" />
          {m.status === "failed" && <span className="text-2xs">Not sent</span>}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{label}</TooltipContent>
    </Tooltip>
  );
}

/** Unread count; grey for a muted chat. */
export function WaUnread({ n, muted, className }: { n: number; muted?: boolean; className?: string }) {
  if (!n || n <= 0) return null;
  return (
    <span
      className={cn(
        "tabular inline-flex h-[1.125rem] min-w-[1.125rem] items-center justify-center rounded-full px-1.5 text-2xs font-semibold",
        muted ? "bg-muted-foreground/25 text-foreground/70" : "bg-brand text-brand-foreground",
        className,
      )}
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}

export function SyncBar({ progress, className }: { progress?: number; className?: string }) {
  const pct = Math.max(0, Math.min(100, Math.round(progress ?? 0)));
  return (
    <div className={cn("flex items-center gap-2", className)} role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="History sync">
      <div className="h-1.5 w-full max-w-48 overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-brand transition-[width] duration-500" style={{ width: `${Math.max(pct, 4)}%` }} />
      </div>
      <span className="tabular shrink-0 text-2xs text-muted-foreground">{pct}%</span>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// text: WhatsApp's *bold*, _italic_, ~strike~, `code` and ```mono```, and links. Built as React
// elements from plain text, so nothing in a message is ever treated as markup.

const URL_RE = /https?:\/\/[^\s<>"']+[^\s<>"'.,:;!?)\]]/;
const TOKEN = new RegExp(
  [
    "```[\\s\\S]+?```",
    "`[^`\\n]+`",
    URL_RE.source,
    "(?<![\\w*])\\*(?=\\S)[^*\\n]*?\\S\\*(?![\\w*])",
    "(?<![\\w*])\\*\\S\\*(?![\\w*])",
    "(?<![\\w_])_(?=\\S)[^_\\n]*?\\S_(?![\\w_])",
    "(?<![\\w~])~(?=\\S)[^~\\n]*?\\S~(?![\\w~])",
  ].join("|"),
  "g",
);

export function ExternalLink({ href, children, className }: { href: string; children: ReactNode; className?: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      className={cn("text-brand underline-offset-2 hover:underline [overflow-wrap:anywhere]", className)}
      onClick={(e) => {
        const t = useStore.getState().transport;
        if (t?.desktop) {
          e.preventDefault();
          t.openExternal(href);
        }
      }}
    >
      {children}
    </a>
  );
}

export function WaText({ text, className }: { text: string; className?: string }) {
  const out: ReactNode[] = [];
  let last = 0;
  let k = 0;
  for (const m of text.matchAll(TOKEN)) {
    const s = m[0];
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    last = at + s.length;
    if (s.startsWith("```")) out.push(<code key={k++} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.7812rem]">{s.slice(3, -3)}</code>);
    else if (s.startsWith("`")) out.push(<code key={k++} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.7812rem]">{s.slice(1, -1)}</code>);
    else if (/^https?:/.test(s)) out.push(<ExternalLink key={k++} href={s}>{s}</ExternalLink>);
    else if (s.startsWith("*")) out.push(<strong key={k++} className="font-semibold">{s.slice(1, -1)}</strong>);
    else if (s.startsWith("_")) out.push(<em key={k++}>{s.slice(1, -1)}</em>);
    else if (s.startsWith("~")) out.push(<s key={k++}>{s.slice(1, -1)}</s>);
    else out.push(<Fragment key={k++}>{s}</Fragment>);
  }
  if (last < text.length) out.push(text.slice(last));
  return <span className={cn("whitespace-pre-wrap [overflow-wrap:anywhere]", className)}>{out}</span>;
}

/** The first link in a text (a location's map link). */
export function firstLink(text: string | undefined): string {
  return (text ?? "").match(URL_RE)?.[0] ?? "";
}

export function dayLabel(ms: number): string {
  const d = new Date(ms);
  const today = new Date();
  const y = new Date();
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
}

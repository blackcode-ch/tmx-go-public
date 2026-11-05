// Small pieces of the chat UI: avatars, the person/agent badge, delivery ticks, reachability.
import { AlertCircle, Bot, Check, CheckCheck, Clock3, User, Users } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { Chat, ChatMessage, Reachability } from "@/lib/chat-types";
import { initials } from "@/lib/chats";
import { cn } from "@/lib/utils";

// A stable soft hue per name, so people are told apart at a glance without loud colours.
const HUES = [24, 60, 145, 190, 230, 265, 300, 335];
function hue(name: string): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

export function PersonAvatar({ name, className, size = "md" }: { name: string; className?: string; size?: "sm" | "md" | "lg" }) {
  const h = hue(name);
  return (
    <span
      aria-hidden
      style={{
        background: `oklch(0.93 0.04 ${h})`,
        color: `oklch(0.42 0.09 ${h})`,
      }}
      className={cn(
        "flex shrink-0 items-center justify-center rounded-full font-semibold select-none dark:brightness-[0.82]",
        size === "sm" && "size-5 text-[0.5938rem]",
        size === "md" && "size-7 text-[0.6875rem]",
        size === "lg" && "size-10 text-[0.875rem]",
        className,
      )}
    >
      {initials(name)}
    </span>
  );
}

export function ChatAvatar({ chat, size = "md", className }: { chat: Pick<Chat, "kind" | "name">; size?: "sm" | "md" | "lg"; className?: string }) {
  if (chat.kind === "dm") return <PersonAvatar name={chat.name} size={size} className={className} />;
  return (
    <span
      aria-hidden
      className={cn(
        "flex shrink-0 items-center justify-center rounded-md border bg-muted/60 text-muted-foreground",
        size === "sm" && "size-5",
        size === "md" && "size-7",
        size === "lg" && "size-10",
        className,
      )}
    >
      <Users className={size === "lg" ? "size-5" : size === "md" ? "size-3.5" : "size-3"} />
    </span>
  );
}

const pill = "inline-flex h-[1.125rem] shrink-0 items-center gap-1 rounded-[5px] border px-1.5 text-[0.6562rem] font-medium whitespace-nowrap";

/** "person" or the agent's name with a bot icon. */
export function SenderBadge({ m, className }: { m: Pick<ChatMessage, "sender_agent">; className?: string }) {
  if (m.sender_agent) {
    return (
      <span className={cn(pill, "border-brand/25 bg-brand-soft text-brand", className)} title="Written by an agent">
        <Bot className="size-3" aria-hidden />
        agent
      </span>
    );
  }
  return (
    <span className={cn(pill, "border-border text-muted-foreground", className)} title="Written by a person">
      <User className="size-3" aria-hidden />
      person
    </span>
  );
}

const STATE: Record<ChatMessage["state"], { icon: React.ComponentType<{ className?: string }>; label: string; cls: string }> = {
  queued: { icon: Clock3, label: "Queued: they're offline; it goes out when they're back", cls: "text-[color-mix(in_oklch,var(--state-waiting)_75%,var(--foreground))]" },
  sent: { icon: Check, label: "Sent", cls: "text-subtle-foreground" },
  delivered: { icon: CheckCheck, label: "Delivered", cls: "text-brand" },
  failed: { icon: AlertCircle, label: "Not delivered", cls: "text-state-error" },
};

export function DeliveryState({ m, className }: { m: ChatMessage; className?: string }) {
  const s = STATE[m.state] ?? STATE.sent;
  const Icon = s.icon;
  const partial = m.state === "sent" && (m.recipients ?? 0) > 1 && (m.delivered ?? 0) > 0;
  const label = partial ? `Delivered to ${m.delivered} of ${m.recipients}` : s.label;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={cn("inline-flex items-center gap-1", s.cls, className)} aria-label={label}>
          <Icon className="size-3.5" />
          {m.state === "queued" && <span className="text-2xs">Queued</span>}
          {m.state === "failed" && <span className="text-2xs">Not delivered</span>}
        </span>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export function ReachDot({ r, className }: { r: Reachability | "" | undefined; className?: string }) {
  const on = r === "direct" || r === "relayed";
  return (
    <span
      className={cn("inline-block size-2 shrink-0 rounded-full", on ? "bg-state-working" : "bg-subtle-foreground/40", className)}
      title={r === "direct" ? "Online" : r === "relayed" ? "Online (through a relay)" : "Offline"}
      aria-label={on ? "online" : "offline"}
    />
  );
}

export function reachLabel(r: Reachability | "" | undefined): string {
  return r === "direct" ? "Online" : r === "relayed" ? "Online · through a relay" : "Offline";
}

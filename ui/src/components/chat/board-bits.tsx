// Small pieces of chat boards used outside the board itself: the topic chip on a message in
// the chat, and the topic picker in the chat's composer (DESIGN.md "Chat boards").
import { useEffect, useState } from "react";
import { Check, Hash, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { after } from "@/components/board/canvas";
import { NewTopicDialog } from "@/components/board/dialogs";
import { threadColor } from "@/lib/board";
import { chatBoardApi, loadChatBoard, useChatBoards } from "@/lib/chat-board";
import type { Chat, ChatTopic } from "@/lib/chat-types";
import { CHAT_GENERAL_TOPIC } from "@/lib/chat-types";
import { cn } from "@/lib/utils";

/** A chat's topics (General first), loading its board once if nobody has. */
export function useChatTopics(chatId: string): ChatTopic[] | undefined {
  const topics = useChatBoards((s) => s.boards[chatId]?.topics);
  useEffect(() => {
    const s = useChatBoards.getState();
    if (!s.boards[chatId] && !s.errors[chatId]) void loadChatBoard(chatId); // after a failure: the board view retries
  }, [chatId]);
  return topics;
}

/** A message's topic, as a small coloured chip (nothing for General or a topic not known here). */
export function TopicChip({ chatId, topic, className }: { chatId: string; topic?: string; className?: string }) {
  const topics = useChatTopics(chatId);
  const t = topic ? topics?.find((x) => x.id === topic && x.kind !== "general") : undefined;
  if (!t) return null;
  return (
    <span
      title={`Topic: ${t.title}${t.archived ? " (archived)" : ""}`}
      className={cn("inline-flex h-[1.125rem] max-w-44 shrink-0 items-center gap-1 rounded-[5px] border px-1.5 text-[0.6562rem] font-medium text-muted-foreground", className)}
    >
      <span className="size-1.5 shrink-0 rounded-full" style={{ background: threadColor(t) }} />
      <span className="truncate">{t.title}</span>
    </span>
  );
}

// The topic the composer writes in stays picked per chat (people tend to write several lines
// in one topic) until changed.
const picked = new Map<string, string>();

export function useTopicChoice(chatId: string): [string, (id: string) => void] {
  const [v, setV] = useState(() => picked.get(chatId) ?? CHAT_GENERAL_TOPIC);
  return [
    v,
    (id: string) => {
      picked.set(chatId, id);
      setV(id);
    },
  ];
}

/** The composer's topic picker: General, the live topics, or a new one. */
export function TopicPicker({ chat, value, onChange, disabled }: { chat: Chat; value: string; onChange: (id: string) => void; disabled?: boolean }) {
  const topics = useChatTopics(chat.id) ?? [];
  const live = topics.filter((x) => !x.archived);
  const current = live.find((x) => x.id === value) ?? live.find((x) => x.kind === "general");
  const [creating, setCreating] = useState(false);
  useEffect(() => {
    // The picked topic was archived (here or by someone else): back to General.
    if (value !== CHAT_GENERAL_TOPIC && topics.length && !live.some((x) => x.id === value)) onChange(CHAT_GENERAL_TOPIC);
  }, [value, topics.length, live, onChange]);
  const color = current && current.kind !== "general" ? threadColor(current) : undefined;
  return (
    <>
      <DropdownMenu modal={false}>
        <Tooltip>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                disabled={disabled}
                aria-label={`Topic: ${current?.title ?? "General"}`}
                className="h-8 max-w-40 shrink-0 gap-1 rounded-lg px-2 text-muted-foreground hover:text-foreground"
              >
                {color ? <span className="size-2 shrink-0 rounded-full" style={{ background: color }} /> : <Hash className="size-3.5 shrink-0" />}
                <span className="truncate text-xs">{current?.title ?? "General"}</span>
              </Button>
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent>The topic on the chat's board this message goes to</TooltipContent>
        </Tooltip>
        <DropdownMenuContent align="start" className="max-h-80 w-56 overflow-y-auto" onCloseAutoFocus={(e) => e.preventDefault()}>
          <DropdownMenuLabel className="text-2xs font-normal text-muted-foreground">Write in</DropdownMenuLabel>
          {live.map((x) => (
            <DropdownMenuItem key={x.id} onSelect={() => onChange(x.id)}>
              <span className="size-2.5 shrink-0 rounded-full" style={{ background: threadColor(x) }} />
              <span className="truncate">{x.title}</span>
              {x.id === (current?.id ?? CHAT_GENERAL_TOPIC) && <Check className="ml-auto" />}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={after(() => setCreating(true))}>
            <Plus /> New topic…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <NewTopicDialog
        open={creating}
        onOpenChange={setCreating}
        description={`A new string on the board of ${chat.name}. Everyone in the chat sees it.`}
        create={async (title, c) => {
          const t = await chatBoardApi.createTopic(chat.id, title, c);
          onChange(t.id);
        }}
      />
    </>
  );
}

import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
import { ArrowUp, Square } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { AttachButton, AttachChips, DropOverlay, useAttachments } from "@/components/attach";
import { DictateButton } from "@/components/dictate-button";
import { api } from "@/lib/api";
import { MAX_FILE_BYTES, MAX_FILES } from "@/lib/attachments";
import { paths } from "@/lib/router";
import type { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";

const drafts = new Map<string, string>();
const retries = new Map<string, { envelope: string; id: string }>();

export interface ComposerHandle {
  focus(): void;
}

export const Composer = forwardRef<
  ComposerHandle,
  {
    agent: Agent;
    placeholder?: string;
    className?: string;
    autoFocus?: boolean;
    onSent?: () => void;
    minRows?: number;
    /** to the head: the board topic the message goes to */
    thread?: string;
  }
>(function Composer({ agent, placeholder, className, autoFocus, onSent, minRows = 1, thread }, ref) {
  const key = thread ? `${agent.id}:${thread}` : agent.id;
  const [text, setText] = useState(() => drafts.get(key) ?? "");
  const [sending, setSending] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const working = agent.state === "working" || agent.state === "waiting";
  const terminal = agent.mode === "terminal" && agent.harness === "shell"; // a harness's screen gets messages typed in
  const att = useAttachments("agent:" + key, (f) => api.upload(agent.id, f), {
    maxFiles: MAX_FILES,
    maxBytes: MAX_FILE_BYTES,
    disabled: terminal,
  });

  useImperativeHandle(ref, () => ({ focus: () => ta.current?.focus() }));

  useEffect(() => {
    setText(drafts.get(key) ?? "");
  }, [key]);

  useEffect(() => {
    if (autoFocus) ta.current?.focus();
  }, [autoFocus, agent.id]);

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 240) + "px";
  }, [text]);

  const canSend = (!!text.trim() || att.readyIds.length > 0) && !att.uploading && !sending && !terminal;

  async function send() {
    const body = text.trim();
    if (!canSend) return;
    setSending(true);
    const envelope = JSON.stringify([agent.id, body, att.readyIds, thread]);
    let retry = retries.get(key);
    if (retry?.envelope !== envelope) {
      retry = { envelope, id: crypto.randomUUID() };
      retries.set(key, retry);
    }
    try {
      await api.send(agent.id, body, att.readyIds, thread, retry.id);
      retries.delete(key);
      setText("");
      drafts.delete(key);
      att.clear();
      onSent?.();
    } catch (e) {
      toast.error((e as Error).message || "The message wasn't sent.");
    } finally {
      setSending(false);
      ta.current?.focus();
    }
  }

  async function interrupt() {
    try {
      await api.agentAction(agent.id, "interrupt");
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <div
      className={cn(
        "relative rounded-xl border border-border-strong bg-card transition-[border-color,box-shadow] duration-150 focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/25",
        att.dragging && "border-ring ring-2 ring-ring/25",
        className,
      )}
      {...att.dropProps}
    >
      <DropOverlay show={att.dragging} />
      <AttachChips files={att.files} onRemove={att.remove} />
      <textarea
        ref={ta}
        value={text}
        rows={minRows}
        onChange={(e) => {
          setText(e.target.value);
          drafts.set(key, e.target.value);
        }}
        onPaste={(e) => {
          if (terminal || !e.clipboardData.files.length) return;
          e.preventDefault();
          att.add(e.clipboardData.files);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            void send();
          } else if (e.key === "Escape") {
            ta.current?.blur();
          }
        }}
        placeholder={placeholder ?? (terminal ? `Type into ${agent.name}'s terminal from the Terminal tab` : `Message ${agent.name}…`)}
        disabled={terminal}
        aria-label={`Message ${agent.name}`}
        className="scroll-thin block max-h-60 w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-sm leading-relaxed outline-none placeholder:text-subtle-foreground disabled:cursor-not-allowed"
      />
      <div className="flex items-center gap-2 px-2 pb-2">
        <AttachButton
          disabled={terminal}
          onFiles={(f) => {
            att.add(f);
            ta.current?.focus();
          }}
        />
        <DictateButton
          target={ta}
          value={text}
          draft={{ id: "agent:" + key, label: agent.name, path: paths.agent(agent.id), drafts, draftKey: key }}
          disabled={terminal}
          onChange={(v) => {
            setText(v);
            drafts.set(key, v);
          }}
        />
        <span className="hidden min-w-0 truncate text-2xs text-subtle-foreground sm:block">
          {agent.queued > 0
            ? `${agent.queued} queued ${agent.queued === 1 ? "message" : "messages"} · delivered when ${agent.name} is free`
            : "Enter to send · Shift+Enter for a new line"}
        </span>
        <div className="ml-auto flex items-center gap-1.5">
          {working && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="sm" onClick={() => void interrupt()} className="gap-1.5">
                  <Square className="size-3 fill-current" />
                  Interrupt
                </Button>
              </TooltipTrigger>
              <TooltipContent>Stop the current turn; the agent keeps running</TooltipContent>
            </Tooltip>
          )}
          <Button size="icon-sm" onClick={() => void send()} disabled={!canSend} aria-label={att.uploading ? "Uploading…" : "Send"} className="rounded-lg">
            <ArrowUp className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  );
});

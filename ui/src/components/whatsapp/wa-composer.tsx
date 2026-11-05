// The WhatsApp composer: text (Enter sends), a reply, files (paperclip, paste, drop) and
// dictation, built from the same pieces as the chat composer. Files go with the message itself
// (POST …/messages uploads), so "uploading" here only reads them; the daemon sends them on.
import { useLayoutEffect, useRef, useState } from "react";
import { ArrowUp, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { AttachButton, AttachChips, DropOverlay, useAttachments } from "@/components/attach";
import { DictateButton } from "@/components/dictate-button";
import { paths } from "@/lib/router";
import { cn } from "@/lib/utils";
import { upsertWaMessage, waApi, waSummary } from "@/lib/whatsapp";
import type { WaChat, WaMessage, WaUpload } from "@/lib/wa-types";
import { WA_MAX_FILE_BYTES, WA_MAX_FILES } from "@/lib/wa-types";

const drafts = new Map<string, string>();
// Files read for the next message, by the id the attach chips know them by.
const staged = new Map<string, WaUpload>();
let seq = 0;

function sentence(e: string) {
  const t = e.trim();
  return t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?)]$/.test(t) ? "" : ".");
}

export function WaComposer({
  chat,
  ready,
  why,
  replyTo,
  onClearReply,
}: {
  chat: WaChat;
  /** WhatsApp is connected: sending works */
  ready: boolean;
  /** why it can't send right now (shown as the hint) */
  why?: string;
  replyTo?: WaMessage;
  onClearReply: () => void;
}) {
  const [text, setText] = useState(() => drafts.get(chat.jid) ?? "");
  const [sending, setSending] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const att = useAttachments(
    "wa:" + chat.jid,
    async (f) => {
      const id = `wa${++seq}`;
      staged.set(id, f);
      return { id };
    },
    { maxFiles: WA_MAX_FILES, maxBytes: WA_MAX_FILE_BYTES, disabled: !ready },
  );

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 200) + "px";
  }, [text]);

  const canSend = ready && !sending && !att.uploading && (!!text.trim() || att.readyIds.length > 0);

  async function send() {
    if (!canSend) return;
    const body = text.trim();
    const ids = att.readyIds;
    const uploads = ids.map((id) => staged.get(id)).filter((u): u is WaUpload => !!u);
    setSending(true);
    try {
      const m = await waApi.send(chat.jid, {
        ...(body ? { text: body } : {}),
        ...(replyTo ? { reply_to: replyTo.id } : {}),
        ...(uploads.length ? { uploads } : {}),
      });
      if (m?.id) upsertWaMessage(m);
      setText("");
      drafts.delete(chat.jid);
      for (const id of ids) staged.delete(id);
      att.clear();
      onClearReply();
    } catch (e) {
      toast.error(sentence((e as Error).message || "the message wasn't sent"));
    } finally {
      setSending(false);
      ta.current?.focus();
    }
  }

  const hint = !ready ? (why ?? "") : chat.kind === "group" ? "Everyone in the group reads it · Enter to send" : "Sent from your WhatsApp · Enter to send";

  return (
    <div className="shrink-0 px-4 pt-2 pb-4 md:px-6">
      <div className="mx-auto max-w-[51.25rem]">
        <div
          className={cn(
            "relative rounded-xl border border-border-strong bg-card transition-[border-color,box-shadow] duration-150 focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/25",
            att.dragging && "border-ring ring-2 ring-ring/25",
            !ready && "opacity-70",
          )}
          {...att.dropProps}
        >
          <DropOverlay show={att.dragging} />
          {replyTo && (
            <div className="mx-2 mt-2 flex items-start gap-2 rounded-lg border-l-2 border-brand/60 bg-muted/60 py-1.5 pr-1 pl-2.5">
              <div className="min-w-0 flex-1">
                <div className="truncate text-2xs font-semibold text-brand">Replying to {replyTo.from_me ? "yourself" : replyTo.sender_name || chat.name}</div>
                <div className="truncate text-xs text-muted-foreground">{waSummary(replyTo) || "A message"}</div>
              </div>
              <Button variant="ghost" size="icon-xs" aria-label="Don't reply" className="text-muted-foreground" onClick={onClearReply}>
                <X />
              </Button>
            </div>
          )}
          <AttachChips files={att.files} onRemove={att.remove} />
          <textarea
            ref={ta}
            value={text}
            rows={1}
            onChange={(e) => {
              setText(e.target.value);
              drafts.set(chat.jid, e.target.value);
            }}
            onPaste={(e) => {
              if (!ready || !e.clipboardData.files.length) return;
              e.preventDefault();
              att.add(e.clipboardData.files);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape" && replyTo) {
                e.preventDefault();
                onClearReply();
                return;
              }
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void send();
              }
            }}
            placeholder={`Message ${chat.name}…`}
            aria-label={`Message ${chat.name} on WhatsApp`}
            className="scroll-thin block max-h-52 w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-sm leading-relaxed outline-none placeholder:text-subtle-foreground"
          />
          <div className="flex items-center gap-1 px-2 pb-2">
            <AttachButton
              disabled={!ready}
              hint={`Photos, videos and files (up to ${WA_MAX_FILE_BYTES >> 20} MB) · or paste / drop them here`}
              onFiles={(f) => {
                att.add(f);
                ta.current?.focus();
              }}
            />
            <DictateButton
              target={ta}
              value={text}
              draft={{ id: "wa:" + chat.jid, label: chat.name, path: paths.waChat(chat.jid), drafts, draftKey: chat.jid }}
              onChange={(v) => {
                setText(v);
                drafts.set(chat.jid, v);
              }}
            />
            <span className="ml-1 min-w-0 flex-1 truncate text-2xs text-subtle-foreground">{hint}</span>
            <Button size="icon-sm" onClick={() => void send()} disabled={!canSend} aria-label="Send" className="ml-auto rounded-lg">
              <ArrowUp className="size-4" />
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

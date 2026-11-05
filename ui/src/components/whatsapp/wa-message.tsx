// One WhatsApp message in a chat, laid out like tmx's own chats (avatar and name on the first of
// a run, then the text): replies, reactions, ticks, and media previews. Media is fetched on demand
// through the daemon (which downloads it from WhatsApp the first time): small photos and stickers
// load by themselves as they scroll into view, everything else on a click.
import { useEffect, useRef, useState } from "react";
import {
  Ban,
  Copy,
  Download,
  FileText,
  ImageOff,
  Loader2,
  MapPin,
  MessageCircleQuestion,
  Pause,
  Pencil,
  Play,
  Reply,
  UserRound,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PersonAvatar } from "@/components/chat/chat-bits";
import { copyText } from "@/components/copy-button";
import { useMedia } from "@/hooks/use-media";
import { formatSize } from "@/lib/attachments";
import { absoluteTime, clockTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { clock, saveWaMedia, waMediaBlobURL, waMediaStreamURL, waSummary } from "@/lib/whatsapp";
import type { WaChat, WaMessage } from "@/lib/wa-types";
import { WA_AUTOLOAD_BYTES } from "@/lib/wa-types";
import { ExternalLink, firstLink, WaAvatar, WaText, WaTicks } from "./wa-bits";

export function senderLabel(chat: WaChat, m: WaMessage): string {
  if (m.from_me) return "You";
  return m.sender_name || (chat.kind === "dm" ? chat.name : m.sender_jid.split("@")[0]);
}

/** Messages that follow each other from the same person within 5 minutes share a header. */
export function sameRun(a: WaMessage, b: WaMessage): boolean {
  return a.type !== "system" && b.type !== "system" && a.from_me === b.from_me && a.sender_jid === b.sender_jid && b.at - a.at < 5 * 60_000;
}

function useBlob(m: WaMessage, auto: boolean) {
  const [url, setUrl] = useState<string>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const load = async () => {
    if (url || loading) return url;
    setLoading(true);
    setError("");
    try {
      const u = await waMediaBlobURL(m.chat_jid, m.id);
      setUrl(u);
      return u;
    } catch (e) {
      setError((e as Error).message || "Couldn't load it.");
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    if (auto) void load();
  }, [m.id, auto]); // eslint-disable-line react-hooks/exhaustive-deps -- load once per message
  return { url, error, loading, load };
}

/** True once the element comes near the visible part of the list (then it stays true). */
function useNearView<T extends Element>() {
  const ref = useRef<T>(null);
  const [near, setNear] = useState(() => typeof IntersectionObserver === "undefined");
  useEffect(() => {
    const el = ref.current;
    if (!el || near) return;
    const io = new IntersectionObserver(
      (es) => {
        if (es.some((e) => e.isIntersecting)) {
          setNear(true);
          io.disconnect();
        }
      },
      { rootMargin: "600px 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [near]);
  return [ref, near] as const;
}

/** The box a picture takes before it is loaded, from its size, so the list doesn't jump. */
function box(m: WaMessage, max: number) {
  const w = m.media?.width || 4;
  const h = m.media?.height || 3;
  const scale = Math.min(1, max / Math.max(w, h));
  const cw = Math.max(120, Math.round(w * scale));
  return { width: cw, height: Math.round((h / w) * cw) };
}

function WaImage({ m, sticker }: { m: WaMessage; sticker?: boolean }) {
  const auto = (m.media?.size ?? 0) <= WA_AUTOLOAD_BYTES;
  const [ref, near] = useNearView<HTMLButtonElement>();
  const { url, error, loading, load } = useBlob(m, auto && near);
  const [open, setOpen] = useState(false);
  const size = sticker ? { width: 128, height: 128 } : box(m, 300);
  const label = sticker ? "Sticker" : m.media?.name || "Photo";
  if (error) {
    return (
      <div className="flex h-12 max-w-64 items-center gap-2.5 rounded-xl border bg-card px-3 text-xs text-muted-foreground">
        <ImageOff className="size-4 shrink-0" />
        <span className="min-w-0 truncate" title={error}>
          {sticker ? "The sticker couldn't be loaded" : "The photo couldn't be loaded"}
        </span>
        <Button variant="ghost" size="xs" onClick={() => void load()}>
          Try again
        </Button>
      </div>
    );
  }
  return (
    <>
      <button
        ref={ref}
        type="button"
        onClick={() => (url ? setOpen(true) : void load())}
        aria-label={url ? `Show ${label}` : `Load ${label}`}
        style={size}
        className={cn(
          "relative block max-w-full overflow-hidden outline-none focus-visible:ring-2 focus-visible:ring-ring",
          sticker ? "rounded-md" : "rounded-xl border bg-muted transition-opacity duration-100 hover:opacity-95",
        )}
      >
        {url ? (
          <img src={url} alt={m.caption || label} className={cn("size-full", sticker ? "object-contain" : "object-cover")} />
        ) : loading || auto ? (
          <span className="block size-full animate-pulse bg-muted" />
        ) : (
          <span className="flex size-full flex-col items-center justify-center gap-1 text-xs text-muted-foreground">
            <Download className="size-4" />
            Load photo · {formatSize(m.media?.size ?? 0)}
          </span>
        )}
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-[min(92vw,1100px)] gap-3 p-3 sm:max-w-[min(92vw,1100px)]">
          <DialogTitle className="truncate pr-8 text-sm">{m.caption || label}</DialogTitle>
          <DialogDescription className="sr-only">
            {m.sender_name}, {absoluteTime(m.at)}
          </DialogDescription>
          {url && <img src={url} alt={m.caption || label} className="max-h-[78vh] w-full rounded-lg object-contain" />}
        </DialogContent>
      </Dialog>
    </>
  );
}

// One audio at a time, like WhatsApp: starting one pauses the other.
let playingNow: HTMLAudioElement | null = null;

function bars(id: string, n: number): number[] {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return Array.from({ length: n }, (_, i) => {
    h = (h * 1103515245 + 12345) >>> 0;
    const wave = 0.45 + 0.35 * Math.sin(i / 2.3);
    return Math.max(0.18, Math.min(1, wave + ((h % 100) / 100 - 0.5) * 0.6));
  });
}

/** A voice note or an audio file: play/pause, a waveform-like bar that seeks, the time. */
function WaAudio({ m, voice }: { m: WaMessage; voice: boolean }) {
  const { url, error, loading, load } = useBlob(m, false);
  const el = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [pos, setPos] = useState(0);
  const [dur, setDur] = useState((m.media?.duration_ms ?? 0) / 1000);
  const wantPlay = useRef(false);
  const shape = bars(m.id, 32);
  const frac = dur > 0 ? Math.min(1, pos / dur) : 0;

  async function toggle() {
    const a = el.current;
    if (!url) {
      wantPlay.current = true;
      await load();
      return;
    }
    if (!a) return;
    if (a.paused) void a.play().catch(() => undefined);
    else a.pause();
  }

  return (
    <div className={cn("flex w-72 max-w-full items-center gap-2.5 rounded-xl border bg-card py-2 pr-3 pl-2", error && "border-destructive/50")}>
      <Button
        size="icon-sm"
        variant={voice ? "brand" : "secondary"}
        className="shrink-0 rounded-full"
        aria-label={playing ? "Pause" : voice ? "Play the voice message" : "Play"}
        onClick={() => void toggle()}
        disabled={loading}
      >
        {loading ? <Loader2 className="animate-spin" /> : playing ? <Pause /> : <Play className="translate-x-px" />}
      </Button>
      <div className="min-w-0 flex-1">
        {!voice && <div className="truncate pb-0.5 text-xs font-medium">{m.media?.name || "Audio"}</div>}
        <div
          role="slider"
          aria-label="Position"
          aria-valuemin={0}
          aria-valuemax={Math.round(dur)}
          aria-valuenow={Math.round(pos)}
          tabIndex={url ? 0 : -1}
          className={cn("flex h-6 items-center gap-[2px]", url && "cursor-pointer")}
          onClick={(e) => {
            const a = el.current;
            if (!a || !url || !dur) return;
            const r = e.currentTarget.getBoundingClientRect();
            a.currentTime = ((e.clientX - r.left) / r.width) * dur;
          }}
          onKeyDown={(e) => {
            const a = el.current;
            if (!a || !url) return;
            if (e.key === "ArrowRight") a.currentTime = Math.min(dur, a.currentTime + 5);
            if (e.key === "ArrowLeft") a.currentTime = Math.max(0, a.currentTime - 5);
          }}
        >
          {shape.map((v, i) => (
            <span
              key={i}
              className={cn("w-[3px] flex-1 rounded-full transition-colors duration-150", i / shape.length < frac ? "bg-brand" : "bg-muted-foreground/30")}
              style={{ height: `${Math.round(v * 100)}%` }}
            />
          ))}
        </div>
        <div className="tabular flex items-center gap-1.5 pt-0.5 text-2xs text-subtle-foreground">
          {error ? (
            <span className="truncate text-destructive" title={error}>
              Couldn't load it
            </span>
          ) : (
            <span>{playing || pos > 0 ? `${clock(pos * 1000)} / ${clock(dur * 1000)}` : clock(dur * 1000)}</span>
          )}
          {!voice && m.media?.size ? <span>· {formatSize(m.media.size)}</span> : null}
        </div>
      </div>
      {url && (
        <audio
          ref={el}
          src={url}
          preload="metadata"
          onLoadedMetadata={(e) => {
            const a = e.currentTarget;
            if (Number.isFinite(a.duration) && a.duration > 0) setDur(a.duration);
            if (wantPlay.current) {
              wantPlay.current = false;
              void a.play().catch(() => undefined);
            }
          }}
          onPlay={(e) => {
            if (playingNow && playingNow !== e.currentTarget) playingNow.pause();
            playingNow = e.currentTarget;
            setPlaying(true);
          }}
          onPause={() => setPlaying(false)}
          onEnded={() => {
            setPlaying(false);
            setPos(0);
          }}
          onTimeUpdate={(e) => setPos(e.currentTarget.currentTime)}
          className="hidden"
        />
      )}
    </div>
  );
}

function WaVideo({ m }: { m: WaMessage }) {
  const [src, setSrc] = useState<string>();
  const [blob, setBlob] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const size = box(m, 300);

  async function start(asBlob = false) {
    setLoading(true);
    setError("");
    try {
      const stream = asBlob ? null : await waMediaStreamURL(m.chat_jid, m.id).catch(() => null);
      if (stream) setSrc(stream);
      else {
        setSrc(await waMediaBlobURL(m.chat_jid, m.id));
        setBlob(true);
      }
    } catch (e) {
      setError((e as Error).message || "Couldn't load the video.");
    } finally {
      setLoading(false);
    }
  }

  if (src) {
    return (
      <video
        src={src}
        controls
        autoPlay
        playsInline
        style={{ width: size.width }}
        className="max-h-[26.25rem] max-w-full rounded-xl border bg-black"
        onError={() => (blob ? setError("This video can't be played here.") : void start(true))}
      />
    );
  }
  return (
    <button
      type="button"
      onClick={() => void start()}
      disabled={loading}
      aria-label={`Play the video (${formatSize(m.media?.size ?? 0)})`}
      style={size}
      className="relative flex max-w-full items-center justify-center overflow-hidden rounded-xl border bg-neutral-900 text-white outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="flex size-11 items-center justify-center rounded-full bg-white/15 backdrop-blur-sm">
        {loading ? <Loader2 className="size-5 animate-spin" /> : <Play className="size-5 translate-x-px" />}
      </span>
      <span className="tabular absolute bottom-1.5 left-2 text-2xs text-white/80">
        {m.media?.duration_ms ? clock(m.media.duration_ms) + " · " : ""}
        {formatSize(m.media?.size ?? 0)}
      </span>
      {error && <span className="absolute inset-x-2 top-2 truncate rounded bg-black/60 px-1.5 py-0.5 text-2xs text-red-200">{error}</span>}
    </button>
  );
}

function kindOf(mime: string, name?: string): string {
  const ext = name?.includes(".") ? name.split(".").pop()!.toUpperCase() : "";
  if (ext && ext.length <= 5) return ext;
  const sub = mime.split("/")[1]?.split(";")[0] ?? "";
  return sub ? sub.toUpperCase().slice(0, 6) : "File";
}

function WaDocument({ m }: { m: WaMessage }) {
  const [busy, setBusy] = useState(false);
  const name = m.media?.name || "Document";
  return (
    <div className="flex h-12 w-72 max-w-full min-w-0 items-center gap-2.5 rounded-xl border bg-card py-1.5 pr-1.5 pl-2.5">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
        <FileText className="size-4" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium" title={name}>
          {name}
        </span>
        <span className="block truncate text-2xs text-subtle-foreground">
          {kindOf(m.media?.mime ?? "", m.media?.name)} · {formatSize(m.media?.size ?? 0)}
        </span>
      </span>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`Download ${name}`}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await saveWaMedia(m);
              } catch (e) {
                toast.error((e as Error).message || "The file couldn't be downloaded.");
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy ? <Loader2 className="animate-spin" /> : <Download />}
          </Button>
        </TooltipTrigger>
        <TooltipContent>Download</TooltipContent>
      </Tooltip>
    </div>
  );
}

function Chip({ icon: Icon, title, sub, href }: { icon: React.ComponentType<{ className?: string }>; title: string; sub?: string; href?: string }) {
  return (
    <div className="flex min-h-12 w-72 max-w-full min-w-0 items-center gap-2.5 rounded-xl border bg-card py-1.5 pr-3 pl-2.5">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
        <Icon className="size-4" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium" title={title}>
          {title}
        </span>
        {sub && <span className="block truncate text-2xs text-subtle-foreground">{sub}</span>}
      </span>
      {href && (
        <ExternalLink href={href} className="shrink-0 text-xs">
          Open map
        </ExternalLink>
      )}
    </div>
  );
}

function Body({ m }: { m: WaMessage }) {
  if (m.deleted) {
    return (
      <span className="flex items-center gap-1.5 text-[0.8125rem] text-muted-foreground italic">
        <Ban className="size-3.5" />
        {m.from_me ? "You deleted this message" : "This message was deleted"}
      </span>
    );
  }
  const caption = m.caption || (m.type !== "text" && m.type !== "location" && m.type !== "contact" && m.type !== "unsupported" ? m.text : "");
  const media = (() => {
    switch (m.type) {
      case "image":
        return <WaImage m={m} />;
      case "sticker":
        return <WaImage m={m} sticker />;
      case "voice":
        return <WaAudio m={m} voice />;
      case "audio":
        return <WaAudio m={m} voice={false} />;
      case "video":
        return <WaVideo m={m} />;
      case "document":
        return <WaDocument m={m} />;
      case "location": {
        const link = firstLink(m.text);
        const parts = (m.text ?? "").replace(link, "").split("·").map((s) => s.trim()).filter(Boolean);
        return <Chip icon={MapPin} title={parts[0] || "Location"} sub={parts.slice(1).join(" · ") || undefined} href={link || undefined} />;
      }
      case "contact": {
        const [name, ...rest] = (m.text ?? "Contact").split("·").map((s) => s.trim());
        return <Chip icon={UserRound} title={name || "Contact"} sub={rest.join(" · ") || "Contact card"} />;
      }
      case "unsupported":
        return (
          <span className="flex items-start gap-1.5 text-[0.8125rem] text-muted-foreground">
            <MessageCircleQuestion className="mt-0.5 size-3.5 shrink-0" />
            <span>
              {m.text ? <span className="text-foreground/80">{m.text} · </span> : null}
              tmx can't show this kind of message yet. Your phone can.
            </span>
          </span>
        );
      case "reaction":
        return <span className="text-[0.8125rem] text-muted-foreground">{waSummary(m)}</span>;
      default:
        return null;
    }
  })();
  const text = m.type === "text" ? m.text : caption;
  return (
    <div className="flex min-w-0 flex-col items-start gap-1.5">
      {media}
      {text ? <WaText text={text} className="text-[0.8438rem] leading-relaxed" /> : null}
    </div>
  );
}

function Quoted({ q, onJump }: { q: NonNullable<WaMessage["quoted"]>; onJump?: (id: string) => void }) {
  return (
    <button
      type="button"
      onClick={() => onJump?.(q.id)}
      className="mb-1 block max-w-md min-w-0 rounded-md border-l-2 border-brand/60 bg-muted/60 px-2.5 py-1 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="block truncate text-2xs font-semibold text-brand">{q.sender_name || "Someone"}</span>
      <span className="line-clamp-2 text-xs text-muted-foreground [overflow-wrap:anywhere]">{q.text || "A message"}</span>
    </button>
  );
}

function Reactions({ list }: { list: NonNullable<WaMessage["reactions"]> }) {
  const by = new Map<string, string[]>();
  for (const r of list) by.set(r.emoji, [...(by.get(r.emoji) ?? []), r.by]);
  return (
    <div className="flex flex-wrap gap-1 pt-1">
      {[...by.entries()].map(([emoji, people]) => (
        <Tooltip key={emoji}>
          <TooltipTrigger asChild>
            <span className="inline-flex h-6 items-center gap-1 rounded-full border bg-card px-2 text-xs" aria-label={`${emoji} from ${people.join(", ")}`}>
              <span>{emoji}</span>
              {people.length > 1 && <span className="tabular text-2xs text-muted-foreground">{people.length}</span>}
            </span>
          </TooltipTrigger>
          <TooltipContent>{people.join(", ")}</TooltipContent>
        </Tooltip>
      ))}
    </div>
  );
}

export function WaMessageRow({
  chat,
  m,
  first,
  flash,
  onReply,
  onJump,
}: {
  chat: WaChat;
  m: WaMessage;
  first: boolean;
  flash?: boolean;
  onReply?: (m: WaMessage) => void;
  onJump?: (id: string) => void;
}) {
  const touch = useMedia("(hover: none)");
  const [picked, setPicked] = useState(false);
  if (m.type === "system") {
    return (
      <div id={`wa-${m.id}`} className="flex justify-center py-2">
        <span className="max-w-[80%] rounded-full bg-muted/70 px-3 py-1 text-center text-xs text-muted-foreground">{m.text}</span>
      </div>
    );
  }
  const name = senderLabel(chat, m);
  const copyable = m.text || m.caption;
  const canReply = !!onReply && !m.deleted && m.status !== "pending" && m.status !== "failed";
  return (
    <div
      id={`wa-${m.id}`}
      onClick={() => touch && setPicked((v) => !v)}
      className={cn(
        "group/msg relative flex gap-3 rounded-lg px-2 py-0.5 transition-colors duration-500 hover:bg-muted/35",
        first && "mt-3 pt-1.5",
        flash && "bg-brand-soft",
        picked && "bg-muted/40",
      )}
    >
      <div className="w-7 shrink-0">
        {first && (chat.kind === "dm" && !m.from_me ? <WaAvatar chat={chat} /> : <PersonAvatar name={m.from_me ? "You" : name} />)}
      </div>
      <div className="min-w-0 flex-1">
        {first && (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pb-0.5">
            <span className="text-[0.8125rem] font-semibold">{name}</span>
            <span className="tabular text-2xs text-subtle-foreground" title={absoluteTime(m.at)}>
              {clockTime(m.at)}
            </span>
          </div>
        )}
        {m.quoted && <Quoted q={m.quoted} onJump={onJump} />}
        <div className="flex items-end gap-2">
          <div className="min-w-0 flex-1">
            <Body m={m} />
          </div>
          <span className="flex shrink-0 items-center gap-1.5 pb-0.5">
            {m.edited && !m.deleted && (
              <span className="flex items-center gap-0.5 text-2xs text-subtle-foreground" title="Edited">
                <Pencil className="size-2.5" />
                edited
              </span>
            )}
            {!first && (
              <span className="tabular hidden text-2xs text-subtle-foreground group-hover/msg:inline" title={absoluteTime(m.at)}>
                {clockTime(m.at)}
              </span>
            )}
            {m.from_me && <WaTicks m={m} />}
          </span>
        </div>
        {m.reactions && m.reactions.length > 0 && <Reactions list={m.reactions} />}
      </div>
      {(canReply || copyable) && (
        <div
          className={cn(
            "absolute -top-3 right-2 z-10 hidden items-center rounded-md border bg-popover p-0.5 shadow-sm group-hover/msg:flex",
            picked && "flex",
          )}
        >
          {canReply && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="Reply"
                  className="text-muted-foreground hover:text-foreground"
                  onClick={(e) => {
                    e.stopPropagation();
                    setPicked(false);
                    onReply!(m);
                  }}
                >
                  <Reply />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Reply</TooltipContent>
            </Tooltip>
          )}
          {copyable && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="Copy the text"
                  className="text-muted-foreground hover:text-foreground"
                  onClick={async (e) => {
                    e.stopPropagation();
                    setPicked(false);
                    if (await copyText(copyable)) toast.success("Copied.");
                  }}
                >
                  <Copy />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Copy the text</TooltipContent>
            </Tooltip>
          )}
        </div>
      )}
    </div>
  );
}

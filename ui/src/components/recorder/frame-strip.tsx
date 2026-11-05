// A recording's still frames as a strip of thumbnails (DESIGN.md "Recorder"): the key moments
// (key-moments.ts: the start, each screen change, what was pointed at) and every still (taken at
// the start, every 30 s and at stop for the summaries); a click plays the recording from there. Over
// http an <img> loads the daemon's URL with a playback ticket; through the desktop bridge (or
// when that fails) the frame comes through the transport. Thumbnails load as they come into view.
import { useEffect, useRef, useState } from "react";
import { Images, Sparkles } from "lucide-react";
import { frameURL, recApi } from "@/lib/recordings";
import type { Recording, RecordingFrame } from "@/lib/recording-types";
import { clock } from "@/lib/recording-types";
import { keyMoments, place, type Moment, type MomentLine } from "@/lib/key-moments";
import { useStore } from "@/lib/store";

/** The key moments first (with what made them one), then every still. */
export function FrameStrip({ rec, lines, onSeek }: { rec: Recording; lines: MomentLine[]; onSeek: (n: number, ms: number) => void }) {
  const frames = rec.frames ?? [];
  const moments = keyMoments(frames, lines);
  const kind = useStore((s) => s.transport?.kind);
  // a ticket for the <img> URLs (http); null while it comes, false: through the transport
  const [ticket, setTicket] = useState<string | null | false>(null);
  const strip = useRef<HTMLDivElement>(null);
  const row = useRef<HTMLDivElement>(null);
  const has = frames.length > 0;
  useEffect(() => {
    if (!has) return;
    if (kind !== "http") return setTicket(false);
    let gone = false;
    recApi
      .ticket(rec.id)
      .then((t) => !gone && setTicket(t.ticket))
      .catch(() => !gone && setTicket(false));
    return () => {
      gone = true;
    };
  }, [rec.id, kind, has]);
  if (!has) return null;
  const byMs = new Map(frames.map((f) => [f.ms, f]));
  const seek = (ms: number) => {
    const p = place(rec.segments, ms);
    onSeek(p.n, p.ms);
  };
  return (
    <>
      <section aria-label="Key moments" className="space-y-1.5">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Sparkles className="size-3.5" />
          Key moments
          <span className="font-normal text-subtle-foreground">· {moments.length}: the start, each change on the screen, what you pointed at</span>
        </h3>
        <div ref={row} className="scroll-thin flex gap-2 overflow-x-auto pb-1" data-key-moments>
          {moments.map((m) => (
            <figure key={m.ms} className="w-40 shrink-0 space-y-1">
              <Thumb rec={rec} frame={byMs.get(m.ms)!} ticket={ticket} root={row} onSeek={() => seek(m.ms)} large />
              <figcaption className="line-clamp-2 text-2xs text-muted-foreground" title={m.said}>
                {momentWhy(m)}
              </figcaption>
            </figure>
          ))}
        </div>
      </section>
      <section aria-label="Frames" className="space-y-1.5">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Images className="size-3.5" />
          Every still
          <span className="font-normal text-subtle-foreground">· {frames.length}, for summaries and your agents</span>
        </h3>
        <div ref={strip} className="scroll-thin flex gap-1.5 overflow-x-auto pb-1" data-frame-strip>
          {frames.map((f) => (
            <Thumb key={f.ms} rec={rec} frame={f} ticket={ticket} root={strip} onSeek={() => seek(f.ms)} />
          ))}
        </div>
      </section>
    </>
  );
}

function momentWhy(m: Moment): string {
  if (m.why === "said") return `“${m.said}”`;
  return m.why === "start" ? "The start" : "The screen changed";
}

function Thumb({
  rec,
  frame,
  ticket,
  root,
  onSeek,
  large,
}: {
  rec: Recording;
  frame: RecordingFrame;
  ticket: string | null | false;
  root: React.RefObject<HTMLDivElement | null>;
  onSeek: () => void;
  large?: boolean;
}) {
  const el = useRef<HTMLButtonElement>(null);
  const [seen, setSeen] = useState(false);
  const [blob, setBlob] = useState(false); // through the transport
  const [src, setSrc] = useState("");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const b = el.current;
    if (!b || seen) return;
    if (typeof IntersectionObserver === "undefined") return setSeen(true);
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && setSeen(true), { root: root.current, rootMargin: "0px 240px" });
    io.observe(b);
    return () => io.disconnect();
  }, [seen, root]);

  useEffect(() => {
    if (!seen) return;
    if (ticket === null && !blob) return; // the ticket is on its way
    if (ticket && !blob) return setSrc(frameURL(rec.id, frame.ms, ticket));
    let gone = false;
    let url = "";
    recApi
      .frameData(rec.id, frame.ms)
      .then((d) => {
        if (gone) return;
        const bin = atob(d.data);
        const bytes = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        url = URL.createObjectURL(new Blob([bytes], { type: d.type }));
        setSrc(url);
      })
      .catch(() => !gone && setFailed(true));
    return () => {
      gone = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [seen, ticket, blob, rec.id, frame.ms]);

  return (
    <button
      ref={el}
      type="button"
      onClick={onSeek}
      title={`Play from ${clock(frame.ms)}`}
      className={`relative ${large ? "aspect-video w-full" : "h-16 w-28"} shrink-0 overflow-hidden rounded-md border bg-muted outline-none transition-opacity hover:opacity-85 focus-visible:ring-2 focus-visible:ring-ring`}
      data-frame={large ? undefined : frame.ms}
      data-moment={large ? frame.ms : undefined}
    >
      {src && !failed && (
        <img
          src={src}
          alt={`The picture at ${clock(frame.ms)}`}
          className="size-full object-cover"
          onError={() => (blob ? setFailed(true) : (setBlob(true), setSrc("")))}
          draggable={false}
        />
      )}
      <span className="tabular absolute bottom-1 left-1 rounded bg-black/60 px-1 font-mono text-[0.625rem] leading-4 text-white">{clock(frame.ms)}</span>
    </button>
  );
}

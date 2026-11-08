// Recordings (#/recordings, #/recordings/<id>[?seg=N&t=MS]): the owner's screen, camera and
// microphone recordings on this machine. A list, a player that plays the segments in order, the
// still frames, the transcript (from the local transcription service, when it is there), and
// Rename, Share to a chat, Add to Board, Transcribe, Delete. Recording starts from the Record
// button only.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle,
  ArrowLeft,
  CircleDot,
  FileAudio,
  FolderOpen,
  Languages,
  Loader2,
  Mic,
  Monitor,
  MoreHorizontal,
  Pencil,
  Settings2,
  Share2,
  Trash2,
  Video,
  Waypoints,
} from "lucide-react";
import { toast } from "sonner";
import { CopyButton } from "@/components/copy-button";
import { EmptyState, PageBody, PageHeader, PageTitle } from "@/components/page";
import { FrameStrip } from "@/components/recorder/frame-strip";
import { lineAt, segmentStarts, type MomentLine } from "@/lib/key-moments";
import { BoardLinkDialog, DeleteRecordingDialog, fileSize, ShareRecordingDialog } from "@/components/recorder/recording-dialogs";
import { useLiveRecording } from "@/components/recorder/recording-bar";
import { SummaryPanel } from "@/components/recorder/summary-panel";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { useNow } from "@/hooks/use-now";
import { absoluteTime, relativeTime } from "@/lib/format";
import { openRecordDialog } from "@/lib/recorder";
import { recordNow } from "@/lib/record-now";
import { loadRecordings, onTranscriptUpdated, recApi, segmentURL, upsertRecording, useRecordings } from "@/lib/recordings";
import { isClinic, sensitivityOf } from "@/lib/clinic";
import { ClinicCheckbox } from "@/components/recorder/clinic-checkbox";
import type { Recording, RecordingTranscript } from "@/lib/recording-types";
import { captureLabel, clock, elapsedMs, hasSound, isLive, transcriptSegment } from "@/lib/recording-types";
import { href, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { firstSentence, summaryPending, useSummaryBrief } from "@/lib/summaries";
import { ApiError } from "@/lib/transport";
import { cn } from "@/lib/utils";

const KIND_ICON = { screen: Monitor, window: Monitor, audio: Mic, camera: Video } as const;

function StateBadge({ rec }: { rec: Recording }) {
  const map: Record<Recording["state"], [string, string]> = {
    recording: ["Recording", "bg-state-error/15 text-state-error"],
    paused: ["Paused", "bg-state-error/10 text-state-error"],
    done: ["", ""],
    interrupted: ["Interrupted", "bg-state-waiting/20 text-foreground"],
    failed: ["Failed", "bg-state-error/15 text-state-error"],
  };
  const [label, cls] = map[rec.state];
  if (!label) return null;
  return <span className={cn("shrink-0 rounded px-1.5 py-px text-2xs font-medium", cls)}>{label}</span>;
}

function upperFirst(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function hashQuery(): URLSearchParams {
  const h = window.location.hash;
  const i = h.indexOf("?");
  return new URLSearchParams(i >= 0 ? h.slice(i + 1) : "");
}

export function RecordingsPage({ id }: { id?: string }) {
  const list = useRecordings((s) => s.list);
  const loading = useRecordings((s) => s.loading);
  const error = useRecordings((s) => s.error);
  const available = useRecordings((s) => s.available);
  const live = useLiveRecording();
  useNow(30_000);

  useEffect(() => {
    void loadRecordings();
  }, []);

  const selected = useMemo(() => (id ? list?.find((r) => r.id === id) : undefined), [id, list]);
  // A deep link to a recording that is not in the first page of the list.
  const [extra, setExtra] = useState<Recording | null>(null);
  useEffect(() => {
    if (!id || !list || selected) return;
    let gone = false;
    recApi
      .get(id)
      .then((r) => !gone && (upsertRecording(r), setExtra(r)))
      .catch(() => !gone && setExtra(null));
    return () => {
      gone = true;
    };
  }, [id, list, selected]);
  const rec = selected ?? (extra?.id === id ? extra : undefined);

  // ONE tap records (the owner): screen + camera + microphone at once (record-now.ts); the options are beside it
  const busy = !!live.rec || !available;
  const recordButton = (
    <div className="flex items-center gap-1">
      <Button
        variant="destructive"
        size="sm"
        disabled={busy}
        onClick={() => void recordNow().catch((e) => toast.error("The recording didn't start", { description: (e as Error).message, duration: 12000 }))}
        data-record-now
      >
        <span className="size-2 rounded-full bg-white" aria-hidden />
        Record
      </Button>
      <Button variant="ghost" size="icon-sm" disabled={busy} onClick={() => openRecordDialog(true)} aria-label="Recording options" title="Recording options">
        <Settings2 />
      </Button>
    </div>
  );

  return (
    <>
      <PageHeader actions={recordButton}>
        {id && (
          <Button variant="ghost" size="icon-sm" asChild className="lg:hidden" aria-label="All recordings">
            <a href={href(paths.recordings())}>
              <ArrowLeft />
            </a>
          </Button>
        )}
        <PageTitle>Recordings</PageTitle>
        {list && list.length > 0 && <span className="tabular text-xs text-muted-foreground">{list.length}</span>}
      </PageHeader>
      {!available ? (
        <PageBody>
          <EmptyState icon={CircleDot} title="Recordings aren't available">
            This daemon has no recorder. Update tmx on this machine.
          </EmptyState>
        </PageBody>
      ) : loading && !list ? (
        <PageBody>
          <div className="space-y-2">
            <Skeleton className="h-16 rounded-lg" />
            <Skeleton className="h-16 rounded-lg" />
            <Skeleton className="h-16 rounded-lg" />
          </div>
        </PageBody>
      ) : error && !list ? (
        <PageBody>
          <EmptyState icon={AlertTriangle} title="Couldn't load the recordings">
            {error}
          </EmptyState>
        </PageBody>
      ) : !list?.length ? (
        <PageBody>
          {/* ONE Record button (the owner): the header's; the empty state points to it instead of repeating it */}
          <EmptyState icon={CircleDot} title="No recordings yet">
            Tap Record above: it records this device's screen, camera and microphone (on a phone: camera and microphone) until you tap Stop. The options beside it record one
            window, a camera clip or the microphone only. Recordings stay on this machine, and your agents can read them.
          </EmptyState>
        </PageBody>
      ) : (
        <div className="flex min-h-0 flex-1">
          <div className={cn("scroll-thin min-h-0 w-full shrink-0 overflow-y-auto border-r p-2 lg:block lg:w-[20rem]", id ? "hidden" : "block")}>
            <ul className="flex flex-col gap-px" aria-label="Recordings">
              {list.map((r) => (
                <li key={r.id}>
                  <RecordingRow rec={r} active={r.id === id} />
                </li>
              ))}
            </ul>
          </div>
          <div className={cn("min-w-0 flex-1", !id && "hidden lg:block")}>
            {rec ? (
              <RecordingDetail key={rec.id} rec={rec} />
            ) : id ? (
              <PageBody>
                <EmptyState icon={FileAudio} title="No such recording">
                  It may have been deleted.
                </EmptyState>
              </PageBody>
            ) : (
              <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">Pick a recording.</div>
            )}
          </div>
        </div>
      )}
    </>
  );
}

function RecordingRow({ rec, active }: { rec: Recording; active: boolean }) {
  const Icon = KIND_ICON[rec.kind] ?? FileAudio;
  const now = useNow(isLive(rec) ? 1000 : 60_000);
  return (
    <a
      href={href(paths.recording(rec.id))}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex items-start gap-2.5 rounded-md px-2.5 py-2 outline-none transition-colors hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring",
        active && "bg-accent",
      )}
    >
      <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md border bg-card">
        <Icon className="size-3.5 text-muted-foreground" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-1.5">
          <span className="truncate text-[0.8125rem] font-medium">{rec.title}</span>
          <StateBadge rec={rec} />
        </span>
        <span className="tabular block truncate text-2xs text-muted-foreground">
          {relativeTime(rec.started_at, now)} · {clock(isLive(rec) ? elapsedMs(rec, now) : rec.duration_ms)} · {isLive(rec) ? "recording…" : fileSize(rec.bytes)}
        </span>
        <SummaryLine rec={rec} />
      </span>
    </a>
  );
}

/** The first sentence of the recording's summary, or that one is being made. */
function SummaryLine({ rec }: { rec: Recording }) {
  const b = useSummaryBrief(rec);
  if (b?.summary) return <span className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{firstSentence(b.summary)}</span>;
  if (summaryPending(b?.state)) return <span className="mt-0.5 block text-2xs text-subtle-foreground italic">Summarizing…</span>;
  return null;
}

interface SeekRequest {
  n: number;
  ms: number;
  nonce: number;
}

function RecordingDetail({ rec }: { rec: Recording }) {
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState(rec.title);
  const [share, setShare] = useState(false);
  const [board, setBoard] = useState(false);
  const [del, setDel] = useState(false);
  const [seek, setSeek] = useState<SeekRequest | null>(() => {
    const q = hashQuery();
    const n = Number(q.get("seg") || 0);
    return n ? { n, ms: Number(q.get("t") || 0), nonce: 1 } : null;
  });
  const live = isLive(rec);
  const Icon = KIND_ICON[rec.kind] ?? FileAudio;
  const now = useNow(live ? 1000 : 60_000);

  async function rename() {
    const t = title.trim();
    setRenaming(false);
    if (!t || t === rec.title) return setTitle(rec.title);
    try {
      upsertRecording(await recApi.rename(rec.id, t));
    } catch (e) {
      setTitle(rec.title);
      toast.error("Not renamed", { description: (e as Error).message });
    }
  }

  const [savingClinic, setSavingClinic] = useState(false);
  async function setClinic(on: boolean) {
    setSavingClinic(true);
    try {
      upsertRecording(await recApi.setSensitivity(rec.id, sensitivityOf(on)));
    } catch (e) {
      toast.error("Not changed", { description: (e as Error).message });
    } finally {
      setSavingClinic(false);
    }
  }

  const onSeek = useCallback((n: number, ms: number) => setSeek((s) => ({ n, ms, nonce: (s?.nonce ?? 0) + 1 })), []);
  // where the player is (the transcript marks the line being heard)
  const [at, setAt] = useState<{ n: number; ms: number } | null>(null);
  const transcripts = useTranscripts(rec);
  const lines = useMemo(() => momentLines(rec, transcripts.list), [rec, transcripts.list]);

  return (
    <PageBody width="max-w-[55rem]" className="space-y-5">
      <div className="flex items-start gap-3">
        <span className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg border bg-card">
          <Icon className="size-4 text-muted-foreground" />
        </span>
        <div className="min-w-0 flex-1">
          {renaming ? (
            <Input
              autoFocus
              value={title}
              maxLength={120}
              onChange={(e) => setTitle(e.target.value)}
              onBlur={() => void rename()}
              onKeyDown={(e) => {
                if (e.key === "Enter") void rename();
                if (e.key === "Escape") {
                  setTitle(rec.title);
                  setRenaming(false);
                }
              }}
              className="h-8 text-[0.9375rem] font-semibold"
              aria-label="Title"
            />
          ) : (
            <button type="button" className="group flex max-w-full items-center gap-1.5 text-left" onClick={() => (setTitle(rec.title), setRenaming(true))}>
              <h2 className="truncate text-[0.9375rem] font-semibold tracking-tight">{rec.title}</h2>
              <Pencil className="size-3.5 shrink-0 text-subtle-foreground opacity-0 transition-opacity group-hover:opacity-100" />
            </button>
          )}
          <p className="tabular mt-0.5 flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground">
            <span>
              {upperFirst(captureLabel(rec))}
              {rec.kind !== "audio" && !rec.mic ? ", no microphone" : ""}
            </span>
            <span>·</span>
            <span title={absoluteTime(rec.started_at)}>{absoluteTime(rec.started_at)}</span>
            <span>·</span>
            <span>{clock(live ? elapsedMs(rec, now) : rec.duration_ms)}</span>
            {!live && (
              <>
                <span>·</span>
                <span>{fileSize(rec.bytes)}</span>
              </>
            )}
            {rec.segments.length > 1 && (
              <>
                <span>·</span>
                <span>{rec.segments.length} parts</span>
              </>
            )}
            <StateBadge rec={rec} />
          </p>
          <div className="mt-2">
            <ClinicCheckbox checked={isClinic(rec)} onChange={(on) => void setClinic(on)} disabled={savingClinic} />
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="outline" size="sm" onClick={() => setShare(true)} disabled={live || rec.bytes === 0}>
            <Share2 />
            <span className="hidden sm:inline">Share</span>
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm" aria-label="More">
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => (setTitle(rec.title), setRenaming(true))}>
                <Pencil /> Rename
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setBoard(true)}>
                <Waypoints /> Add to Board…
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" disabled={live} onSelect={() => setDel(true)}>
                <Trash2 /> Delete…
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {rec.error && (
        <p className={cn("rounded-md border px-3 py-2 text-xs", rec.state === "done" ? "bg-muted/40 text-muted-foreground" : "border-state-waiting/40 bg-state-waiting/10")}>
          {rec.state === "interrupted" ? "Interrupted: " : rec.state === "failed" ? "Failed: " : ""}
          {rec.error}
        </p>
      )}

      {/* the report (the owner): the summary on top, then the video with its sound, the key moments, the transcript */}
      <SummaryPanel rec={rec} onSeek={onSeek} />

      {live ? (
        <div className="flex aspect-video max-h-[18.75rem] w-full flex-col items-center justify-center gap-2 rounded-lg border bg-muted/30 text-sm text-muted-foreground">
          <span className="size-3 animate-pulse rounded-full bg-state-error" aria-hidden />
          Still recording. It plays here once it is stopped.
        </div>
      ) : rec.segments.some((g) => g.bytes > 0) ? (
        <Player rec={rec} seek={seek} onTime={setAt} />
      ) : null}

      {!live && <FrameStrip rec={rec} lines={lines} onSeek={onSeek} />}

      <TranscriptPane rec={rec} v={transcripts} at={at} onSeek={onSeek} />

      <FilesSection rec={rec} />

      <ShareRecordingDialog rec={rec} open={share} onOpenChange={setShare} />
      <BoardLinkDialog rec={rec} open={board} onOpenChange={setBoard} />
      <DeleteRecordingDialog rec={rec} open={del} onOpenChange={setDel} />
    </PageBody>
  );
}

// ---------------------------------------------------------------------------------------------
// player

type Source = { n: number; url: string; blob: boolean };

/**
 * Plays the segments in order. Over http the media element loads the daemon's URL with a
 * short-lived ticket (Range requests, so seeking works); through the desktop bridge, or when
 * that URL fails to load, the segment comes through the transport as a blob instead.
 */
function Player({ rec, seek, onTime }: { rec: Recording; seek: SeekRequest | null; onTime?: (at: { n: number; ms: number }) => void }) {
  const transport = useStore((s) => s.transport);
  const segs = rec.segments.filter((g) => g.bytes > 0);
  const [n, setN] = useState(segs[0]?.n ?? 1);
  const [src, setSrc] = useState<Source | null>(null);
  const [useBlob, setUseBlob] = useState(transport?.kind === "bridge");
  const [error, setError] = useState("");
  const el = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const ticket = useRef<{ t: string; exp: number } | null>(null);
  const pendingSeek = useRef<number | null>(null);
  const autoplay = useRef(false);
  const retried = useRef(false);
  const fixingDuration = useRef(false);
  const mock = transport?.kind === "mock";

  // load segment n
  useEffect(() => {
    if (mock || !transport) return;
    let gone = false;
    let blobURL = "";
    setError("");
    (async () => {
      try {
        if (useBlob) {
          const d = await recApi.segmentData(rec.id, n);
          const bin = atob(d.data);
          const bytes = new Uint8Array(bin.length);
          for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
          blobURL = URL.createObjectURL(new Blob([bytes], { type: d.type }));
          if (!gone) setSrc({ n, url: blobURL, blob: true });
        } else {
          if (!ticket.current || ticket.current.exp - Date.now() < 30_000) {
            const tk = await recApi.ticket(rec.id);
            ticket.current = { t: tk.ticket, exp: tk.expires_at };
          }
          if (!gone) setSrc({ n, url: segmentURL(rec.id, n, ticket.current.t), blob: false });
        }
      } catch (e) {
        if (!gone) setError((e as Error).message);
      }
    })();
    return () => {
      gone = true;
      if (blobURL) URL.revokeObjectURL(blobURL);
    };
  }, [rec.id, n, useBlob, mock, transport]);

  // a seek from the transcript or a deep link
  useEffect(() => {
    if (!seek) return;
    const v = el.current;
    if (seek.n === n && v && v.readyState >= 1 && !fixingDuration.current) {
      v.currentTime = seek.ms / 1000;
      void v.play().catch(() => {});
    } else {
      pendingSeek.current = seek.ms;
      autoplay.current = true;
      setN(seek.n);
    }
  }, [seek]); // eslint-disable-line react-hooks/exhaustive-deps -- only a new request seeks

  const onLoaded = () => {
    const v = el.current;
    if (!v) return;
    retried.current = false;
    // MediaRecorder's WebM has no duration in its header: let the browser find the end once.
    if (!Number.isFinite(v.duration)) {
      fixingDuration.current = true;
      v.currentTime = 1e101;
      return;
    }
    startAt(v);
  };
  const onDuration = () => {
    const v = el.current;
    if (v && fixingDuration.current && Number.isFinite(v.duration)) {
      fixingDuration.current = false;
      startAt(v);
    }
  };
  const startAt = (v: HTMLMediaElement) => {
    v.currentTime = pendingSeek.current !== null ? pendingSeek.current / 1000 : 0;
    pendingSeek.current = null;
    if (autoplay.current) void v.play().catch(() => {});
    autoplay.current = false;
  };
  const onEnded = () => {
    const i = segs.findIndex((g) => g.n === n);
    if (i >= 0 && i < segs.length - 1) {
      autoplay.current = true;
      setN(segs[i + 1].n);
    }
  };
  const onError = async () => {
    if (!src || src.blob) {
      setError("This part can't be played here.");
      return;
    }
    const at = el.current?.currentTime ?? 0;
    if (!retried.current) {
      // the ticket may have expired: a new one, at the same place
      retried.current = true;
      try {
        const tk = await recApi.ticket(rec.id);
        ticket.current = { t: tk.ticket, exp: tk.expires_at };
        pendingSeek.current = at * 1000;
        setSrc({ n, url: segmentURL(rec.id, n, tk.ticket), blob: false });
        return;
      } catch {
        /* fall through to the blob */
      }
    }
    pendingSeek.current = at * 1000;
    setUseBlob(true); // the window cannot load the daemon's URL: through the transport
  };

  const video = rec.kind !== "audio";
  return (
    <div className="space-y-2">
      {mock ? (
        <div className={cn("flex w-full items-center justify-center rounded-lg bg-black/90 text-xs text-white/70", video ? "aspect-video" : "h-14")}>
          Part {n} of {segs.length} · {clock(segs.find((g) => g.n === n)?.duration_ms ?? 0)} (mock data: no media)
        </div>
      ) : video ? (
        <video
          ref={el}
          key={src?.url ?? "none"}
          src={src?.url}
          controls
          playsInline
          preload="metadata"
          onLoadedMetadata={onLoaded}
          onDurationChange={onDuration}
          onEnded={onEnded}
          onTimeUpdate={(e) => onTime?.({ n, ms: e.currentTarget.currentTime * 1000 })}
          onError={() => void onError()}
          className="aspect-video w-full rounded-lg bg-black"
          data-recording-player
        />
      ) : (
        <audio
          ref={el}
          key={src?.url ?? "none"}
          src={src?.url}
          controls
          preload="metadata"
          onLoadedMetadata={onLoaded}
          onDurationChange={onDuration}
          onEnded={onEnded}
          onTimeUpdate={(e) => onTime?.({ n, ms: e.currentTarget.currentTime * 1000 })}
          onError={() => void onError()}
          className="w-full"
          data-recording-player
        />
      )}
      {error && <p className="text-xs text-state-error">{error}</p>}
      {segs.length > 1 && (
        <div className="flex flex-wrap items-center gap-1.5" aria-label="Parts">
          {segs.map((g) => (
            <button
              key={g.n}
              type="button"
              onClick={() => {
                autoplay.current = true;
                setN(g.n);
              }}
              aria-current={g.n === n ? "true" : undefined}
              className={cn(
                "tabular rounded-md border px-2 py-1 text-xs outline-none transition-colors hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring",
                g.n === n ? "border-border-strong bg-accent font-medium" : "text-muted-foreground",
              )}
            >
              Part {g.n} · {clock(g.duration_ms)}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------------------------
// transcript

type TranscriptView = { loading: boolean; missing: boolean; error: string; list: RecordingTranscript[] };

/** The recording's transcripts (one per part), kept current as parts are transcribed. */
function useTranscripts(rec: Recording): TranscriptView {
  const [v, setV] = useState<TranscriptView>({ loading: true, missing: false, error: "", list: [] });
  const prefix = `recording:${rec.id}/`;

  useEffect(() => {
    let gone = false;
    setV({ loading: true, missing: false, error: "", list: [] });
    recApi
      .transcripts(rec.id)
      .then((l) => !gone && setV({ loading: false, missing: false, error: "", list: (l ?? []).filter((t) => t.source.startsWith(prefix)) }))
      .catch((e) => {
        if (gone) return;
        const missing = e instanceof ApiError && (e.status === 404 || e.status === 501);
        setV({ loading: false, missing, error: missing ? "" : (e as Error).message, list: [] });
      });
    const off = onTranscriptUpdated((tr) => {
      if (!tr.source.startsWith(prefix)) return;
      setV((s) => {
        const list = s.list.filter((x) => x.id !== tr.id && x.source !== tr.source);
        return { ...s, missing: false, list: [...list, tr] };
      });
    });
    return () => {
      gone = true;
      off();
    };
  }, [rec.id, prefix]);
  return v;
}

/** Every transcript line at its time in the whole recording (for the key moments). */
function momentLines(rec: Recording, list: RecordingTranscript[]): MomentLine[] {
  const starts = segmentStarts(rec.segments);
  return list.flatMap((tr) => (tr.segments ?? []).map((s) => ({ ms: (starts.get(transcriptSegment(tr.source)) ?? 0) + s.from_ms, text: s.text })));
}

function TranscriptPane({ rec, v, at, onSeek }: { rec: Recording; v: TranscriptView; at: { n: number; ms: number } | null; onSeek: (n: number, ms: number) => void }) {
  const [busy, setBusy] = useState(false);
  // where each part starts in the whole recording
  const starts = useMemo(() => segmentStarts(rec.segments), [rec.segments]);
  const box = useRef<HTMLDivElement>(null);
  const sorted = [...v.list].sort((a, b) => transcriptSegment(a.source) - transcriptSegment(b.source));

  async function transcribe() {
    setBusy(true);
    try {
      const r = await recApi.transcribe(rec.id);
      toast.success(`Transcribing ${r.queued} ${r.queued === 1 ? "part" : "parts"}`, { description: "On this machine. The text appears here." });
    } catch (e) {
      toast.error("Not transcribed", { description: (e as Error).message, duration: 10000 });
    } finally {
      setBusy(false);
    }
  }

  // keep the line being heard in view, scrolling the transcript only (never the page)
  useEffect(() => {
    const b = box.current;
    const li = b?.querySelector<HTMLElement>("[data-heard]");
    if (!b || !li) return;
    const top = li.offsetTop; // the box is positioned: its own coordinates
    if (top < b.scrollTop || top + li.offsetHeight > b.scrollTop + b.clientHeight) b.scrollTop = Math.max(0, top - b.clientHeight / 3);
  }, [at?.n, at && Math.floor(at.ms / 1000)]); // eslint-disable-line react-hooks/exhaustive-deps -- once a second is enough

  const canTranscribe = hasSound(rec) && !isLive(rec) && rec.segments.some((g) => (g.wav_bytes ?? 0) > 44);
  return (
    <section aria-label="Transcript" className="rounded-xl border bg-card">
      <div className="flex h-10 items-center gap-2 border-b px-3.5">
        <Languages className="size-4 text-muted-foreground" />
        <h3 className="text-[0.8125rem] font-medium">Transcript</h3>
        {canTranscribe && !v.missing && (
          <Button variant="ghost" size="xs" className="ml-auto" onClick={() => void transcribe()} disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            {sorted.length ? "Transcribe again" : "Transcribe"}
          </Button>
        )}
      </div>
      <div ref={box} className="scroll-thin relative max-h-[32.5rem] overflow-y-auto px-3.5 py-3 text-[0.8125rem]" data-transcript>
        {v.loading ? (
          <Skeleton className="h-10" />
        ) : v.missing ? (
          <p className="text-muted-foreground">Transcription isn't available on this daemon yet.</p>
        ) : v.error ? (
          <p className="text-state-error">{v.error}</p>
        ) : !hasSound(rec) ? (
          <p className="text-muted-foreground">No sound in this recording, so there is nothing to transcribe.</p>
        ) : sorted.length === 0 ? (
          <p className="text-muted-foreground">Not transcribed yet. It runs on this machine (Settings → Voice).</p>
        ) : (
          <div className="space-y-3">
            {sorted.map((tr) => {
              const n = transcriptSegment(tr.source);
              const base = starts.get(n) ?? 0;
              const heard = at && at.n === n && tr.segments ? lineAt(tr.segments, at.ms) : -1;
              return (
                <div key={tr.id} data-transcript-part={n}>
                  {sorted.length > 1 && (
                    <p className="mb-1 flex items-center gap-1.5 text-2xs font-medium tracking-wide text-subtle-foreground uppercase">
                      Part {n}
                      {tr.language && <span className="normal-case">· {tr.language}</span>}
                    </p>
                  )}
                  {tr.state === "queued" || tr.state === "running" ? (
                    <p className="flex items-center gap-1.5 text-muted-foreground">
                      <Loader2 className="size-3.5 animate-spin" />
                      {tr.error || (tr.state === "queued" ? "Waiting to be transcribed..." : "Transcribing...")}
                    </p>
                  ) : tr.state === "failed" ? (
                    <p className="text-state-error">{tr.error || "Transcription failed."}</p>
                  ) : tr.segments?.length ? (
                    <ol className="space-y-0.5">
                      {tr.segments.map((s, i) => (
                        <li key={i}>
                          <button
                            type="button"
                            onClick={() => onSeek(n, s.from_ms)}
                            aria-current={i === heard ? "true" : undefined}
                            data-heard={i === heard ? "" : undefined}
                            className={cn(
                              "group flex w-full items-baseline gap-2.5 rounded px-1 py-0.5 text-left outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring",
                              i === heard && "bg-accent font-medium",
                            )}
                          >
                            <span className="tabular w-12 shrink-0 font-mono text-2xs text-subtle-foreground group-hover:text-foreground">{clock(base + s.from_ms)}</span>
                            <span className="min-w-0">{s.text.trim()}</span>
                          </button>
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <p className="whitespace-pre-wrap">{tr.text || "(no speech)"}</p>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------------------------
// files

function FilesSection({ rec }: { rec: Recording }) {
  const [open, setOpen] = useState(false);
  return (
    <section className="text-xs text-muted-foreground">
      <button type="button" className="flex items-center gap-1.5 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <FolderOpen className="size-3.5" />
        Files on this machine
        <span className="text-subtle-foreground">· your agents read them from here</span>
      </button>
      {open && (
        <div className="mt-2 space-y-1.5 rounded-lg border bg-card p-3 font-mono text-[0.7188rem]">
          <div className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate" title={rec.dir}>
              {rec.dir}
            </span>
            <CopyButton text={rec.dir} label="Copy folder" />
          </div>
          {rec.segments.map((g) => (
            <div key={g.n} className="text-subtle-foreground">
              <div className="flex items-center gap-2">
                <span className="tabular w-5 shrink-0">{g.n}</span>
                <span className="min-w-0 flex-1 truncate" title={g.file}>
                  {g.file.split(/[\\/]/).pop()} · {fileSize(g.bytes)}
                </span>
                <CopyButton text={g.file} label="Copy path" />
              </div>
              {g.wav && (
                <div className="flex items-center gap-2 pl-7">
                  <span className="min-w-0 flex-1 truncate" title={g.wav}>
                    {g.wav.split(/[\\/]/).pop()} · 16 kHz · {fileSize(g.wav_bytes ?? 0)}
                  </span>
                  <CopyButton text={g.wav} label="Copy path" />
                </div>
              )}
            </div>
          ))}
          {!!rec.frames?.length && (
            <div className="flex items-center gap-2 text-subtle-foreground">
              <span className="w-5 shrink-0" />
              <span className="min-w-0 flex-1 truncate" title={rec.frames[0].path.replace(/[\\/][^\\/]*$/, "")}>
                frames/ · {rec.frames.length} JPEG{rec.frames.length === 1 ? "" : "s"} · {fileSize(rec.frames.reduce((a, f) => a + f.bytes, 0))}
              </span>
              <CopyButton text={rec.frames[0].path.replace(/[\\/][^\\/]*$/, "")} label="Copy folder" />
            </div>
          )}
        </div>
      )}
    </section>
  );
}

// The recording indicator (DESIGN.md "Recorder"): a bar on every page while a recording runs,
// with the elapsed time, Pause/Resume and Stop, in the window that records and in every other
// client of this daemon ("Recording on this machine"). It also puts "● Recording 12:03 — tmx"
// in the window title and restores the title afterwards.
import { useEffect, useRef, useState } from "react";
import { Loader2, Pause, Play, Square } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useNow } from "@/hooks/use-now";
import { capture, localElapsed, useCapture } from "@/lib/recorder";
import { nativeCapture, useNative, useNativeId } from "@/lib/native-recorder";
import { loadActive, recApi, useRecordings } from "@/lib/recordings";
import { captureLabel, clock, elapsedMs, isLive } from "@/lib/recording-types";
import { useStore } from "@/lib/store";
import { cn } from "@/lib/utils";

/** The live recording as this window should show it: its own capture, else the daemon's. */
export function useLiveRecording() {
  const phase = useCapture((s) => s.phase);
  const local = useCapture((s) => s.rec);
  const base = useCapture((s) => s.base);
  const since = useCapture((s) => s.since);
  const remote = useRecordings((s) => s.active);
  const nativeId = useNativeId();
  const mine = phase !== "idle" && phase !== "starting" && !!local;
  const rec = mine ? local : isLive(remote) ? remote : null;
  const native = !mine && !!rec && rec.id === nativeId; // this window captures it natively (native-recorder.ts)
  const paused = mine ? phase === "paused" : rec?.state === "paused";
  return { mine, native, rec, paused, phase, elapsed: (now: number) => (mine ? localElapsed({ base, since }, now) : rec ? elapsedMs(rec, now) : 0) };
}

type WailsTitle = { WindowSetTitle?: (title: string) => void };

function setWindowTitle(title: string) {
  document.title = title;
  try {
    (window.runtime as unknown as WailsTitle | undefined)?.WindowSetTitle?.(title);
  } catch {
    /* not the desktop app */
  }
}

/** "● Recording 12:03 — tmx" while live; the title from before afterwards. */
function useRecordingTitle(live: boolean, label: () => string) {
  const original = useRef<string | null>(null);
  const labelRef = useRef(label);
  useEffect(() => {
    labelRef.current = label;
    if (live && original.current !== null) setWindowTitle(label()); // a pause shows at once, not on the next tick
  });
  useEffect(() => {
    if (!live) return;
    if (original.current === null) original.current = document.title || "tmx";
    const update = () => setWindowTitle(labelRef.current());
    update();
    const id = setInterval(update, 1000);
    return () => {
      clearInterval(id);
      setWindowTitle(original.current ?? "tmx");
      original.current = null;
    };
  }, [live]);
}

export function RecordingBar() {
  const status = useStore((s) => s.status);
  const { mine, native, rec, paused, phase, elapsed } = useLiveRecording();
  const nativePhase = useNative((s) => s.status?.phase);
  const backlog = useCapture((s) => s.backlog);
  const warning = useCapture((s) => s.warning);
  const [busy, setBusy] = useState(false);
  const now = useNow(1000);

  useEffect(() => {
    if (status === "ready") void loadActive();
  }, [status]);

  const live = !!rec;
  useRecordingTitle(live, () => `● Recording${paused ? " (paused)" : ""} ${clock(elapsed(Date.now()))} — tmx`);
  if (!rec) return null;

  const act = async (what: "pause" | "resume" | "stop") => {
    setBusy(true);
    try {
      if (native) {
        if (what === "pause") await nativeCapture.pause();
        else if (what === "resume") await nativeCapture.resume();
        else await nativeCapture.stop();
      } else if (mine) {
        if (what === "pause") await capture.pause();
        else if (what === "resume") await capture.resume();
        else await capture.stop();
      } else {
        // another window records: the daemon tells it to follow
        await recApi.setState(rec.id, what === "pause" ? "paused" : what === "resume" ? "recording" : "stopped");
        void loadActive();
      }
    } catch (e) {
      toast.error("That didn't work", { description: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };
  const stopping = phase === "stopping" || (native && nativePhase === "stopping") || !!rec.stopping;

  return (
    <div
      role="status"
      aria-live="polite"
      data-recording-bar
      className="flex shrink-0 items-center gap-2.5 border-b border-state-error/30 bg-state-error/10 px-4 py-1.5 text-[0.8125rem] text-foreground md:px-6"
    >
      <span className="relative flex size-2.5 shrink-0" aria-hidden>
        {!paused && <span className="absolute inline-flex size-full animate-ping rounded-full bg-state-error opacity-60" />}
        <span className={cn("relative inline-flex size-2.5 rounded-full bg-state-error", paused && "opacity-50")} />
      </span>
      <span className="shrink-0 font-medium">{stopping ? "Saving…" : paused ? "Paused" : mine || native ? "Recording" : "Recording on this machine"}</span>
      <span className="tabular shrink-0 font-mono text-[0.7812rem]" data-recording-time>
        {clock(elapsed(now))}
      </span>
      <span className="hidden min-w-0 truncate text-muted-foreground sm:inline">
        {rec.title}
        {captureLabel(rec) && ` · ${captureLabel(rec)}`}
      </span>
      {mine && backlog > 1 << 20 && <span className="hidden shrink-0 text-2xs text-muted-foreground md:inline">sending… {(backlog / (1 << 20)).toFixed(1)} MB behind</span>}
      {mine && warning && (
        <span className="hidden min-w-0 truncate text-2xs text-muted-foreground lg:inline" title={warning}>
          {warning}
        </span>
      )}
      <div className="ml-auto flex shrink-0 items-center gap-1">
        {stopping ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground" />
        ) : (
          <>
            <Button variant="ghost" size="sm" className="h-7 gap-1.5 px-2" onClick={() => void act(paused ? "resume" : "pause")} disabled={busy}>
              {paused ? <Play /> : <Pause />}
              <span className="hidden sm:inline">{paused ? "Resume" : "Pause"}</span>
            </Button>
            <Button variant="destructive" size="sm" className="h-7 gap-1.5 px-2.5" onClick={() => void act("stop")} disabled={busy}>
              <Square className="fill-current" />
              Stop
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

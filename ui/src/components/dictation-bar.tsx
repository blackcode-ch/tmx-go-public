// The dictation indicator (#45): a bar on every page while a dictation runs, with the elapsed
// time, the box its words go to (a link back), Stop and Throw away. Shown on whatever page the
// owner is on, so a dictation started elsewhere can always be stopped.
import { useEffect, useState } from "react";
import { Loader2, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cancelDictating, dictationElapsed, stopDictating, useDictation } from "@/lib/dictation-session";
import { navigate } from "@/lib/router";

function clock(ms: number) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export function DictationBar() {
  const phase = useDictation((s) => s.phase);
  const target = useDictation((s) => s.target);
  const [elapsed, setElapsed] = useState(0);

  useEffect(() => {
    if (phase !== "recording") return;
    const tick = () => setElapsed(dictationElapsed());
    tick();
    const id = setInterval(tick, 500);
    return () => clearInterval(id);
  }, [phase]);

  if (phase === "idle" || !target) return null;
  const recording = phase === "recording";

  return (
    <div
      role="status"
      aria-live="polite"
      data-dictation-bar
      className="flex shrink-0 items-center gap-2.5 border-b border-state-error/30 bg-state-error/10 px-4 py-1.5 text-[0.8125rem] text-foreground md:px-6"
    >
      <span className="relative flex size-2.5 shrink-0" aria-hidden>
        {recording && <span className="absolute inline-flex size-full animate-ping rounded-full bg-state-error opacity-60" />}
        <span className="relative inline-flex size-2.5 rounded-full bg-state-error" />
      </span>
      <span className="shrink-0 font-medium">{recording ? "Dictating" : phase === "starting" ? "Waiting for the microphone…" : "Transcribing…"}</span>
      {recording && (
        <span className="tabular shrink-0 font-mono text-[0.7812rem]" data-dictation-time>
          {clock(elapsed)}
        </span>
      )}
      <span className="min-w-0 truncate text-muted-foreground">
        into{" "}
        <button type="button" className="underline-offset-2 hover:text-foreground hover:underline" onClick={() => navigate(target.path)}>
          {target.label}
        </button>
      </span>
      <div className="ml-auto flex shrink-0 items-center gap-1">
        {phase === "working" ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground" />
        ) : (
          <>
            <Button variant="ghost" size="sm" className="h-7 gap-1.5 px-2" onClick={() => cancelDictating()} aria-label="Throw the dictation away">
              <X />
              <span className="hidden sm:inline">Throw away</span>
            </Button>
            {recording && (
              <Button variant="destructive" size="sm" className="h-7 gap-1.5 px-2.5" onClick={() => void stopDictating()}>
                <Square className="fill-current" />
                Stop
              </Button>
            )}
          </>
        )}
      </div>
    </div>
  );
}

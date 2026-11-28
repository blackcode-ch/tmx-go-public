// The dictation button of the message boxes (agent composer, chat composer, WhatsApp): click to
// start and click to stop, or hold it while speaking. Message composers send immediately;
// answer cards keep their separate draft-and-review flow. Transcription runs on this machine (Settings → Voice).
// The dictation itself is the app's (lib/dictation-session.ts): leaving the page keeps it
// running, the bar at the top stops it, and the words still reach this box (#45).
import { useEffect, useRef, useState, type RefObject } from "react";
import { Loader2, Mic, MicOff } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { dictationSupported, insertAtCursor } from "@/lib/dictation";
import {
  cancelDictating,
  dictationLevel,
  registerDictationBox,
  startDictating,
  stopDictating,
  useDictation,
  type DictationTarget,
} from "@/lib/dictation-session";
import { navigate, paths } from "@/lib/router";
import { useStore } from "@/lib/store";
import { loadVoice, useVoice } from "@/lib/voice";
import { cn } from "@/lib/utils";

const HOLD_MS = 450; // a press longer than this is hold-to-talk: letting go stops

let loading = false;

function openVoiceSettings() {
  navigate(paths.settings() + "?section=voice");
}

export function DictateButton({
  target,
  value,
  onChange,
  onSend,
  draft,
  disabled,
  className,
  buttonRef,
}: {
  /** the message box the words go into */
  target: RefObject<HTMLTextAreaElement | null>;
  value: string;
  onChange: (next: string) => void;
  onSend?: (text: string, requestId: string, signal?: AbortSignal) => Promise<void>;
  /** which box this is, app-wide: where the words go when the box has left the screen */
  draft: DictationTarget;
  disabled?: boolean;
  className?: string;
  buttonRef?: RefObject<HTMLButtonElement | null>;
}) {
  const status = useVoice((s) => s.status);
  const loaded = useVoice((s) => s.loaded);
  const connected = useStore((s) => s.status === "ready");
  const phase = useDictation((s) => s.phase);
  const activeId = useDictation((s) => s.target?.id);
  const mine = activeId === draft.id;
  const state = mine ? phase : "idle";
  const [level, setLevel] = useState(0);
  const press = useRef<{ at: number; started: boolean } | null>(null);
  const latest = useRef({ value, onChange, draft, onSend });
  latest.current = { value, onChange, draft, onSend };

  useEffect(() => {
    if (connected && !loaded && !loading) {
      loading = true;
      void loadVoice().finally(() => (loading = false));
    }
  }, [connected, loaded]);

  // This box takes its dictation's words directly while it is on screen.
  useEffect(
    () =>
      registerDictationBox(draft.id, async (text, requestId, signal, review) => {
        if (!review && latest.current.onSend) { await latest.current.onSend(text, requestId, signal); return; }
        const el = target.current;
        const { value: cur, onChange: set } = latest.current;
        const at = el && !review ? [el.selectionStart, el.selectionEnd] : [cur.length, cur.length];
        const next = insertAtCursor(cur, at[0], at[1], text);
        latest.current.value = next.value; // consecutive recovered clips before React rerenders
        set(next.value);
        if (el) {
          el.value = next.value;
          el.setSelectionRange(next.caret, next.caret);
        }
        requestAnimationFrame(() => {
          if (!el) return;
          el.setSelectionRange(next.caret, next.caret);
        });
      }),
    [draft.id, target],
  );

  // The microphone meter; status and timer live outside the action row.
  useEffect(() => {
    if (state !== "recording") return;
    const tick = () => {
      setLevel(dictationLevel());
    };
    tick();
    const id = setInterval(tick, 100);
    return () => clearInterval(id);
  }, [state]);

  if (!loaded || !status) return null; // an older daemon without voice

  const ready = status.ready && dictationSupported();
  const start = () => {
    target.current?.blur();
    void startDictating({ ...latest.current.draft, send: latest.current.onSend });
  };
  const stop = () => {
    if (useDictation.getState().target?.id === draft.id) void stopDictating();
  };
  const cancel = () => {
    if (useDictation.getState().target?.id === draft.id) cancelDictating();
  };

  function explain() {
    if (!dictationSupported()) {
      toast.error("This window cannot use the microphone.");
      return;
    }
    if (status?.engine.available === false) {
      toast("Voice transcription is not available on this machine.", { description: status.reason });
      return;
    }
    toast("Dictation needs voice set up first.", {
      description: "Settings → Voice downloads the speech engine and a model. It all runs on this machine.",
      action: { label: "Set up", onClick: openVoiceSettings },
    });
  }

  const recording = state === "recording";
  const label = !ready
    ? "Dictate: voice is not set up (Settings → Voice)"
    : recording
      ? "Stop dictating"
      : state === "working"
        ? "Transcribing…"
        : "Dictate (click, or hold while speaking)";

  return (
    <div className={cn("flex shrink-0 items-center gap-1", className)}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            ref={buttonRef}
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={disabled || state === "starting" || state === "working"}
            aria-label={label}
            aria-pressed={recording}
            data-dictation={state}
            className={cn(
              "relative shrink-0 rounded-lg text-muted-foreground hover:text-foreground",
              recording && "bg-state-error/12 text-state-error hover:bg-state-error/18 hover:text-state-error",
              !ready && "opacity-70",
            )}
            onPointerDown={(e) => {
              if (e.button !== 0 || !ready) return;
              e.preventDefault();
              (document.activeElement as HTMLElement | null)?.blur?.();
              press.current = { at: Date.now(), started: state === "idle" };
              if (state === "idle") start();
            }}
            onMouseDown={(e) => e.preventDefault()}
            onPointerUp={() => {
              const p = press.current;
              if (p?.started && Date.now() - p.at > HOLD_MS) stop(); // hold-to-talk: letting go stops
            }}
            onClick={() => {
              if (!ready) return explain();
              const p = press.current;
              press.current = null;
              if (p) {
                if (!p.started && recording) stop(); // a second click stops
                return;
              }
              // the keyboard (Enter, Space)
              if (state === "idle") start();
              else if (recording) stop();
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape" && recording) {
                e.preventDefault();
                cancel();
              }
            }}
          >
            {state === "working" || state === "starting" ? (
              <Loader2 className="size-4 animate-spin" />
            ) : ready ? (
              <Mic className="size-4" />
            ) : (
              <MicOff className="size-4" />
            )}
            {recording && (
              <span
                aria-hidden
                className="pointer-events-none absolute inset-0 rounded-lg ring-2 ring-state-error/50 transition-[opacity] duration-100"
                style={{ opacity: 0.25 + level * 0.75 }}
              />
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent>{recording ? "Click to stop · Esc throws it away" : label}</TooltipContent>
      </Tooltip>
    </div>
  );
}

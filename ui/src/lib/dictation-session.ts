// The one dictation of this window (#45). It belongs to the app, not to the mic button that
// started it: switching pages, leaving the box or hiding the window keeps it capturing, and the
// words go to the original destination, even if its composer is no longer on screen.
// It ends only when the owner stops it or throws it away (the mic button, Esc, the bar at the
// top), at the two-minute limit, or when the microphone goes away. The last two keep what was
// heard: message dictation sends through its original composer. Answer cards remain plain drafts.
import { create } from "zustand";
import { toast } from "sonner";
import { DictationError, MAX_DICTATION_MS, insertAtCursor, startDictation, toBase64, type Dictation } from "./dictation";
import { dictationScope, flushDictationOutbox, keepDictation, setDictationDelivery } from "./dictation-outbox";
import { navigate } from "./router";
import { useVoice } from "./voice";
import { useStore } from "./store";

export interface DictationTarget {
  /** unique in the app: "agent:<id>[:topic]", "chat:<id>", "wa:<jid>" */
  id: string;
  /** the agent or chat, shown in the bar */
  label: string;
  /** the page of its message box */
  path: string;
  /** the box's drafts and its key there: written when the box is not on screen */
  drafts: Map<string, string>;
  draftKey: string;
  /** Message composers send separately, without touching their typed draft. */
  send?: (text: string, requestId: string, signal?: AbortSignal) => Promise<void>;
}

export type DictationPhase = "idle" | "starting" | "recording" | "working";

export const useDictation = create<{ phase: DictationPhase; target: { id: string; label: string; path: string } | null }>(() => ({
  phase: "idle",
  target: null,
}));

interface Session {
  scope: string;
  recover: boolean;
  target: DictationTarget;
  rec: Dictation | null; // null while the microphone is being asked for
  ended: boolean; // stopped or thrown away: no second stop, and a late microphone is released
  limit?: ReturnType<typeof setTimeout>;
}

let session: Session | null = null;

// Mounted destinations: message composers send; answer cards insert into their draft.
const boxes = new Map<string, (text: string, requestId: string, signal?: AbortSignal, review?: boolean) => void | Promise<void>>();
const targets = new Map<string, DictationTarget>();

setDictationDelivery(async (clip, signal) => {
  const box = boxes.get(clip.target.id);
  const t = targets.get(clip.target.id);
  if (clip.review) {
    if (!box || !clip.text?.trim()) return false;
    await box(clip.text.trim(), clip.requestId ?? clip.id, signal, true);
    return true;
  }
  if (!clip.send && !box && !t) return false; // after a reload, wait for this composer to mount
  if (clip.text?.trim()) {
    if (clip.send) await clip.send(clip.text.trim(), clip.requestId ?? clip.id, signal);
    else if (box) await box(clip.text.trim(), clip.requestId ?? clip.id, signal);
    else if (t) deliver(t, clip.text.trim());
  } else toast("No words were heard.");
  return true;
});

/** A message box on screen takes its dictation's words directly; returns the unregister. */
export function registerDictationBox(id: string, insert: (text: string, requestId: string, signal?: AbortSignal, review?: boolean) => void | Promise<void>): () => void {
  boxes.set(id, insert);
  void flushDictationOutbox();
  return () => {
    if (boxes.get(id) === insert) boxes.delete(id);
  };
}

export function dictationLevel(): number {
  return session?.rec?.level() ?? 0;
}

export function dictationNoInput(): boolean {
  return session?.rec?.noInput() ?? false;
}

export function dictationElapsed(): number {
  return session?.rec?.elapsed() ?? 0;
}

function finish(s: Session) {
  clearTimeout(s.limit);
  if (session !== s) return;
  session = null;
  useDictation.setState({ phase: "idle", target: null });
}

/** Starts dictating into target; with one already running (anywhere) it says so instead. */
export async function startDictating(target: DictationTarget): Promise<void> {
  if (session) {
    if (session.target.id !== target.id)
      toast(useDictation.getState().phase === "working" ? "The last dictation is still being transcribed." : `Already dictating into ${session.target.label}.`, {
        description: "Stop that one first (the bar at the top).",
      });
    return;
  }
  (document.activeElement as HTMLElement | null)?.blur?.();
  const s: Session = { target, scope: dictationScope(), recover: useStore.getState().status === "ready", rec: null, ended: false };
  targets.set(target.id, target);
  session = s;
  useDictation.setState({ phase: "starting", target: { id: target.id, label: target.label, path: target.path } });
  let rec: Dictation;
  try {
    rec = await startDictation();
  } catch (e) {
    const quiet = s.ended; // thrown away while the microphone was being asked for
    finish(s);
    if (!quiet) toast.error((e as Error).message);
    return;
  }
  if (s.ended) {
    rec.cancel(); // stopped while the microphone was being asked for: nothing was recorded
    return;
  }
  s.rec = rec;
  rec.onEnded(() => {
    if (s.ended) return;
    toast("Microphone interrupted: another app or keyboard voice typing may be using it.");
    void stopDictating();
  });
  if (s.ended) return; // track may already be muted/ended when its callback is registered
  s.limit = setTimeout(() => {
    if (s.ended) return;
    toast("Dictation stops at two minutes.");
    void stopDictating();
  }, MAX_DICTATION_MS);
  useDictation.setState({ phase: "recording" });
}

/** Stops, transcribes, and sends to the original message destination. */
export async function stopDictating(): Promise<void> {
  const s = session;
  if (!s || s.ended) return;
  s.ended = true;
  clearTimeout(s.limit);
  const rec = s.rec;
  if (!rec) {
    finish(s); // still asking for the microphone: nothing to keep
    return;
  }
  useDictation.setState({ phase: "working" });
  try {
    const ms = rec.elapsed();
    const wav = await rec.stop();
    if (ms < 300) {
      console.info("dictation: short capture discarded", { elapsed_ms: Math.round(ms), audio_bytes: wav.byteLength });
      return;
    } // a tap, no speech
    const language = useVoice.getState().status?.settings.language;
    await keepDictation({
      id: crypto.randomUUID(), scope: s.scope, send: s.target.send,
      target: { id: s.target.id, label: s.target.label, path: s.target.path },
      data: toBase64(wav), language: language === "auto" ? undefined : language, created: Date.now(),
      capture: rec.captureMetadata(),
    }, s.recover);
  } catch (e) {
    toast.error(e instanceof DictationError ? e.message : (e as Error).message || "The dictation was not transcribed.");
  } finally {
    try { rec.cancel(); } // defensive cleanup even if stop/encoding/storage failed
    finally { finish(s); }
  }
}

/** Stops and throws the audio away. */
export function cancelDictating(): void {
  const s = session;
  if (!s || s.ended) return;
  s.ended = true;
  s.rec?.cancel();
  finish(s);
}

function deliver(t: DictationTarget, text: string) {
  const box = boxes.get(t.id);
  if (box) {
    void box(text, crypto.randomUUID());
    return;
  }
  const cur = t.drafts.get(t.draftKey) ?? "";
  t.drafts.set(t.draftKey, insertAtCursor(cur, cur.length, cur.length, text).value);
  toast(`The dictation is in your draft to ${t.label}.`, { action: { label: "Open", onClick: () => navigate(t.path) } });
}

// A dictation running in this window asks before the window closes or reloads.
window.addEventListener("beforeunload", (e) => {
  if (session) {
    e.preventDefault();
    e.returnValue = "A dictation is running in this window.";
  }
});

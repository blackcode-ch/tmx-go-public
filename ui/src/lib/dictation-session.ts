// The one dictation of this window (#45). It belongs to the app, not to the mic button that
// started it: switching pages, leaving the box or hiding the window keeps it capturing, and the
// words go to the box it was started in (into that box's draft when it is no longer on screen).
// It ends only when the owner stops it or throws it away (the mic button, Esc, the bar at the
// top), at the two-minute limit, or when the microphone goes away. The last two keep what was
// heard: it goes into the box like a normal stop. Nothing is ever sent by itself.
import { create } from "zustand";
import { toast } from "sonner";
import { DictationError, MAX_DICTATION_MS, insertAtCursor, startDictation, transcribeWAV, type Dictation } from "./dictation";
import { navigate } from "./router";
import { useVoice } from "./voice";

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
}

export type DictationPhase = "idle" | "starting" | "recording" | "working";

export const useDictation = create<{ phase: DictationPhase; target: { id: string; label: string; path: string } | null }>(() => ({
  phase: "idle",
  target: null,
}));

interface Session {
  target: DictationTarget;
  rec: Dictation | null; // null while the microphone is being asked for
  ended: boolean; // stopped or thrown away: no second stop, and a late microphone is released
  limit?: ReturnType<typeof setTimeout>;
}

let session: Session | null = null;

// The message boxes on screen, by target id: each puts the words in at its own cursor.
const boxes = new Map<string, (text: string) => void>();

/** A message box on screen takes its dictation's words directly; returns the unregister. */
export function registerDictationBox(id: string, insert: (text: string) => void): () => void {
  boxes.set(id, insert);
  return () => {
    if (boxes.get(id) === insert) boxes.delete(id);
  };
}

export function dictationLevel(): number {
  return session?.rec?.level() ?? 0;
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
  const s: Session = { target, rec: null, ended: false };
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
    toast("The microphone stopped.", { description: "What was heard so far goes into the message box." });
    void stopDictating();
  });
  s.limit = setTimeout(() => {
    if (s.ended) return;
    toast("Dictation stops at two minutes.");
    void stopDictating();
  }, MAX_DICTATION_MS);
  useDictation.setState({ phase: "recording" });
}

/** Stops, transcribes, and puts the words into the box it was started in. */
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
    if (ms < 300) return; // a tap, no speech
    const res = await transcribeWAV(wav, useVoice.getState().status?.settings.language);
    const text = res.text.trim();
    if (!text) {
      toast("No words were heard.");
      return;
    }
    deliver(s.target, text);
  } catch (e) {
    toast.error(e instanceof DictationError ? e.message : (e as Error).message || "The dictation was not transcribed.");
  } finally {
    finish(s);
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
    box(text);
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

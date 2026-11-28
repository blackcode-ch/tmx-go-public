import type { DictationCapture } from "./dictation-capture";

// In-flight dictations preserve ordering. Failed audio is deleted immediately; held text is transient.
export interface QueuedDictation {
  id: string;
  requestId?: string;
  /** Current-page callback captured at recording time; never persisted. */
  send?: (text: string, requestId: string, signal?: AbortSignal) => Promise<void>;
  scope: string;
  target: { id: string; label: string; path: string };
  data: string;
  language?: string;
  capture?: DictationCapture;
  created: number;
  transcriptId?: string;
  pendingUntil?: number;
  text?: string;
  delivered?: boolean;
  error?: string;
  blocked?: boolean;
  held?: boolean;
  heldReason?: string;
  /** Shown once, alongside the delivered (unaltered) text, e.g. "Fae unreachable, used local". */
  note?: string;
  /** Explicit recovery can show fresh text for review, but never sends it. */
  review?: boolean;
  recoverable?: boolean;
}

export interface DictationStorage {
  list(): Promise<QueuedDictation[]>;
  put(clip: QueuedDictation): Promise<void>;
  remove(id: string): Promise<void>;
}

/** Only the current page's in-flight work, never IndexedDB or Web Storage. */
export class MemoryDictations implements DictationStorage {
  private rows = new Map<string, QueuedDictation>();
  async list() { return [...this.rows.values()].map(c => ({ ...c })); }
  async put(clip: QueuedDictation) { this.rows.set(clip.id, { ...clip }); }
  async remove(id: string) { this.rows.delete(id); }
  clear() { this.rows.clear(); }
}

type Answer = { text: string; held?: boolean; held_reason?: string; note?: string } | { transcript_id: string };
type Transcript = { state: string; text: string; error?: string; held?: boolean; held_reason?: string; note?: string };
export interface DictationRequests {
  transcribe(clip: QueuedDictation): Promise<Answer>;
  transcript(id: string, clip: QueuedDictation): Promise<Transcript>;
}

export const DICTATION_PENDING_MS = 60_000;
export const DICTATION_SEND_MS = 15_000;
export type DictationDelivery = (clip: QueuedDictation, signal?: AbortSignal) => boolean | Promise<boolean>;
export class DictationSendTimeout extends Error {
  constructor() { super("Send uncertain: check the conversation before sending again."); }
}

/** Race as well as abort: a transport ignoring cancellation cannot hold the queue. */
export async function deliverDictation(clip: QueuedDictation, deliver: DictationDelivery) {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      Promise.resolve().then(() => deliver(clip, controller.signal)),
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => {
          const error = new DictationSendTimeout();
          reject(error);
          controller.abort(error);
        }, DICTATION_SEND_MS);
      }),
    ]);
  } finally { clearTimeout(timer); }
}

export class DictationQueue {
  private running = false;
  private again = false;
  private generation = 0;
  /** Invalidates a request finishing after the page's migration/reset. */
  async reset() {
    this.generation++;
    this.running = false;
    for (const clip of await this.storage.list()) await this.storage.remove(clip.id);
  }
  private async save(clip: QueuedDictation) { await this.storage.put(clip); }
  readonly storage: DictationStorage;
  private readonly requests: DictationRequests;
  private readonly notify: (clip: QueuedDictation) => void;
  private readonly transcriptionFinished: (clip: QueuedDictation) => Promise<void>;
  constructor(storage: DictationStorage, requests: DictationRequests, notify: (clip: QueuedDictation) => void = () => {},
    transcriptionFinished: (clip: QueuedDictation) => Promise<void> = async () => {}) {
    this.storage = storage;
    this.requests = requests;
    this.notify = notify;
    this.transcriptionFinished = transcriptionFinished;
  }

  private async drop(clip: QueuedDictation) {
    await this.storage.remove(clip.id);
    // Review text and failure notices are transient; never retain failed audio.
    this.notify({ ...clip, data: "" });
  }

  async enqueue(clip: QueuedDictation) {
    await this.storage.put(clip);
    if (this.running) this.again = true;
  }

  // One bounded pass, triggered at load, reconnect, and periodically. A pending server-side
  // transcript is polled in this page only; reloading never replays dictation.
  async flush(scope: string, deliver: DictationDelivery = () => false) {
    if (this.running) return;
    this.running = true;
    this.again = false;
    const generation = this.generation;
    try {
      const clips = (await this.storage.list()).filter(c => c.scope === scope).sort((a, b) => a.created - b.created);
      for (const clip of clips) {
        if (generation !== this.generation) return;
        // Only in-flight work preserves ordering; rejected clips never block later words.
        if (clip.blocked) { await this.drop(clip); continue; }
        if (clip.text === undefined) {
          try {
            if (clip.transcriptId) {
              clip.pendingUntil ??= clip.created + DICTATION_PENDING_MS;
              if (Date.now() >= clip.pendingUntil) throw new Error("Transcription timed out");
              let timer: ReturnType<typeof setTimeout> | undefined;
              let tr: Transcript;
              try {
                tr = await Promise.race([
                  this.requests.transcript(clip.transcriptId, clip),
                  new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error("Transcription timed out")), Math.max(0, clip.pendingUntil! - Date.now())); }),
                ]);
              } finally { clearTimeout(timer); }
              if (tr.state === "done") { clip.text = tr.text; clip.held = !!tr.held; clip.heldReason = tr.held_reason; clip.note = tr.note; }
              else if (tr.state === "failed") { clip.blocked = true; clip.error = tr.error || "Transcription failed. Recording discarded."; }
            } else {
              const answer = await this.requests.transcribe(clip);
              if ("transcript_id" in answer) { clip.transcriptId = answer.transcript_id; clip.pendingUntil = Date.now() + DICTATION_PENDING_MS; }
              else {
                clip.text = answer.text;
                clip.held = !!answer.held;
                clip.heldReason = answer.held_reason;
                clip.note = answer.note;
              }
            }
            if (clip.text !== undefined && !clip.text.trim()) {
              clip.text = undefined;
              clip.blocked = true;
              clip.error = "No clear speech was detected. Recording discarded.";
            }
            if (!clip.blocked) clip.error = undefined;
          } catch (e) {
            clip.blocked = true;
            const status = (e as { status?: number })?.status;
            clip.error = status === 422 ? "No clear speech was detected. Recording discarded."
              : status === 401 || status === 403 || status === 423 ? "Dictation failed because the session needs to be unlocked or signed in again."
              : status === 0 || status === 502 || status === 503 || status === 504 ? "Dictation failed because transcription was unavailable."
              : (e as Error)?.message === "Transcription timed out" ? "Transcription timed out."
              : "Dictation failed. Please try again.";
          }
          if (generation !== this.generation) return;
          if (clip.blocked || clip.held) await this.transcriptionFinished(clip);
          clip.data = ""; // recovery copies, if allowed, are kept separately from the queue
          if (clip.blocked || clip.held) { await this.drop(clip); continue; }
          await this.save(clip);
        }
        if (clip.text !== undefined && !clip.held && !clip.delivered) {
          try {
            if (await deliverDictation(clip, deliver)) {
              await this.storage.remove(clip.id);
              await this.transcriptionFinished(clip);
              // The text itself is unchanged and already delivered; the note is informational
              // only, shown once alongside it (never gates or re-reviews the delivered words).
              if (clip.note) this.notify({ ...clip, data: "", delivered: true });
            }
          } catch (error) {
            clip.held = true;
            clip.heldReason = error instanceof DictationSendTimeout ? "send_uncertain" : "send_failed";
            clip.error = "The voice message was not confirmed. Check the conversation before sending again.";
            await this.transcriptionFinished(clip);
            await this.drop(clip);
          }
        }
      }
    } finally {
      if (generation === this.generation) {
        this.running = false;
        if (this.again) await this.flush(scope, deliver);
      }
    }
  }
}

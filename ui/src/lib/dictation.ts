// Dictation: the microphone, captured in the page and turned into a 16 kHz mono PCM16 WAV (what
// whisper reads best), sent to POST /api/transcribe; the text comes back for the composer.
// Capture runs through an AudioWorklet (public/dictation-worklet.js), else the older
// ScriptProcessor. Nothing is recorded before the owner presses the button, and the
// microphone is released as soon as they stop. The app-wide session around it (one at a
// time, surviving page switches) is dictation-session.ts.
import { voiceApi, type TranscribeResult } from "./voice";
import { audioCaptureLease } from "./audio-capture-lease";
import { dictationCapturePath, type DictationCapture } from "./dictation-capture";

/** Longest dictation the daemon takes (POST /api/transcribe). */
export const MAX_DICTATION_MS = 120_000;
const RATE = 16_000;
// Cancellation is synchronous to the UI, but a new capture waits for native teardown.
let captureClosing: Promise<void> = Promise.resolve();

export class DictationError extends Error {}

export interface Dictation {
  /** Stops and returns the WAV (16 kHz, mono, 16-bit). */
  stop(): Promise<Uint8Array>;
  /** Metadata for the completed WAV, sampled before stopping the track/context. */
  captureMetadata(): DictationCapture | undefined;
  /** Stops and throws the audio away. */
  cancel(): void;
  /** The input level now, 0..1 (for a meter). */
  level(): number;
  /** True once two seconds of capture have passed without usable input. */
  noInput(): boolean;
  /** Milliseconds recorded so far. */
  elapsed(): number;
  /** Calls fn once if the microphone goes away before stop or cancel (unplugged, revoked). */
  onEnded(fn: () => void): void;
}

/** Whether this page can capture the microphone at all. */
export function dictationSupported(): boolean {
  return typeof navigator !== "undefined" && !!navigator.mediaDevices?.getUserMedia && typeof AudioContext !== "undefined";
}

function micError(e: unknown): DictationError {
  const name = (e as { name?: string })?.name ?? "";
  if (name === "NotAllowedError" || name === "SecurityError")
    return new DictationError("tmx may not use the microphone. Allow it in the system's privacy settings (Microphone), then try again.");
  if (name === "NotFoundError" || name === "OverconstrainedError") return new DictationError("No microphone was found.");
  if (name === "NotReadableError") return new DictationError("The microphone is busy in another app.");
  return new DictationError((e as Error)?.message || "The microphone could not be started.");
}

/** Starts capturing the microphone. */
export async function startDictation(): Promise<Dictation> {
  if (!dictationSupported()) throw new DictationError("This window cannot use the microphone.");
  await captureClosing;
  const releaseLease = await audioCaptureLease();
  let stream: MediaStream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (e) {
    releaseLease();
    throw micError(e);
  }
  let ctx: AudioContext;
  try { ctx = new AudioContext(); }
  catch (e) {
    stream.getTracks().forEach(tr => tr.stop());
    releaseLease();
    throw micError(e);
  }
  let done = false;
  const chunks: Float32Array[] = [];
  let samples = 0;
  let level = 0;
  const started = performance.now();
  let lastInput = started;
  const take = (b: Float32Array) => {
    if (done || !b.length) return;
    chunks.push(b);
    samples += b.length;
    let sum = 0;
    for (let i = 0; i < b.length; i++) sum += b[i] * b[i];
    level = Math.min(1, Math.sqrt(sum / b.length) * 4);
    if (level >= 0.02) lastInput = performance.now();
  };

  let flush: () => Promise<void> = async () => {};
  const nodes: AudioNode[] = [];
  const detach: (() => void)[] = [];
  const path = dictationCapturePath();
  const hiddenAtStart = document.hidden;
  let processor: DictationCapture["processor"] = "script_processor";
  let permission: PermissionStatus | undefined;
  // A query reads the existing state. Unsupported microphone descriptors may reject.
  void (async () => {
    try { permission = await navigator.permissions?.query({ name: "microphone" as PermissionName }); }
    catch { /* unavailable on this browser */ }
  })();
  let contextInterruptions = 0;
  const onContextState = () => {
    if (["suspended", "interrupted"].includes(ctx.state)) contextInterruptions = Math.min(255, contextInterruptions + 1);
  };
  ctx.addEventListener?.("statechange", onContextState);
  detach.push(() => ctx.removeEventListener?.("statechange", onContextState));
  const closeContext = async () => {
    try { await ctx.close(); } catch { /* already closed or interrupted by the browser */ }
  };
  const disconnect = () => {
    for (const clear of detach) {
      try { clear(); } catch { /* a dead worklet must not prevent track cleanup */ }
    }
    for (const n of nodes) {
      try { n.disconnect(); } catch { /* already disconnected */ }
    }
  };
  try {
    await ctx.resume();
    const src = ctx.createMediaStreamSource(stream);
    const mute = ctx.createGain(); // processing nodes must reach the destination to run; silently
    mute.gain.value = 0;
    nodes.push(src, mute);
    mute.connect(ctx.destination);
    let worklet: AudioWorkletNode | null = null;
    if (ctx.audioWorklet) {
      try {
        await ctx.audioWorklet.addModule(new URL("dictation-worklet.js", document.baseURI).href);
        worklet = new AudioWorkletNode(ctx, "tmx-dictation", { numberOfInputs: 1, numberOfOutputs: 1, channelCount: 1 });
      } catch {
        worklet = null; // e.g. the worklet file is not served: the fallback below
      }
    }
    if (worklet) {
      processor = "worklet";
      const w = worklet;
      let flushed: (() => void) | null = null;
      w.port.onmessage = (e: MessageEvent) => {
        if (e.data === "flushed") flushed?.();
        else if (e.data instanceof Float32Array) take(e.data);
      };
      let flushTimer: ReturnType<typeof setTimeout> | undefined;
      detach.push(() => {
        clearTimeout(flushTimer);
        flushed?.();
        flushed = null;
        w.port.onmessage = null;
        w.port.close();
      });
      flush = () =>
        new Promise<void>((resolve) => {
          flushed = () => { clearTimeout(flushTimer); resolve(); };
          flushTimer = setTimeout(flushed, 300);
          w.port.postMessage("flush");
        });
      nodes.push(w);
      src.connect(w);
      w.connect(mute);
    } else {
      const sp = ctx.createScriptProcessor(4096, 1, 1);
      sp.onaudioprocess = (e) => take(e.inputBuffer.getChannelData(0).slice());
      detach.push(() => { sp.onaudioprocess = null; });
      nodes.push(sp);
      src.connect(sp);
      sp.connect(mute);
    }
  } catch (e) {
    done = true;
    disconnect();
    stream.getTracks().forEach((tr) => tr.stop());
    await closeContext();
    releaseLease();
    throw micError(e);
  }

  let ended: (() => void) | null = null;
  let interrupted = false;
  let interruption: DictationCapture["interruption"];
  const track = stream.getAudioTracks()[0];
  const onTrackEnded = (event: Event) => {
    if (!done) interruption ??= event.type === "mute" ? "mute" : "ended";
    if (!done && !interrupted) { interrupted = true; ended?.(); }
  };
  track?.addEventListener("ended", onTrackEnded);
  track?.addEventListener("mute", onTrackEnded);
  // A hidden window keeps capturing; where the browser suspended the audio anyway (WebKit may,
  // when the window is minimized), coming back resumes it.
  let hiddenEvents = 0;
  let visibleEvents = 0;
  const onVisible = () => {
    if (document.hidden) hiddenEvents = Math.min(255, hiddenEvents + 1);
    else visibleEvents = Math.min(255, visibleEvents + 1);
    if (!done && !document.hidden && ctx.state !== "running") void ctx.resume();
  };
  document.addEventListener("visibilitychange", onVisible);
  let closing: Promise<void> | undefined;
  const release = () => {
    if (closing) return closing;
    done = true;
    track?.removeEventListener("ended", onTrackEnded);
    track?.removeEventListener("mute", onTrackEnded);
    document.removeEventListener("visibilitychange", onVisible);
    disconnect();
    stream.getTracks().forEach((tr) => tr.stop());
    // Do not let the next capture race a still-closing audio context.
    closing = closeContext().finally(releaseLease);
    captureClosing = closing;
    return closing;
  };

  let metadata: DictationCapture | undefined;
  return {
    async stop() {
      if (done) throw new DictationError("The dictation has ended.");
      try {
        await flush();
        const state: string = ctx.state;
        const count = Math.floor(samples / (ctx.sampleRate / RATE));
        metadata = {
          path, processor, permission: permission?.state,
          track_muted: track?.muted, track_ended: track ? track.readyState === "ended" : undefined,
          interruption, hidden_at_start: hiddenAtStart, hidden_events: hiddenEvents, visible_events: visibleEvents,
          context_state: state === "running" || state === "suspended" || state === "interrupted" || state === "closed" ? state : "unknown",
          context_interruptions: contextInterruptions, samples: count, duration_ms: Math.round(count * 1000 / RATE),
        };
      } finally {
        await release(); // flush/postMessage errors must never leave the microphone live
      }
      const rate = ctx.sampleRate;
      const all = new Float32Array(samples);
      let o = 0;
      for (const c of chunks) {
        all.set(c, o);
        o += c.length;
      }
      return encodeWAV(resample(all, rate, RATE), RATE);
    },
    captureMetadata: () => metadata,
    cancel() {
      if (!done) void release();
    },
    level: () => performance.now() - lastInput < 350 ? level : 0,
    noInput: () => performance.now() - lastInput >= 2000,
    elapsed: () => performance.now() - started,
    onEnded(fn) {
      ended = fn;
      if (interrupted || track?.readyState === "ended" || track?.muted) {
        interruption ??= track?.readyState === "ended" ? "ended" : "mute";
        interrupted = true;
        fn();
      }
    },
  };
}

/** Downsamples by averaging the input under each output sample (a box filter: enough for speech). */
export function resample(input: Float32Array, from: number, to: number): Float32Array {
  if (from === to) return input;
  const ratio = from / to;
  const n = Math.floor(input.length / ratio);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) {
    const a = Math.floor(i * ratio);
    const b = Math.max(a + 1, Math.min(input.length, Math.floor((i + 1) * ratio)));
    let sum = 0;
    for (let j = a; j < b; j++) sum += input[j];
    out[i] = sum / (b - a);
  }
  return out;
}

/** A mono 16-bit PCM WAV of the samples. */
export function encodeWAV(samples: Float32Array, rate: number): Uint8Array {
  const buf = new ArrayBuffer(44 + samples.length * 2);
  const v = new DataView(buf);
  const str = (o: number, s: string) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  str(0, "RIFF");
  v.setUint32(4, 36 + samples.length * 2, true);
  str(8, "WAVE");
  str(12, "fmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  str(36, "data");
  v.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Uint8Array(buf);
}

export function toBase64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

/** Sends a WAV from stop() to the daemon and returns what was said. */
export function transcribeWAV(wav: Uint8Array, language?: string): Promise<TranscribeResult> {
  return voiceApi.transcribe(toBase64(wav), language && language !== "auto" ? language : undefined);
}

/** Puts text into a textarea's value at its cursor (replacing a selection), with spaces around
 * it where words would otherwise run together. Returns the new value and where the cursor goes. */
export function insertAtCursor(value: string, start: number, end: number, text: string): { value: string; caret: number } {
  const before = value.slice(0, start);
  const after = value.slice(end);
  const pre = before && !/\s$/.test(before) ? " " : "";
  const post = after && !/^[\s.,;:!?)]/.test(after) ? " " : "";
  const ins = pre + text + post;
  return { value: before + ins + after, caret: before.length + pre.length + text.length };
}

// Dictation: the microphone, captured in the page and turned into a 16 kHz mono PCM16 WAV (what
// whisper reads best), sent to POST /api/transcribe; the text comes back for the composer.
// Capture runs through an AudioWorklet (public/dictation-worklet.js), else the older
// ScriptProcessor. Nothing is recorded before the owner presses the button, and the
// microphone is released as soon as they stop. The app-wide session around it (one at a
// time, surviving page switches) is dictation-session.ts.
import { voiceApi, type TranscribeResult } from "./voice";

/** Longest dictation the daemon takes (POST /api/transcribe). */
export const MAX_DICTATION_MS = 120_000;
const RATE = 16_000;

export class DictationError extends Error {}

export interface Dictation {
  /** Stops and returns the WAV (16 kHz, mono, 16-bit). */
  stop(): Promise<Uint8Array>;
  /** Stops and throws the audio away. */
  cancel(): void;
  /** The input level now, 0..1 (for a meter). */
  level(): number;
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
  let stream: MediaStream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (e) {
    throw micError(e);
  }
  const ctx = new AudioContext();
  const chunks: Float32Array[] = [];
  let samples = 0;
  let level = 0;
  const started = performance.now();
  const take = (b: Float32Array) => {
    if (!b.length) return;
    chunks.push(b);
    samples += b.length;
    let sum = 0;
    for (let i = 0; i < b.length; i++) sum += b[i] * b[i];
    level = Math.min(1, Math.sqrt(sum / b.length) * 4);
  };

  let flush: () => Promise<void> = async () => {};
  const nodes: AudioNode[] = [];
  try {
    await ctx.resume();
    const src = ctx.createMediaStreamSource(stream);
    const mute = ctx.createGain(); // processing nodes must reach the destination to run; silently
    mute.gain.value = 0;
    mute.connect(ctx.destination);
    nodes.push(src, mute);
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
      const w = worklet;
      let flushed: (() => void) | null = null;
      w.port.onmessage = (e: MessageEvent) => {
        if (e.data === "flushed") flushed?.();
        else if (e.data instanceof Float32Array) take(e.data);
      };
      flush = () =>
        new Promise<void>((resolve) => {
          flushed = resolve;
          w.port.postMessage("flush");
          setTimeout(resolve, 300);
        });
      src.connect(w);
      w.connect(mute);
      nodes.push(w);
    } else {
      const sp = ctx.createScriptProcessor(4096, 1, 1);
      sp.onaudioprocess = (e) => take(e.inputBuffer.getChannelData(0).slice());
      src.connect(sp);
      sp.connect(mute);
      nodes.push(sp);
    }
  } catch (e) {
    stream.getTracks().forEach((tr) => tr.stop());
    void ctx.close();
    throw micError(e);
  }

  let done = false;
  let ended: (() => void) | null = null;
  const track = stream.getAudioTracks()[0];
  const onTrackEnded = () => {
    if (!done) ended?.();
  };
  track?.addEventListener("ended", onTrackEnded);
  // A hidden window keeps capturing; where the browser suspended the audio anyway (WebKit may,
  // when the window is minimized), coming back resumes it.
  const onVisible = () => {
    if (!done && !document.hidden && ctx.state !== "running") void ctx.resume();
  };
  document.addEventListener("visibilitychange", onVisible);
  const release = () => {
    done = true;
    track?.removeEventListener("ended", onTrackEnded);
    document.removeEventListener("visibilitychange", onVisible);
    for (const n of nodes) {
      try {
        n.disconnect();
      } catch {
        /* already */
      }
    }
    stream.getTracks().forEach((tr) => tr.stop());
    void ctx.close();
  };

  return {
    async stop() {
      if (done) throw new DictationError("The dictation has ended.");
      await flush();
      const rate = ctx.sampleRate;
      release();
      const all = new Float32Array(samples);
      let o = 0;
      for (const c of chunks) {
        all.set(c, o);
        o += c.length;
      }
      return encodeWAV(resample(all, rate, RATE), RATE);
    },
    cancel() {
      if (!done) release();
    },
    level: () => level,
    elapsed: () => performance.now() - started,
    onEnded(fn) {
      ended = fn;
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

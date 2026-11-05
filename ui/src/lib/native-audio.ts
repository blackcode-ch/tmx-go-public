// Device sound from the Windows desktop app (DESIGN.md "Recorder", "Recording on each platform").
// WebView2's getDisplayMedia may give a screen recording no sound of the PC; then the window
// process captures it natively (WASAPI loopback of the default output, internal/devaudio) and
// streams it here as "tmx:devaudio" Wails events of ~100 ms Int16 PCM. An AudioWorklet
// (ScriptProcessorNode where there is none) plays them into a MediaStreamAudioDestinationNode,
// whose track the recorder mixes in like a display stream's own audio track. Nothing is played
// out loud: the speakers would feed the loopback back into itself.
//
// Lifecycle: nativeDeviceAudio() starts the capture (call it only for a recording the owner has
// just started with device sound on, and only when the display stream has no audio track);
// pauseNativeDeviceAudio(true | false) stops and restarts the device at Pause and Resume; the
// track's stop() (or stopNativeDeviceAudio()) ends it. The window stops it when it closes, a page
// that loads stops whatever an earlier page left running, and the window ends a capture whose
// page stops renewing its lease.
import { toast } from "sonner";

/** What DeviceAudioStart answers: the chunks' format. */
export interface DeviceAudioStream {
  rate: number;
  channels: number;
}

/** One "tmx:devaudio" event (internal/devaudio Chunk). */
export interface DeviceAudioChunk {
  seq: number;
  rate: number;
  channels: number;
  frames: number;
  /** base64 of interleaved Int16, little-endian */
  pcm: string;
}

interface DeviceAudioApp {
  DeviceAudioStart(): Promise<DeviceAudioStream>;
  DeviceAudioPing(): Promise<boolean>;
  DeviceAudioStop(): Promise<void>;
}

type EventsOn = (name: string, cb: (...data: unknown[]) => void) => () => void;

export const DEVAUDIO_EVENT = "tmx:devaudio";
export const DEVAUDIO_ENDED_EVENT = "tmx:devaudio-ended";

const PING_MS = 5000; // the window's lease is 60 s
const PRIME_S = 0.15; // sound waits for this much before it plays (chunks come every 100 ms)
const MAX_S = 0.6; // more than this behind: drop the oldest …
const TARGET_S = 0.25; // … down to this

interface Bridge {
  app: DeviceAudioApp;
  on: EventsOn;
}

function bridge(): Bridge | null {
  const app = window.go?.desktop?.App as unknown as Partial<DeviceAudioApp> | undefined;
  const rt = window.runtime;
  if (!app || !rt || typeof rt.EventsOn !== "function") return null;
  if (typeof app.DeviceAudioStart !== "function" || typeof app.DeviceAudioPing !== "function" || typeof app.DeviceAudioStop !== "function") return null;
  return { app: app as DeviceAudioApp, on: rt.EventsOn.bind(rt) };
}

function onWindows(): boolean {
  return /Windows/i.test(navigator.userAgent);
}

/** This window is the Windows app with the native device-sound source (older builds lack it). */
export function hasNativeDeviceAudio(): boolean {
  return onWindows() && bridge() !== null;
}

// ---------------------------------------------------------------------------------------------
// what each platform can do

export type DeviceAudioRoute = "loopback" | "native" | "picker" | "none";

export interface DeviceAudioSupport {
  /** offer the device-sound switch */
  available: boolean;
  /**
   * loopback: the Windows app's own capture (when WebView2's picker gives none); native: the
   * macOS app's recorder; picker: only what the browser's picker shares (known after it);
   * none: no device sound here
   */
  route: DeviceAudioRoute;
  /** one short line for the start dialog */
  hint: string;
}

/**
 * What device sound a recording can have in this window, for an honest start dialog.
 * nativeMac: the macOS app's native recorder can capture sound here (native-rec decides: macOS
 * 15 or later).
 */
export function deviceAudioSupport(env: { desktop: boolean; platform: string; userAgent?: string; nativeMac?: boolean }): DeviceAudioSupport {
  const ua = env.userAgent ?? navigator.userAgent;
  // The app's UI is built into the same exe as its window, so the Windows app always has loopback.
  if (env.desktop && env.platform === "windows") return { available: true, route: "loopback", hint: "Everything this PC plays, from every app." };
  if (env.desktop && env.platform === "darwin")
    return env.nativeMac
      ? { available: true, route: "native", hint: "Everything this Mac plays." }
      : { available: false, route: "none", hint: "Recording this Mac's sound needs macOS 15 or later." };
  if (/Firefox\//.test(ua)) return { available: false, route: "none", hint: "Firefox doesn't share sound with the screen. Chrome or Edge do." };
  const chromium = /Chrome\/|Chromium\/|Edg\//.test(ua);
  if (!chromium) return { available: false, route: "none", hint: "This browser doesn't share sound with the screen. Chrome or Edge do." };
  if (/Windows|CrOS/.test(ua))
    return { available: true, route: "picker", hint: "In the picker, turn on sharing audio: a tab's sound, or the whole PC's with the entire screen." };
  return { available: true, route: "picker", hint: "In the picker, pick a tab and turn on sharing its audio. A window shares no sound." };
}

// ---------------------------------------------------------------------------------------------
// the stream

// The player, as a module loaded from a Blob URL (same origin, no extra file to serve). It turns
// chunks into planar Float32 at the context's rate (linear interpolation when the device's rate
// differs), waits for a small cushion, then plays; when it runs dry (nothing plays on the PC:
// loopback then gives no packets) it plays silence and waits for a cushion again.
const WORKLET = `
class TmxDevAudio extends AudioWorkletProcessor {
  constructor() {
    super();
    this.q = []; this.off = 0; this.have = 0; this.playing = false;
    this.t = 0; this.pl = 0; this.pr = 0;
    this.port.onmessage = (e) => this.add(e.data);
  }
  add(m) {
    if (m === "clear") { this.q = []; this.off = 0; this.have = 0; this.playing = false; this.t = 0; this.pl = 0; this.pr = 0; return; }
    const pcm = new Int16Array(m.pcm), ch = m.channels, n = Math.floor(pcm.length / ch);
    let l = new Float32Array(n), r = new Float32Array(n);
    for (let i = 0; i < n; i++) { l[i] = pcm[i * ch] / 32768; r[i] = ch > 1 ? pcm[i * ch + 1] / 32768 : l[i]; }
    if (m.rate !== sampleRate) {
      const step = m.rate / sampleRate, ol = [], or = [];
      let t = this.t;
      while (t < n - 1) {
        const i = Math.floor(t), f = t - i;
        const al = i < 0 ? this.pl : l[i], ar = i < 0 ? this.pr : r[i];
        ol.push(al + (l[i + 1] - al) * f); or.push(ar + (r[i + 1] - ar) * f);
        t += step;
      }
      this.t = t - n; this.pl = l[n - 1]; this.pr = r[n - 1];
      l = Float32Array.from(ol); r = Float32Array.from(or);
    }
    if (!l.length) return;
    this.q.push([l, r]); this.have += l.length;
    if (this.have > ${MAX_S} * sampleRate)
      while (this.have > ${TARGET_S} * sampleRate && this.q.length > 1) { this.have -= this.q[0][0].length - this.off; this.q.shift(); this.off = 0; }
  }
  process(_inputs, outputs) {
    const L = outputs[0][0], R = outputs[0][1] || L, n = L.length;
    let k = 0;
    if (!this.playing && this.have >= ${PRIME_S} * sampleRate) this.playing = true;
    while (this.playing && k < n && this.q.length) {
      const [bl, br] = this.q[0], m = Math.min(n - k, bl.length - this.off);
      L.set(bl.subarray(this.off, this.off + m), k);
      if (R !== L) R.set(br.subarray(this.off, this.off + m), k);
      k += m; this.off += m; this.have -= m;
      if (this.off === bl.length) { this.q.shift(); this.off = 0; }
    }
    if (k < n) { this.playing = false; L.fill(0, k); if (R !== L) R.fill(0, k); }
    return true;
  }
}
registerProcessor("tmx-devaudio", TmxDevAudio);
`;

interface Feed {
  pcm: ArrayBuffer;
  rate: number;
  channels: number;
}

/** The same player on the main thread (ScriptProcessorNode fallback). */
class Fifo {
  private q: [Float32Array, Float32Array][] = [];
  private off = 0;
  private have = 0;
  private playing = false;
  private t = 0;
  private pl = 0;
  private pr = 0;
  constructor(private readonly rate: number) {}

  add(m: Feed | "clear") {
    if (m === "clear") {
      this.q = [];
      this.off = this.have = this.t = this.pl = this.pr = 0;
      this.playing = false;
      return;
    }
    const pcm = new Int16Array(m.pcm);
    const ch = m.channels;
    const n = Math.floor(pcm.length / ch);
    let l = new Float32Array(n);
    let r = new Float32Array(n);
    for (let i = 0; i < n; i++) {
      l[i] = pcm[i * ch] / 32768;
      r[i] = ch > 1 ? pcm[i * ch + 1] / 32768 : l[i];
    }
    if (m.rate !== this.rate) {
      const step = m.rate / this.rate;
      const ol: number[] = [];
      const or: number[] = [];
      let t = this.t;
      while (t < n - 1) {
        const i = Math.floor(t);
        const f = t - i;
        const al = i < 0 ? this.pl : l[i];
        const ar = i < 0 ? this.pr : r[i];
        ol.push(al + (l[i + 1] - al) * f);
        or.push(ar + (r[i + 1] - ar) * f);
        t += step;
      }
      this.t = t - n;
      this.pl = l[n - 1];
      this.pr = r[n - 1];
      l = Float32Array.from(ol);
      r = Float32Array.from(or);
    }
    if (!l.length) return;
    this.q.push([l, r]);
    this.have += l.length;
    if (this.have > MAX_S * this.rate)
      while (this.have > TARGET_S * this.rate && this.q.length > 1) {
        this.have -= this.q[0][0].length - this.off;
        this.q.shift();
        this.off = 0;
      }
  }

  read(L: Float32Array, R: Float32Array) {
    const n = L.length;
    let k = 0;
    if (!this.playing && this.have >= PRIME_S * this.rate) this.playing = true;
    while (this.playing && k < n && this.q.length) {
      const [bl, br] = this.q[0];
      const m = Math.min(n - k, bl.length - this.off);
      L.set(bl.subarray(this.off, this.off + m), k);
      R.set(br.subarray(this.off, this.off + m), k);
      k += m;
      this.off += m;
      this.have -= m;
      if (this.off === bl.length) {
        this.q.shift();
        this.off = 0;
      }
    }
    if (k < n) {
      this.playing = false;
      L.fill(0, k);
      R.fill(0, k);
    }
  }
}

function b64ToBuffer(s: string): ArrayBuffer {
  const bin = atob(s);
  const out = new Uint8Array(bin.length & ~1);
  for (let i = 0; i < out.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer; // Int16 in the machine's order: little-endian on every Windows PC
}

function isChunk(d: unknown): d is DeviceAudioChunk {
  const c = d as DeviceAudioChunk;
  return !!c && typeof c.pcm === "string" && c.rate > 0 && (c.channels === 1 || c.channels === 2);
}

class Live {
  paused = false;
  ended = false;
  private offs: (() => void)[] = [];
  private timer: ReturnType<typeof setInterval> | undefined;
  private restarting = false;

  constructor(
    private readonly b: Bridge,
    private readonly ctx: AudioContext,
    private readonly feed: (m: Feed | "clear") => void,
    private readonly nodes: AudioNode[],
    readonly track: MediaStreamTrack,
    pingMs: number,
  ) {
    this.offs.push(
      b.on(DEVAUDIO_EVENT, (d) => {
        if (this.paused || this.ended || !isChunk(d)) return;
        const pcm = b64ToBuffer(d.pcm);
        this.feed({ pcm, rate: d.rate, channels: d.channels });
      }),
      b.on(DEVAUDIO_ENDED_EVENT, (reason) => {
        if (this.paused || this.ended) return;
        toast.warning("The device sound stopped", { description: `${String(reason || "The sound device stopped")}. The recording goes on; tmx tries to get it back.`, duration: 10000 });
      }),
    );
    this.timer = setInterval(() => void this.ping(), pingMs);
    // The page's own stop() of the track ends the capture too.
    const stop = track.stop.bind(track);
    track.stop = () => {
      stop();
      this.end();
    };
  }

  /** Renews the lease; a capture that ended by itself (the device went away) is started again. */
  private async ping() {
    if (this.paused || this.ended || this.restarting) return;
    let ok: boolean;
    try {
      ok = await this.b.app.DeviceAudioPing();
    } catch {
      return;
    }
    if (ok || this.paused || this.ended) return;
    this.restarting = true;
    try {
      await this.b.app.DeviceAudioStart();
      if (this.paused || this.ended) void this.b.app.DeviceAudioStop().catch(() => {});
    } catch {
      /* still no device: the next ping tries again */
    } finally {
      this.restarting = false;
    }
  }

  async setPaused(p: boolean) {
    if (this.ended || p === this.paused) return;
    this.paused = p;
    this.feed("clear");
    if (p) {
      await this.b.app.DeviceAudioStop().catch(() => {});
      return;
    }
    try {
      await this.b.app.DeviceAudioStart();
      if (this.paused || this.ended) void this.b.app.DeviceAudioStop().catch(() => {});
    } catch (e) {
      toast.warning("The device sound didn't resume", { description: (e as Error)?.message || String(e) });
    }
  }

  end() {
    if (this.ended) return;
    this.ended = true;
    if (this.timer) clearInterval(this.timer);
    this.offs.forEach((f) => f());
    this.offs = [];
    void this.b.app.DeviceAudioStop().catch(() => {});
    for (const n of this.nodes) {
      try {
        n.disconnect();
      } catch {
        /* already */
      }
    }
    if (this.track.readyState === "live") this.track.stop();
    void this.ctx.close().catch(() => {});
    if (live === this) live = null;
  }
}

let live: Live | null = null;

function newContext(rate: number): AudioContext {
  try {
    return new AudioContext({ sampleRate: rate, latencyHint: "interactive" });
  } catch {
    return new AudioContext({ latencyHint: "interactive" }); // the player resamples
  }
}

/**
 * The PC's own sound as a live audio track, from the Windows app's native capture; null outside
 * the Windows app or when the device can't be opened. Starts the device: call it only for a
 * recording the owner started with device sound, when its display stream has no audio track.
 * (`opts` are for tests: worklet false uses the ScriptProcessorNode fallback.)
 */
export async function nativeDeviceAudio(opts: { worklet?: boolean; pingMs?: number } = {}): Promise<MediaStreamTrack | null> {
  const b = hasNativeDeviceAudio() ? bridge() : null;
  if (!b) return null;
  if (live && !live.ended) return live.track;
  let st: DeviceAudioStream;
  try {
    st = await b.app.DeviceAudioStart();
  } catch {
    return null; // no output device, or this build cannot (the caller says the recording has no device sound)
  }
  let ctx: AudioContext | null = null;
  try {
    ctx = newContext(st.rate);
    if (ctx.state === "suspended") await ctx.resume().catch(() => {});
    const dest = ctx.createMediaStreamDestination();
    let feed: (m: Feed | "clear") => void;
    const nodes: AudioNode[] = [dest];
    if (opts.worklet !== false && ctx.audioWorklet && typeof AudioWorkletNode !== "undefined") {
      const url = URL.createObjectURL(new Blob([WORKLET], { type: "text/javascript" }));
      try {
        await ctx.audioWorklet.addModule(url);
      } finally {
        URL.revokeObjectURL(url);
      }
      const node = new AudioWorkletNode(ctx, "tmx-devaudio", { numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [2] });
      node.connect(dest);
      nodes.push(node);
      feed = (m) => (m === "clear" ? node.port.postMessage(m) : node.port.postMessage(m, [m.pcm]));
    } else {
      const fifo = new Fifo(ctx.sampleRate);
      const proc = ctx.createScriptProcessor(2048, 1, 2);
      proc.onaudioprocess = (e) => fifo.read(e.outputBuffer.getChannelData(0), e.outputBuffer.getChannelData(1));
      const mute = ctx.createGain(); // processing nodes must reach the output to run; nothing is heard
      mute.gain.value = 0;
      proc.connect(dest);
      proc.connect(mute);
      mute.connect(ctx.destination);
      nodes.push(proc, mute);
      feed = (m) => fifo.add(m);
    }
    const track = dest.stream.getAudioTracks()[0];
    if (!track) throw new Error("no track");
    live = new Live(b, ctx, feed, nodes, track, opts.pingMs ?? PING_MS);
    return track;
  } catch {
    void b.app.DeviceAudioStop().catch(() => {});
    void ctx?.close().catch(() => {});
    return null;
  }
}

/** Pause (stops the device) or resume the native device sound; nothing without one. */
export function pauseNativeDeviceAudio(paused: boolean): Promise<void> {
  return live ? live.setPaused(paused) : Promise.resolve();
}

/** Ends the native device sound (stops the device and the track); nothing without one. */
export function stopNativeDeviceAudio(): void {
  live?.end();
}

// A page that loads has no recording yet: stop any capture an earlier page left running (a
// reload during a recording).
if (hasNativeDeviceAudio()) void bridge()?.app.DeviceAudioStop().catch(() => {});

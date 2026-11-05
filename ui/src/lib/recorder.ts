// Capturing a recording in this window (DESIGN.md "Recorder"). Only the owner's click starts
// it: the OS picker for the screen or a window (getDisplayMedia, with the device's own sound
// where the engine gives it), the camera and the microphone (getUserMedia). A MediaRecorder
// makes segments of at most about 20 MB / 10 minutes, each a playable file of its own; chunks
// of about a second stream to the daemon in order (raw bytes over http, base64 through the
// desktop bridge), so memory stays flat. The microphone and the device's sound are mixed into
// the one audio track and tapped as 16 kHz mono PCM16 for the WAV next to every segment, and a
// still JPEG goes up every 30 s for the summaries. Closing the window ends the capture; the
// daemon then marks the recording interrupted and keeps what arrived.
import { create } from "zustand";
import { toast } from "sonner";
import { useStore } from "./store";
import { ApiError, type Transport } from "./transport";
import { mixAudio, pcmBytes, prepareAudioContext, prepareMixContext, startPcmTap, PCM_RATE, type AudioMix, type PcmTap } from "./rec-pcm";
import { CHANGE_EVERY_MS, CHANGE_MIN, FrameGrabber, frameEvery, KEY_GAP_MS, MAX_FRAMES, MAX_KEY_FRAMES } from "./rec-frames";
import { startBubble, type Bubble } from "./rec-bubble";
import { nativeDeviceAudio, pauseNativeDeviceAudio, stopNativeDeviceAudio } from "./native-audio";
import { clinicDefault, sensitivityOf } from "./clinic";
import { loadActive, onRecordingUpdated, recApi, upsertRecording } from "./recordings";
import type { Recording, RecorderSettings, RecordingKind } from "./recording-types";
import { isLive } from "./recording-types";

export type Phase = "idle" | "starting" | "recording" | "paused" | "stopping";

interface LocalState {
  phase: Phase;
  /** the daemon's view of the recording this window captures */
  rec: Recording | null;
  /** recorded ms before `since` */
  base: number;
  /** local time the current stretch began; 0 while paused */
  since: number;
  /** bytes waiting to reach the daemon */
  backlog: number;
  warning: string;
  /** the start dialog */
  dialog: boolean;
  /** the camera's picture while a camera clip records here (the floating preview) */
  preview: MediaStream | null;
}

export const useCapture = create<LocalState>(() => ({ phase: "idle", rec: null, base: 0, since: 0, backlog: 0, warning: "", dialog: false, preview: null }));
const setLocal = useCapture.setState;

export function openRecordDialog(v = true) {
  setLocal({ dialog: v });
}

/** The recorded time of this window's capture now. */
export function localElapsed(s: Pick<LocalState, "base" | "since">, now = Date.now()): number {
  return s.base + (s.since ? now - s.since : 0);
}

// ---------------------------------------------------------------------------------------------
// what this window can do

export function canCaptureScreen(): boolean {
  return !!navigator.mediaDevices?.getDisplayMedia && typeof MediaRecorder !== "undefined";
}
export function canCaptureMic(): boolean {
  return !!navigator.mediaDevices?.getUserMedia && typeof MediaRecorder !== "undefined";
}
export function canCaptureCamera(): boolean {
  return canCaptureMic();
}

/**
 * Whether this engine probably gives the device's own sound with a screen capture: Chromium
 * does (Chrome, Edge, the Windows app's WebView2: a tab's sound, a window's or the whole
 * screen's where the OS allows); WebKit (Safari, the macOS app's web view) and Firefox give
 * none. Only a guess for the dialog's default: the capture itself checks for the track.
 */
export function deviceAudioLikely(): boolean {
  const ua = navigator.userAgent;
  if (/iPhone|iPad|Android/.test(ua)) return false;
  const brands = (navigator as unknown as { userAgentData?: { brands?: { brand: string }[] } }).userAgentData?.brands ?? [];
  if (brands.some((b) => /Chromium|Google Chrome|Microsoft Edge/.test(b.brand))) return true;
  return /\b(Chrome|Chromium|Edg)\//.test(ua) && !/\bFirefox\//.test(ua);
}

const VIDEO_TYPES = ["video/mp4;codecs=avc1,mp4a.40.2", "video/mp4;codecs=avc1", "video/mp4", "video/webm;codecs=vp9,opus", "video/webm;codecs=vp8,opus", "video/webm"];
const VIDEO_ONLY_TYPES = ["video/mp4;codecs=avc1", "video/mp4", "video/webm;codecs=vp9", "video/webm;codecs=vp8", "video/webm"];
const AUDIO_TYPES = ["audio/mp4;codecs=mp4a.40.2", "audio/mp4", "audio/webm;codecs=opus", "audio/ogg;codecs=opus", "audio/webm"];

/** The first type this MediaRecorder makes (hardware H.264/AAC in MP4 first: plays on every platform). */
export function pickMime(video: boolean, audio: boolean): string {
  const list = video ? (audio ? VIDEO_TYPES : VIDEO_ONLY_TYPES) : AUDIO_TYPES;
  for (const t of list) {
    try {
      if (MediaRecorder.isTypeSupported(t)) return t;
    } catch {
      /* ignore */
    }
  }
  return "";
}

// ---------------------------------------------------------------------------------------------
// the session

interface Chunk {
  n: number;
  track: "media" | "pcm";
  offset: number;
  data: Blob | Uint8Array;
  size: number;
  dur: number;
  at: number;
}

const HEARTBEAT_MS = 5000;
// Parts survive an outage (tmx-lead, 3 Nov): a dropped Wi-Fi or a daemon restart (an update) only queues the
// chunks here; they are retried (at most 5 s apart) for up to 10 minutes, the daemon's OutageGrace
// (internal/recorder), before the recording ends with what arrived. ~10 min of screen + camera fit in the cap.
export const MAX_BACKLOG_MS = 10 * 60_000;
export const MAX_BACKLOG_BYTES = 256 << 20;
/** A still frame's next try: a growing pause, at most 15 s, for about 10 minutes. */
export function frameRetryMs(tries: number): number | null {
  return tries >= 45 ? null : Math.min(15_000, 1000 * tries);
}

class Fatal extends Error {}

/** What one recording captures, put together by startRecording. */
interface Capture {
  /** what the MediaRecorder records: at most one video and one audio track */
  stream: MediaStream;
  /** the picture the still frames are taken from (null for a voice recording) */
  picture: MediaStreamTrack | null;
  /** the one audio track (microphone, device audio, or both mixed): the 16 kHz tap reads it */
  sound: MediaStreamTrack | null;
  /** the screen's or the camera's own video track: when it ends ("Stop sharing"), so does the recording */
  video: MediaStreamTrack | null;
  mic: MediaStreamTrack | null;
  /** stops every track and audio node this capture opened */
  release(): void;
}

class Session {
  rec: Recording;
  private t: Transport;
  private settings: RecorderSettings;
  private cap: Capture;
  private stream: MediaStream;
  private mime: string;
  private mr: MediaRecorder | null = null;
  private seg = 1;
  private segBytes = 0;
  private segBase = 0; // this segment's recorded ms before segSince
  private segSince = 0;
  private segDur = new Map<number, number>(); // finished segments' recorded ms
  private maxChunk = 512 << 10;
  private offsets = new Map<string, number>();
  private queue: Chunk[] = [];
  private sending = false;
  private retryMs = 500;
  private pcm: PcmTap | null = null;
  private pcmChunks: Int16Array[] = [];
  private pcmLen = 0;
  private timers: ReturnType<typeof setInterval>[] = [];
  private offUpdates: () => void;
  private recorders = new Set<MediaRecorder>();
  private lastMediaAt = Date.now();
  private stateSeq = 0; // bumped by every pause or resume made here
  private changing = false; // a pause or resume made here is on its way to the daemon
  private stateAt = 0; // the daemon's updated_at of the newest state this window follows
  private grabber: FrameGrabber | null;
  private frameEvery = frameEvery();
  private nextFrameAt = 0; // the recorded ms of the next periodic frame
  private frameCount = 0;
  private framesAt = new Set<number>(); // the recorded ms of the frames taken
  // key moments: the screen's own picture (not the camera bubble) is looked at every 2 s for a change
  private looker: FrameGrabber | null;
  private lookAt = 0;
  private looking = false;
  private keyAt = 0;
  private keyCount = 0;
  private frameQ: { ms: number; blob: Blob; tries: number; key: boolean }[] = [];
  private frameSending = false;
  private frameWarned = false;
  ended = false;

  constructor(o: { rec: Recording; t: Transport; settings: RecorderSettings; cap: Capture; mime: string }) {
    this.rec = o.rec;
    this.t = o.t;
    this.settings = o.settings;
    this.cap = o.cap;
    this.stream = o.cap.stream;
    this.mime = o.mime;
    this.grabber = o.cap.picture ? new FrameGrabber(o.cap.picture) : null;
    this.looker = (o.rec.kind === "screen" || o.rec.kind === "window") && o.cap.video ? new FrameGrabber(o.cap.video) : null;
    this.stateAt = o.rec.updated_at;
    this.offUpdates = onRecordingUpdated((r) => r.id === this.rec.id && this.follow(r));
  }

  async start(ctx: AudioContext | null) {
    const now = Date.now();
    this.segSince = now;
    this.mr = this.newRecorder(this.seg);
    this.mr.start(1000);
    if (this.cap.sound) {
      try {
        this.pcm = await startPcmTap(ctx, new MediaStream([this.cap.sound]), (s) => this.onPCM(s));
      } catch (e) {
        setLocal({ warning: "The 16 kHz sound track is not being recorded: " + (e as Error).message });
      }
    } else void ctx?.close().catch(() => {});
    // "Stop sharing", or the camera went away
    this.cap.video?.addEventListener("ended", () => void this.stop());
    this.cap.mic?.addEventListener("ended", () => {
      if (this.rec.kind === "audio") void this.stop();
      else setLocal({ warning: `The microphone stopped; the ${this.rec.kind === "camera" ? "camera" : "screen"} is still being recorded.` });
    });
    this.timers.push(setInterval(() => this.tick(), 1000));
    this.timers.push(setInterval(() => void this.heartbeat(), HEARTBEAT_MS));
    setLocal({ phase: "recording", rec: this.rec, base: 0, since: now, backlog: 0, warning: useCapture.getState().warning });
    this.nextFrameAt = this.frameEvery;
    void this.frame(0);
  }

  private newRecorder(n: number): MediaRecorder {
    const kbps = this.settings.video_kbps;
    // A keyframe every second: without one Chromium's MP4 muxer keeps the whole recording in
    // memory until stop (nothing would stream, and a crash would lose it all).
    const opts: MediaRecorderOptions & { videoKeyFrameIntervalDuration?: number } = {
      audioBitsPerSecond: this.rec.kind === "audio" ? 48_000 : this.rec.system_audio ? 96_000 : 64_000,
    };
    if (this.mime) opts.mimeType = this.mime;
    if (this.rec.kind !== "audio") {
      opts.videoBitsPerSecond = kbps * 1000;
      opts.videoKeyFrameIntervalDuration = 1000;
    }
    const mr = new MediaRecorder(this.stream, opts);
    this.recorders.add(mr);
    mr.ondataavailable = (e) => {
      if (!e.data || e.data.size === 0 || this.ended) return;
      this.lastMediaAt = Date.now();
      if (n === this.seg) this.segBytes += e.data.size;
      this.maxChunk = Math.max(this.maxChunk, e.data.size);
      this.enqueue(n, "media", e.data, e.data.size);
    };
    mr.onstop = () => this.recorders.delete(mr);
    mr.onerror = (e) => void this.abort("The recorder failed: " + ((e as unknown as { error?: Error }).error?.message ?? "unknown error"));
    return mr;
  }

  /** this segment's recorded ms */
  private segMs(now = Date.now()): number {
    return this.segBase + (this.segSince ? now - this.segSince : 0);
  }

  private enqueue(n: number, track: "media" | "pcm", data: Blob | Uint8Array, size: number) {
    const key = n + track;
    const offset = this.offsets.get(key) ?? 0;
    this.offsets.set(key, offset + size);
    const dur = n === this.seg ? this.segMs() : (this.segDur.get(n) ?? 0);
    this.queue.push({ n, track, offset, data, size, dur, at: Date.now() });
    this.updateBacklog();
    void this.pump();
  }

  private updateBacklog() {
    const bytes = this.queue.reduce((a, c) => a + c.size, 0);
    setLocal({ backlog: bytes });
    const oldest = this.queue[0];
    if (oldest && (Date.now() - oldest.at > MAX_BACKLOG_MS || bytes > MAX_BACKLOG_BYTES))
      void this.abort("The daemon stopped taking the recording, so it ended. What arrived before is kept.");
  }

  private async pump() {
    if (this.sending) return;
    this.sending = true;
    try {
      while (this.queue.length && !this.ended) {
        const c = this.queue[0];
        try {
          await this.send(c);
          this.queue.shift();
          this.retryMs = 500;
        } catch (e) {
          if (e instanceof Fatal) {
            void this.abort(e.message);
            return;
          }
          const err = e as ApiError;
          const size = typeof err.data?.size === "number" ? (err.data.size as number) : -1;
          if (err.status === 409 && size >= 0) {
            // A gap: the daemon has less than this chunk assumes. Drop what can no longer line up.
            if (size < c.offset + c.size) {
              this.queue = this.queue.filter((x) => !(x.n === c.n && x.track === c.track));
              setLocal({ warning: `Part of segment ${c.n} did not reach the daemon.` });
            } else this.queue.shift();
            continue;
          }
          if (err.status === 400 || err.status === 413) {
            this.queue.shift();
            setLocal({ warning: err.message });
            continue;
          }
          if (err.status === 401 || err.status === 403 || err.status === 404 || err.status === 409) {
            void this.abort(err.message);
            return;
          }
          // the daemon is restarting or unreachable: retry, in order
          await new Promise((r) => setTimeout(r, this.retryMs));
          this.retryMs = Math.min(5000, this.retryMs * 2);
        } finally {
          this.updateBacklog();
        }
      }
    } finally {
      this.sending = false;
    }
  }

  private async send(c: Chunk) {
    const path = `/api/recordings/${encodeURIComponent(this.rec.id)}`;
    if (this.t.kind === "http") {
      await putRaw(this.t, `${path}/segments/${c.n}?track=${c.track}&offset=${c.offset}&dur=${Math.round(c.dur)}`, c.data, "application/octet-stream");
      return;
    }
    const data = await toBase64(c.data);
    await this.t.request("POST", `${path}/chunks`, { segment: c.n, track: c.track, offset: c.offset, dur_ms: Math.round(c.dur), data });
  }

  // ---- still frames: at the start, every 30 s of recorded time, at stop

  /** Takes the frame at ms of recorded time and queues it for the daemon. */
  private async frame(ms: number, key = false) {
    ms = Math.round(ms);
    if (!this.grabber || this.frameCount >= MAX_FRAMES || this.framesAt.has(ms)) return;
    this.framesAt.add(ms);
    let blob: Blob | null = null;
    try {
      blob = await this.grabber.grab();
    } catch {
      /* no frame this time */
    }
    if (!blob || this.ended) return;
    this.frameCount++;
    this.frameQ.push({ ms, blob, tries: 0, key });
    void this.pumpFrames();
  }

  /** Every 2 s: when the screen changed (a new page, a highlight, a drawing), a key-moment still now. */
  private look() {
    const now = Date.now();
    if (!this.looker || this.looking || now - this.lookAt < CHANGE_EVERY_MS) return;
    this.lookAt = now;
    this.looking = true;
    void this.looker
      .change()
      .then((d) => {
        if (d < CHANGE_MIN || this.ended || useCapture.getState().phase !== "recording") return;
        if (Date.now() - this.keyAt < KEY_GAP_MS || this.keyCount >= MAX_KEY_FRAMES || this.frameCount >= MAX_FRAMES - 1) return;
        this.keyAt = Date.now();
        this.keyCount++;
        void this.frame(localElapsed(useCapture.getState()), true);
      })
      .catch(() => {})
      .finally(() => (this.looking = false));
  }

  /** Sends the frames one at a time, beside the chunks; a lost frame never ends the recording. */
  private async pumpFrames() {
    if (this.frameSending) return;
    this.frameSending = true;
    try {
      while (this.frameQ.length && !this.ended) {
        const f = this.frameQ[0];
        try {
          await this.sendFrame(f);
          this.frameQ.shift();
        } catch (e) {
          const status = (e as ApiError).status ?? 0;
          if (status === 409 || status === 404 || status === 401 || status === 403) {
            this.frameQ = []; // finished, or it has all its frames
            break;
          }
          const wait = status >= 400 ? null : frameRetryMs(++f.tries); // a 4xx is final at once
          if (wait === null) {
            this.frameQ.shift();
            if (!this.frameWarned) console.warn("recorder: a still frame was not stored:", (e as Error).message);
            this.frameWarned = true;
            continue;
          }
          await new Promise((r) => setTimeout(r, wait)); // the daemon is away for a moment (or the network)
        }
      }
    } finally {
      this.frameSending = false;
    }
  }

  private async sendFrame(f: { ms: number; blob: Blob; key: boolean }) {
    const path = `/api/recordings/${encodeURIComponent(this.rec.id)}/frames/${f.ms}`;
    if (this.t.kind === "http") await putRaw(this.t, f.key ? path + "?key=1" : path, f.blob, "image/jpeg");
    else await this.t.request("PUT", path, { data: await toBase64(f.blob), key: f.key });
  }

  /** Waits for the queued frames (at most until deadline). */
  private async drainFrames(deadline: number) {
    void this.pumpFrames();
    while ((this.frameQ.length || this.frameSending) && Date.now() < deadline) await new Promise((r) => setTimeout(r, 100));
  }

  private onPCM(s: Int16Array) {
    if (useCapture.getState().phase !== "recording" || this.ended) return; // paused time is not recorded
    this.pcmChunks.push(s);
    this.pcmLen += s.length;
    if (this.pcmLen >= PCM_RATE) this.flushPCM(); // about a second
  }

  private flushPCM(n = this.seg) {
    if (!this.pcmLen) return;
    const bytes = pcmBytes(this.pcmChunks, this.pcmLen);
    this.pcmChunks = [];
    this.pcmLen = 0;
    this.enqueue(n, "pcm", bytes, bytes.length);
  }

  private tick() {
    if (useCapture.getState().phase !== "recording") return;
    if (this.grabber) {
      const at = localElapsed(useCapture.getState());
      if (at >= this.nextFrameAt) {
        // a late tick (a throttled timer) takes one frame at the latest whole interval
        const ms = Math.floor(at / this.frameEvery) * this.frameEvery;
        this.nextFrameAt = ms + this.frameEvery;
        if (this.frameCount < MAX_FRAMES - 1) void this.frame(ms); // one is kept for the stop
      }
      this.look();
    }
    if (Date.now() - this.lastMediaAt > 3000 && this.mr?.state === "recording") {
      try {
        this.mr.requestData(); // a recorder that holds its data: ask for it
      } catch {
        /* ignore */
      }
    }
    const limit = this.settings.segment_mb * (1 << 20) - 2 * this.maxChunk;
    if (this.segBytes >= limit || this.segMs() >= this.settings.segment_minutes * 60_000) this.rotate();
  }

  /** Starts the next segment: a new MediaRecorder on the same stream, then the old one stops. */
  private rotate() {
    if (!this.mr || this.ended) return;
    const old = this.mr;
    this.flushPCM(this.seg); // what the microphone gave so far belongs to the old segment
    this.segDur.set(this.seg, this.segMs());
    this.seg++;
    this.segBytes = 0;
    this.segBase = 0;
    this.segSince = Date.now();
    this.mr = this.newRecorder(this.seg);
    this.mr.start(1000);
    old.stop(); // its last chunk still goes to its own segment
  }

  private async heartbeat() {
    const ph = useCapture.getState().phase;
    if (this.ended || (ph !== "recording" && ph !== "paused")) return;
    const seq = this.stateSeq;
    try {
      const r = await recApi.setState(this.rec.id, "heartbeat");
      // A pause or resume made here after the heartbeat left wins over the state it answers.
      if (seq === this.stateSeq || !isLive(r)) this.follow(r);
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 404 || err.status === 409) void this.abort(err.message);
      // otherwise the daemon is away for a moment; the chunks retry
    }
  }

  /**
   * Follows a change made elsewhere (a pause or stop in another window, a limit). The daemon
   * sets updated_at on every change of state, so an older view (an event about a new segment
   * that crossed this window's own pause) never undoes a newer state.
   */
  private follow(r: Recording) {
    if (this.ended) return;
    if (!isLive(r)) {
      void this.abort(r.error ? "The recording stopped: " + r.error : "The recording was stopped in another window.", r.state === "done");
      return;
    }
    const ph = useCapture.getState().phase;
    if (!this.changing && r.updated_at >= this.stateAt) {
      this.stateAt = r.updated_at;
      if (r.state === "paused" && ph === "recording") this.pauseLocal();
      else if (r.state === "recording" && ph === "paused") this.resumeLocal();
    }
    this.rec = { ...r, state: useCapture.getState().phase === "paused" ? "paused" : "recording" };
    setLocal({ rec: this.rec });
  }

  /** Sends a pause or resume made here; while it travels, the daemon's older views are ignored. */
  private async sendState(state: "paused" | "recording") {
    this.stateSeq++;
    this.changing = true;
    try {
      const r = await recApi.setState(this.rec.id, state);
      this.changing = false;
      this.follow(r);
    } catch (e) {
      this.changing = false;
      toast.error(state === "paused" ? "Couldn't pause on the daemon" : "Couldn't resume on the daemon", { description: (e as Error).message });
    }
  }

  private pauseLocal() {
    void pauseNativeDeviceAudio(true);
    const now = Date.now();
    try {
      if (this.mr?.state === "recording") this.mr.pause();
    } catch {
      /* ignore */
    }
    this.segBase = this.segMs(now);
    this.segSince = 0;
    const st = useCapture.getState();
    setLocal({ phase: "paused", base: localElapsed(st, now), since: 0 });
  }

  private resumeLocal() {
    void pauseNativeDeviceAudio(false);
    try {
      if (this.mr?.state === "paused") this.mr.resume();
    } catch {
      /* ignore */
    }
    const now = Date.now();
    this.segSince = now;
    setLocal({ phase: "recording", since: now });
  }

  async pause() {
    if (useCapture.getState().phase !== "recording") return;
    this.pauseLocal();
    await this.sendState("paused");
  }

  async resume() {
    if (useCapture.getState().phase !== "paused") return;
    this.resumeLocal();
    await this.sendState("recording");
  }

  /** Stops capturing, sends what is left, then finishes the recording on the daemon. */
  async stop() {
    const ph = useCapture.getState().phase;
    if (this.ended || ph === "stopping") return;
    setLocal({ phase: "stopping", base: localElapsed(useCapture.getState()), since: 0 });
    this.clearTimers();
    await Promise.all(
      [...this.recorders].map(
        (mr) =>
          new Promise<void>((res) => {
            if (mr.state === "inactive") return res();
            mr.addEventListener("stop", () => res(), { once: true });
            try {
              mr.stop();
            } catch {
              res();
            }
          }),
      ),
    );
    this.flushPCM();
    await this.frame(useCapture.getState().base); // the last picture, while the tracks still run
    this.release();
    const deadline = Date.now() + 25_000;
    while (this.queue.length && !this.ended && Date.now() < deadline) {
      void this.pump();
      await new Promise((r) => setTimeout(r, 150));
    }
    await this.drainFrames(Math.min(deadline, Date.now() + 10_000));
    if (this.ended) return;
    const lost = this.queue.length;
    this.ended = true;
    this.offUpdates();
    try {
      const r = await recApi.setState(this.rec.id, "stopped");
      upsertRecording(r);
      finish(r, lost ? `${lost} last chunk${lost === 1 ? "" : "s"} did not reach the daemon.` : "");
    } catch (e) {
      finish(this.rec, (e as Error).message);
    }
  }

  /** Ends the capture without finishing on the daemon (it ended there, or cannot be reached). */
  async abort(message: string, quiet = false) {
    if (this.ended) return;
    this.ended = true;
    this.offUpdates();
    this.clearTimers();
    for (const mr of this.recorders) {
      try {
        mr.stop();
      } catch {
        /* ignore */
      }
    }
    this.release();
    setLocal({ phase: "idle", rec: null, base: 0, since: 0, backlog: 0, warning: "", preview: null });
    session = null;
    void loadActive();
    if (quiet) toast(message);
    else toast.error("Recording ended", { description: message, duration: 15000 });
  }

  private clearTimers() {
    this.timers.forEach(clearInterval);
    this.timers = [];
  }

  private release() {
    this.pcm?.stop();
    this.pcm = null;
    this.grabber?.stop();
    this.looker?.stop();
    this.cap.release();
    this.stream.getTracks().forEach((tr) => tr.stop());
  }
}

let session: Session | null = null;

function finish(r: Recording, note: string) {
  session = null;
  setLocal({ phase: "idle", rec: null, base: 0, since: 0, backlog: 0, warning: "", preview: null });
  void loadActive();
  const title = r.state === "failed" ? "Nothing was recorded" : "Recording saved";
  const description = [r.title, note].filter(Boolean).join(" · ");
  (r.state === "failed" ? toast.error : toast.success)(title, {
    description,
    action: r.state === "failed" ? undefined : { label: "Open", onClick: () => (window.location.hash = `/recordings/${encodeURIComponent(r.id)}`) },
    duration: 10000,
  });
}

// ---------------------------------------------------------------------------------------------
// starting

export interface StartOptions {
  /** screen: the OS picker (a whole screen or one window); camera: a clip from the camera; audio: the microphone only */
  what: "screen" | "camera" | "audio";
  mic: boolean;
  /** screen: the device's own sound too, where the engine gives it */
  deviceAudio?: boolean;
  /** screen: the camera as a circle in the corner (rec-bubble.ts; where canBubble()) */
  bubble?: boolean;
  /** camera: the devices to use (undefined: the default ones) */
  cameraId?: string;
  micId?: string;
  /** camera: the stream the dialog's preview opened, handed over instead of opening another */
  cameraStream?: MediaStream | null;
  title?: string;
  settings: RecorderSettings;
}

/**
 * The device-audio track of a screen recording: the display capture's own audio track when the
 * engine gave one (Chromium: the tab's sound, or the window's or whole screen's where the OS
 * allows), else null. The one place that chooses it: another source (a native loopback) plugs in
 * here.
 */
export async function deviceAudioTrack(display: MediaStream | null, wanted: boolean): Promise<MediaStreamTrack | null> {
  if (!wanted) return null;
  // The engine's own (a picker that shared sound), else the Windows app's native loopback.
  return display?.getAudioTracks().find((t) => t.readyState === "live") ?? (await nativeDeviceAudio());
}

/** The screen or a window through the OS picker; with the device's sound when audio is set. */
async function openDisplay(fps: number, audio: boolean): Promise<MediaStream> {
  const video = { frameRate: { ideal: fps, max: fps } };
  if (!audio) return navigator.mediaDevices.getDisplayMedia({ video, audio: false });
  // Chromium: systemAudio offers the whole screen's sound, windowAudio a window's; others ignore both.
  const opts = { video, audio: true, systemAudio: "include", windowAudio: "system" } as DisplayMediaStreamOptions;
  try {
    return await navigator.mediaDevices.getDisplayMedia(opts);
  } catch (e) {
    const name = (e as Error).name;
    if (name === "NotAllowedError" || name === "AbortError" || name === "InvalidStateError" || name === "NotFoundError") throw e;
    // an engine that refuses the sound here: the picture only (it says so below)
    return navigator.mediaDevices.getDisplayMedia({ video, audio: false });
  }
}

/** The camera (and the microphone when mic is set; without it if that fails). Also the dialog's preview. */
export async function openCamera(o: { cameraId?: string; micId?: string; mic: boolean }): Promise<MediaStream> {
  const video: MediaTrackConstraints = { width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30, max: 30 } };
  if (o.cameraId) video.deviceId = { exact: o.cameraId };
  const audio: MediaTrackConstraints = { echoCancellation: true, noiseSuppression: true, channelCount: 1 };
  if (o.micId) audio.deviceId = { exact: o.micId };
  if (!o.mic) return navigator.mediaDevices.getUserMedia({ video, audio: false });
  try {
    return await navigator.mediaDevices.getUserMedia({ video, audio });
  } catch (e) {
    const name = (e as Error).name;
    if (name === "NotAllowedError" || name === "AbortError" || name === "SecurityError") throw e;
    // no microphone (or a busy one): the camera alone
    return navigator.mediaDevices.getUserMedia({ video, audio: false });
  }
}

let toldNoDeviceAudio = false;

function noDeviceAudio() {
  if (toldNoDeviceAudio) return;
  toldNoDeviceAudio = true;
  toast("No device audio here", {
    description: "This window gave no sound with what you picked, so the recording has the microphone only. Picking the whole screen (or a tab) can include it.",
    duration: 10000,
  });
}

/**
 * Starts a recording. Call it straight from the click: the browser shows its picker only
 * for a user gesture, so nothing is awaited before getDisplayMedia.
 */
export async function startRecording(o: StartOptions): Promise<void> {
  if (session || useCapture.getState().phase !== "idle") throw new Error("A recording is already running in this window.");
  const t = useStore.getState().transport;
  if (!t) throw new Error("Not connected yet.");
  const wantMic = o.what === "audio" || o.mic;
  const wantDevice = o.what === "screen" && !!o.deviceAudio;
  // Both audio contexts are made during the click (WebKit only runs a context a user gesture made).
  const ctx = wantMic || wantDevice ? prepareAudioContext() : null; // the 16 kHz tap
  const mixCtx = wantMic && wantDevice ? prepareMixContext() : null; // the microphone + device audio
  setLocal({ phase: "starting", warning: "" });
  const opened: MediaStream[] = [];
  let mix: AudioMix | null = null;
  let bubble: Bubble | null = null;
  const stopAll = () => {
    stopNativeDeviceAudio(); // inside the mic mix nothing else stops it; a no-op without one
    bubble?.stop();
    mix?.stop();
    if (!mix) void mixCtx?.close().catch(() => {});
    for (const s of opened) s.getTracks().forEach((tr) => tr.stop());
  };
  const cleanup = () => {
    stopAll();
    void ctx?.close().catch(() => {});
    setLocal({ phase: "idle", preview: null });
  };
  try {
    let video: MediaStreamTrack | null = null;
    let mic: MediaStreamTrack | null = null;
    let device: MediaStreamTrack | null = null;
    let kind: RecordingKind = "audio";
    if (o.what === "screen") {
      if (!canCaptureScreen()) throw new Error("This window can't record the screen. Microphone only works.");
      const display = await openDisplay(o.settings.fps, wantDevice);
      opened.push(display);
      video = display.getVideoTracks()[0] ?? null;
      device = await deviceAudioTrack(display, wantDevice);
      for (const tr of display.getAudioTracks()) if (tr !== device) tr.stop();
      if (wantDevice && !device) noDeviceAudio();
      kind = video?.getSettings().displaySurface === "monitor" ? "screen" : "window";
      if (o.bubble && video) {
        try {
          const cam = await openCamera({ cameraId: o.cameraId, mic: false });
          opened.push(cam);
          bubble = startBubble(video, cam.getVideoTracks()[0], o.settings.fps);
        } catch (e) {
          toast.warning("Recording without the camera bubble", { description: cameraError(e) });
        }
      }
    } else if (o.what === "camera") {
      if (!canCaptureCamera()) throw new Error("This window can't use the camera.");
      const handed = o.cameraStream?.getVideoTracks().some((tr) => tr.readyState === "live") ? o.cameraStream : null;
      let cam: MediaStream;
      try {
        cam = handed ?? (await openCamera({ cameraId: o.cameraId, micId: o.micId, mic: o.mic }));
      } catch (e) {
        throw new Error(cameraError(e), { cause: e });
      }
      opened.push(cam);
      video = cam.getVideoTracks()[0] ?? null;
      mic = o.mic ? (cam.getAudioTracks()[0] ?? null) : null;
      if (!o.mic) cam.getAudioTracks().forEach((tr) => tr.stop());
      if (o.mic && !mic) toast.warning("Recording the camera without the microphone", { description: "The microphone is not available." });
      kind = "camera";
    }
    if (wantMic && o.what !== "camera") {
      try {
        const m = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, channelCount: 1 }, video: false });
        opened.push(m);
        mic = m.getAudioTracks()[0] ?? null;
      } catch (e) {
        if (o.what === "audio") throw new Error(micError(e), { cause: e });
        toast.warning("Recording the screen without the microphone", { description: micError(e) });
      }
    }
    // The recording has one audio track: the microphone, the device's sound, or both mixed.
    let sound: MediaStreamTrack | null = mic ?? device;
    if (mic && device) {
      try {
        if (!mixCtx) throw new Error("This window has no Web Audio.");
        mix = await mixAudio(mixCtx, [mic, device]);
        sound = mix.track;
      } catch (e) {
        device.stop();
        device = null;
        setLocal({ warning: "Only the microphone is recorded: the device audio could not be mixed in (" + (e as Error).message + ")." });
      }
    } else void mixCtx?.close().catch(() => {});
    const picture = bubble?.track ?? video; // what is recorded, and what the frames show
    const stream = new MediaStream([picture, sound].filter((x): x is MediaStreamTrack => !!x));
    const mime = pickMime(!!video, !!sound);
    const rec = await recApi.begin({ kind, mic: !!mic, system_audio: !!device, mime: mime || (video ? "video/webm" : "audio/webm"), title: o.title,
      sensitivity: sensitivityOf(clinicDefault()) }); // the Options checkbox (lib/clinic.ts)
    upsertRecording(rec);
    const cap: Capture = { stream, picture, sound, video, mic, release: stopAll };
    const s = new Session({ rec, t, settings: o.settings, cap, mime });
    session = s;
    if (kind === "camera" && video) setLocal({ preview: new MediaStream([video]) });
    try {
      await s.start(ctx);
    } catch (e) {
      session = null;
      void recApi.setState(rec.id, "stopped").catch(() => {});
      throw e;
    }
  } catch (e) {
    cleanup();
    if ((e as Error).name === "NotAllowedError" || (e as Error).name === "AbortError") return; // the owner cancelled the picker
    throw e;
  }
}

/** The camera's error for the owner. */
export function cameraError(e: unknown): string {
  const name = (e as Error)?.name ?? ((e as Error)?.cause as Error | undefined)?.name;
  if (name === "NotAllowedError") return "The camera is not allowed for this window (System Settings → Privacy → Camera).";
  if (name === "NotFoundError" || name === "OverconstrainedError") return "No camera was found.";
  if (name === "NotReadableError") return "The camera is in use by another app.";
  return (e as Error)?.message || "The camera is not available.";
}

function micError(e: unknown): string {
  const name = (e as Error)?.name;
  if (name === "NotAllowedError") return "The microphone is not allowed for this window (System Settings → Privacy → Microphone).";
  if (name === "NotFoundError") return "No microphone was found.";
  return (e as Error)?.message || "The microphone is not available.";
}

export const capture = {
  pause: () => session?.pause(),
  resume: () => session?.resume(),
  stop: () => session?.stop(),
  active: () => !!session,
};

// A recording running in this window asks before the window closes.
window.addEventListener("beforeunload", (e) => {
  if (session) {
    e.preventDefault();
    e.returnValue = "A recording is running in this window.";
  }
});

// ---------------------------------------------------------------------------------------------
// helpers

/** PUTs raw bytes to the daemon (the http transport); a refusal is an ApiError with its JSON. */
async function putRaw(t: Transport, path: string, body: Blob | Uint8Array, type: string): Promise<void> {
  let res: Response;
  try {
    res = await fetch(`${t.baseURL}${path}`, {
      method: "PUT",
      headers: { Authorization: `Bearer ${t.token}`, "Content-Type": type },
      body: body as BodyInit,
    });
  } catch {
    throw new ApiError(0, "The daemon is not reachable.");
  }
  if (!res.ok) {
    const text = await res.text();
    let data: Record<string, unknown> | undefined;
    try {
      data = JSON.parse(text);
    } catch {
      /* plain text */
    }
    throw new ApiError(res.status, (data?.error as string) || `HTTP ${res.status}`, data);
  }
}

async function toBase64(d: Blob | Uint8Array): Promise<string> {
  const blob = d instanceof Blob ? d : new Blob([d as BlobPart]);
  return new Promise((res, rej) => {
    const r = new FileReader();
    r.onload = () => res(String(r.result).replace(/^data:[^,]*,/, ""));
    r.onerror = () => rej(r.error);
    r.readAsDataURL(blob);
  });
}

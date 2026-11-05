// Still frames for the summaries (DESIGN.md "Recorder"): a JPEG of the screen or camera at the
// start, every 30 s of recorded time and at stop, at most 1280 px on the longer side and
// 600 KB. ImageCapture.grabFrame (Chromium, WebView2) reads the track itself, so it works while
// this window is hidden behind what is being recorded; elsewhere a tiny muted <video> plays the
// track and is drawn onto a canvas.

export const FRAME_EVERY_MS = 30_000;
/** the daemon keeps at most this many per recording */
export const MAX_FRAMES = 400;
const MAX_SIDE = 1280;
const MAX_BYTES = 600 << 10;
const FRAME_KEY = "tmx.rec.frame-every-ms";

/** The time between frames: 30 s of recorded time (a test sets localStorage "tmx.rec.frame-every-ms", at least 1 s). */
export function frameEvery(): number {
  try {
    const v = Number(localStorage.getItem(FRAME_KEY));
    if (Number.isFinite(v) && v >= 1000) return v;
  } catch {
    /* no storage here */
  }
  return FRAME_EVERY_MS;
}

const LOOK_W = 48;
const LOOK_H = 27;
/** A key moment: at least this share of the picture changed, looked at every CHANGE_EVERY_MS, at most one per KEY_GAP_MS. */
export const CHANGE_MIN = 0.02;
export const CHANGE_EVERY_MS = 2000;
export const KEY_GAP_MS = 6000;
/** at most this many key-moment stills per recording (the rest of MAX_FRAMES stays for the 30-s ones) */
export const MAX_KEY_FRAMES = 150;

/** RGBA pixels as grey levels (0.3 R + 0.6 G + 0.1 B). */
export function toGrey(px: ArrayLike<number>): Uint8Array {
  const out = new Uint8Array(px.length >> 2);
  for (let i = 0; i < out.length; i++) out[i] = (px[4 * i] * 3 + px[4 * i + 1] * 6 + px[4 * i + 2]) / 10;
  return out;
}

/** The share of cells that moved by more than 24 grey levels between two pictures of the same size. */
export function greyChange(a: Uint8Array, b: Uint8Array): number {
  if (a.length !== b.length || !a.length) return 0;
  let moved = 0;
  for (let i = 0; i < a.length; i++) if (Math.abs(a[i] - b[i]) > 24) moved++;
  return moved / a.length;
}

type ImageCaptureLike = { grabFrame(): Promise<ImageBitmap> };
type ImageCaptureCtor = new (t: MediaStreamTrack) => ImageCaptureLike;

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  return new Promise((res, rej) => {
    const id = setTimeout(() => rej(new Error("timed out")), ms);
    p.then(
      (v) => (clearTimeout(id), res(v)),
      (e) => (clearTimeout(id), rej(e)),
    );
  });
}

export class FrameGrabber {
  private track: MediaStreamTrack;
  private capture: ImageCaptureLike | null = null;
  private video: HTMLVideoElement | null = null;
  private canvas = document.createElement("canvas");
  private prevGrey: Uint8Array | null = null;
  private look: HTMLCanvasElement | null = null;

  constructor(track: MediaStreamTrack) {
    this.track = track;
    const IC = (window as unknown as { ImageCapture?: ImageCaptureCtor }).ImageCapture;
    if (IC) {
      try {
        this.capture = new IC(track);
      } catch {
        this.capture = null;
      }
    }
  }

  /** A JPEG of the track's picture now, or null when there is none (an ended track, no frame yet). */
  async grab(): Promise<Blob | null> {
    if (this.track.readyState !== "live") return null;
    if (this.capture) {
      try {
        const bm = await withTimeout(this.capture.grabFrame(), 3000);
        try {
          return await this.encode(bm, bm.width, bm.height);
        } finally {
          bm.close();
        }
      } catch {
        /* this time through the video element */
      }
    }
    const v = await this.videoElement();
    if (!v?.videoWidth) return null;
    return this.encode(v, v.videoWidth, v.videoHeight);
  }

  /**
   * How much of the picture changed since the last call (0..1): the share of 48×27 grey cells that moved by
   * more than 24 levels (the Python recorder's key moments: a new page, a highlight, a drawing). The first
   * call, or one without a picture, returns 0.
   */
  async change(): Promise<number> {
    if (this.track.readyState !== "live") return 0;
    let grey: Uint8Array | null = null;
    if (this.capture) {
      try {
        const bm = await withTimeout(this.capture.grabFrame(), 3000);
        try {
          grey = this.greyOf(bm);
        } finally {
          bm.close();
        }
      } catch {
        /* this time through the video element */
      }
    }
    if (!grey) {
      const v = await this.videoElement();
      if (!v?.videoWidth) return 0;
      grey = this.greyOf(v);
    }
    const prev = this.prevGrey;
    this.prevGrey = grey;
    return grey && prev ? greyChange(prev, grey) : 0;
  }

  private greyOf(src: CanvasImageSource): Uint8Array | null {
    this.look ??= Object.assign(document.createElement("canvas"), { width: LOOK_W, height: LOOK_H });
    const g = this.look.getContext("2d", { willReadFrequently: true });
    if (!g) return null;
    g.drawImage(src, 0, 0, LOOK_W, LOOK_H);
    return toGrey(g.getImageData(0, 0, LOOK_W, LOOK_H).data);
  }

  stop() {
    if (this.video) {
      this.video.srcObject = null;
      this.video.remove();
      this.video = null;
    }
  }

  private async videoElement(): Promise<HTMLVideoElement | null> {
    if (!this.video) {
      const v = document.createElement("video");
      v.muted = true;
      v.playsInline = true;
      v.setAttribute("aria-hidden", "true");
      // in the page (some engines draw no frames for a detached element), but not seen
      v.style.cssText = "position:fixed;right:0;bottom:0;width:2px;height:2px;opacity:0.01;pointer-events:none;z-index:-1";
      v.srcObject = new MediaStream([this.track]);
      document.body.appendChild(v);
      this.video = v;
      void v.play().catch(() => {});
    }
    const v = this.video;
    if (v.readyState < 2) {
      await withTimeout(new Promise<void>((res) => v.addEventListener("loadeddata", () => res(), { once: true })), 3000).catch(() => {});
    }
    return v.readyState >= 2 ? v : null;
  }

  /** Draws src at most 1280 px on its longer side and encodes it as a JPEG of at most 600 KB. */
  private async encode(src: CanvasImageSource, w: number, h: number): Promise<Blob | null> {
    if (!w || !h) return null;
    let scale = Math.min(1, MAX_SIDE / Math.max(w, h));
    let quality = 0.7;
    for (let i = 0; i < 5; i++) {
      const cw = Math.max(1, Math.round(w * scale));
      const ch = Math.max(1, Math.round(h * scale));
      this.canvas.width = cw;
      this.canvas.height = ch;
      const g = this.canvas.getContext("2d");
      if (!g) return null;
      g.drawImage(src, 0, 0, cw, ch);
      const blob = await new Promise<Blob | null>((res) => this.canvas.toBlob(res, "image/jpeg", quality));
      if (!blob) return null;
      if (blob.size <= MAX_BYTES) return blob;
      if (quality > 0.45) quality -= 0.15;
      else scale *= 0.75;
    }
    return null;
  }
}

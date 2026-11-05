// The camera bubble (DESIGN.md "Recorder"): the screen with the owner's camera as a circle in
// the lower left corner, drawn into one picture that is recorded instead of the screen.
// Chromium only (Chrome, Edge, the Windows app's WebView2): MediaStreamTrackProcessor hands over
// the tracks' frames, and the camera's frames drive the drawing. So it neither stops while this
// window is hidden behind what is being recorded (timers and animation frames are throttled
// there) nor freezes while the screen is still (a screen capture sends frames only when
// something changes). When the camera goes away, the screen's frames drive it, without the
// circle. The result is a MediaStreamTrackGenerator, at most the recorder's fps, at most
// 1920 px wide.

type FrameReader = ReadableStreamDefaultReader<VideoFrame>;
type ProcessorCtor = new (o: { track: MediaStreamTrack; maxBufferSize?: number }) => { readable: ReadableStream<VideoFrame> };
type GeneratorTrack = MediaStreamTrack & { writable: WritableStream<VideoFrame> };
type GeneratorCtor = new (o: { kind: "video" }) => GeneratorTrack;

const MAX_WIDTH = 1920;

function ctors(): { P: ProcessorCtor; G: GeneratorCtor } | null {
  const w = window as unknown as { MediaStreamTrackProcessor?: ProcessorCtor; MediaStreamTrackGenerator?: GeneratorCtor };
  if (!w.MediaStreamTrackProcessor || !w.MediaStreamTrackGenerator || typeof VideoFrame === "undefined" || typeof OffscreenCanvas === "undefined") return null;
  return { P: w.MediaStreamTrackProcessor, G: w.MediaStreamTrackGenerator };
}

/** Whether this window can draw the camera bubble. */
export function canBubble(): boolean {
  return ctors() !== null;
}

export interface Bubble {
  /** the screen with the camera circle: what is recorded */
  readonly track: MediaStreamTrack;
  stop(): void;
}

export function startBubble(screen: MediaStreamTrack, camera: MediaStreamTrack, fps: number): Bubble {
  const c = ctors();
  if (!c) throw new Error("This window can't draw the camera bubble.");
  const out = new c.G({ kind: "video" });
  const writer = out.writable.getWriter();
  const scr: FrameReader = new c.P({ track: screen, maxBufferSize: 1 }).readable.getReader();
  const cam: FrameReader = new c.P({ track: camera, maxBufferSize: 1 }).readable.getReader();
  const canvas = new OffscreenCanvas(2, 2);
  const g = canvas.getContext("2d", { alpha: false, desynchronized: true });
  if (!g) throw new Error("This window can't draw the camera bubble.");
  const every = 1e6 / Math.max(1, fps); // µs between output frames
  let latest: VideoFrame | null = null; // the screen's newest frame
  let camLive = true;
  let nextAt = 0;
  let writing = false;
  let stopped = false;

  const draw = (bubble: VideoFrame | null, timestamp: number) => {
    if (!latest || writing || stopped || timestamp < nextAt) return;
    // on a schedule, so a 20 fps camera gives 15 fps and not every other frame; a gap restarts it
    nextAt = timestamp - nextAt > every ? timestamp + every : nextAt + every;
    const sw = latest.displayWidth;
    const sh = latest.displayHeight;
    const scale = Math.min(1, MAX_WIDTH / sw);
    const w = Math.round(sw * scale) & ~1;
    const h = Math.round(sh * scale) & ~1;
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
    }
    g.drawImage(latest, 0, 0, w, h);
    if (bubble) {
      const d = Math.round(Math.min(w, h) * 0.24);
      const m = Math.round(Math.min(w, h) * 0.035);
      const x = m;
      const y = h - m - d;
      const side = Math.min(bubble.displayWidth, bubble.displayHeight);
      const sx = (bubble.displayWidth - side) / 2;
      const sy = (bubble.displayHeight - side) / 2;
      g.save();
      g.beginPath();
      g.arc(x + d / 2, y + d / 2, d / 2, 0, Math.PI * 2);
      g.clip();
      g.drawImage(bubble, sx, sy, side, side, x, y, d, d);
      g.restore();
      g.beginPath();
      g.arc(x + d / 2, y + d / 2, d / 2 - 1.5, 0, Math.PI * 2);
      g.lineWidth = Math.max(2, Math.round(d / 60));
      g.strokeStyle = "rgba(255,255,255,0.9)";
      g.stroke();
    }
    const frame = new VideoFrame(canvas, { timestamp });
    writing = true;
    writer
      .write(frame) // the generator closes it
      .catch(() => frame.close())
      .finally(() => (writing = false));
  };

  // the screen: keep only the newest frame; it drives the drawing once the camera is gone
  void (async () => {
    for (;;) {
      const { value, done } = await scr.read();
      if (done || stopped) {
        value?.close();
        break;
      }
      latest?.close();
      latest = value;
      if (!camLive) draw(null, value.timestamp);
    }
  })().catch(() => {});
  // the camera: every frame draws the screen with the circle
  void (async () => {
    for (;;) {
      const { value, done } = await cam.read();
      if (done || stopped) {
        value?.close();
        break;
      }
      try {
        draw(value, value.timestamp);
      } finally {
        value.close();
      }
    }
  })()
    .catch(() => {})
    .finally(() => (camLive = false));

  return {
    track: out,
    stop: () => {
      if (stopped) return;
      stopped = true;
      void scr.cancel().catch(() => {});
      void cam.cancel().catch(() => {});
      latest?.close();
      latest = null;
      void writer.close().catch(() => {});
      out.stop();
    },
  };
}

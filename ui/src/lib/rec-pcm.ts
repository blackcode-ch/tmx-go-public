// The recorder's 16 kHz track: taps the recording's sound (the microphone, the device's own
// sound, or both mixed by mixAudio) as mono PCM16 for the WAV the daemon keeps next to every
// media segment (transcription and agents read it). An AudioWorklet does the work off the main
// thread; where there is none (old WebKit, no secure context) a ScriptProcessorNode does the
// same. Both downmix to mono and decimate to 16 kHz by averaging (a box filter: plain, but
// enough for speech), then post 100 ms of Int16 samples at a time.

export const PCM_RATE = 16000;
const FRAME = 1600; // 100 ms

// The worklet, as a module loaded from a Blob URL (same origin, no extra file to serve).
const WORKLET = `
class TmxRecPcm extends AudioWorkletProcessor {
  constructor() {
    super();
    this.ratio = sampleRate / ${PCM_RATE};
    this.phase = 0; this.acc = 0; this.cnt = 0;
    this.buf = new Int16Array(${FRAME}); this.n = 0;
  }
  push(v) {
    v = v < -1 ? -1 : v > 1 ? 1 : v;
    this.buf[this.n++] = v < 0 ? v * 0x8000 : v * 0x7fff;
    if (this.n === ${FRAME}) { this.port.postMessage(this.buf.buffer, [this.buf.buffer]); this.buf = new Int16Array(${FRAME}); this.n = 0; }
  }
  process(inputs) {
    const x = inputs[0] && inputs[0][0];
    if (!x) return true;
    for (let i = 0; i < x.length; i++) {
      if (this.ratio <= 1) { this.push(x[i]); continue; }
      this.acc += x[i]; this.cnt++; this.phase += 1;
      if (this.phase >= this.ratio) { this.phase -= this.ratio; this.push(this.acc / this.cnt); this.acc = 0; this.cnt = 0; }
    }
    return true;
  }
}
registerProcessor("tmx-rec-pcm", TmxRecPcm);
`;

/** The same decimator on the main thread (ScriptProcessorNode fallback). */
class Decimator {
  private ratio: number;
  private phase = 0;
  private acc = 0;
  private cnt = 0;
  private buf = new Int16Array(FRAME);
  private n = 0;
  constructor(
    rate: number,
    private out: (pcm: Int16Array) => void,
  ) {
    this.ratio = rate / PCM_RATE;
  }
  private push(v: number) {
    v = v < -1 ? -1 : v > 1 ? 1 : v;
    this.buf[this.n++] = v < 0 ? v * 0x8000 : v * 0x7fff;
    if (this.n === FRAME) {
      this.out(this.buf);
      this.buf = new Int16Array(FRAME);
      this.n = 0;
    }
  }
  feed(x: Float32Array) {
    for (let i = 0; i < x.length; i++) {
      if (this.ratio <= 1) {
        this.push(x[i]);
        continue;
      }
      this.acc += x[i];
      this.cnt++;
      this.phase += 1;
      if (this.phase >= this.ratio) {
        this.phase -= this.ratio;
        this.push(this.acc / this.cnt);
        this.acc = 0;
        this.cnt = 0;
      }
    }
  }
}

type AudioContextCtor = typeof AudioContext;

function audioContextClass(): AudioContextCtor | undefined {
  return window.AudioContext ?? (window as unknown as { webkitAudioContext?: AudioContextCtor }).webkitAudioContext;
}

/**
 * Creates the audio context while the click that starts a recording is still being handled
 * (WebKit only lets a context run when a user gesture made it). Asks for 16 kHz, so the
 * browser resamples; falls back to the device rate.
 */
export function prepareAudioContext(): AudioContext | null {
  const AC = audioContextClass();
  if (!AC) return null;
  try {
    return new AC({ sampleRate: PCM_RATE });
  } catch {
    try {
      return new AC();
    } catch {
      return null;
    }
  }
}

/** A context at the device's own rate for mixAudio, made during the click like the other one. */
export function prepareMixContext(): AudioContext | null {
  const AC = audioContextClass();
  if (!AC) return null;
  try {
    return new AC();
  } catch {
    return null;
  }
}

export interface AudioMix {
  /** the mixed sound, for the MediaRecorder and the 16 kHz tap */
  readonly track: MediaStreamTrack;
  stop(): void;
}

/**
 * Mixes tracks (the microphone and the device's sound) into one, so a meeting's recording and
 * its WAV hold both sides. MediaRecorder records only one audio track, hence the mix. ctx runs
 * at the device's rate, so the recording keeps its quality; the 16 kHz tap reads the result.
 */
export async function mixAudio(ctx: AudioContext, tracks: MediaStreamTrack[]): Promise<AudioMix> {
  if (ctx.state === "suspended") await ctx.resume().catch(() => {});
  const out = ctx.createMediaStreamDestination();
  const nodes: AudioNode[] = [];
  for (const t of tracks) {
    const src = ctx.createMediaStreamSource(new MediaStream([t]));
    src.connect(out);
    nodes.push(src);
  }
  const track = out.stream.getAudioTracks()[0];
  if (!track) throw new Error("This window cannot mix audio.");
  return {
    track,
    stop: () => {
      for (const n of nodes) {
        try {
          n.disconnect();
        } catch {
          /* already */
        }
      }
      track.stop();
      void ctx.close().catch(() => {});
    },
  };
}

export interface PcmTap {
  /** "worklet" or "script" */
  readonly how: string;
  readonly rate: number;
  stop(): void;
}

/** Starts tapping stream's audio into onPCM (16 kHz mono Int16, 100 ms per call); stop() closes ctx. */
export async function startPcmTap(ctx: AudioContext | null, stream: MediaStream, onPCM: (pcm: Int16Array) => void): Promise<PcmTap> {
  let src: MediaStreamAudioSourceNode;
  try {
    if (!ctx) throw new Error("no audio context");
    src = ctx.createMediaStreamSource(stream);
  } catch {
    // Some engines cannot connect a microphone to a context at another rate: use the device's.
    void ctx?.close().catch(() => {});
    const AC = audioContextClass();
    if (!AC) throw new Error("This window has no Web Audio: the 16 kHz track cannot be recorded.");
    ctx = new AC();
    src = ctx.createMediaStreamSource(stream);
  }
  const audio = ctx;
  if (audio.state === "suspended") await audio.resume().catch(() => {});
  const mute = audio.createGain(); // processing nodes must reach the output to run; nothing is heard
  mute.gain.value = 0;
  mute.connect(audio.destination);
  const close = (nodes: AudioNode[]) => {
    for (const n of nodes) {
      try {
        n.disconnect();
      } catch {
        /* already */
      }
    }
    void audio.close().catch(() => {});
  };

  if (audio.audioWorklet && typeof AudioWorkletNode !== "undefined") {
    const url = URL.createObjectURL(new Blob([WORKLET], { type: "text/javascript" }));
    try {
      await audio.audioWorklet.addModule(url);
      const node = new AudioWorkletNode(audio, "tmx-rec-pcm", {
        numberOfInputs: 1,
        numberOfOutputs: 1,
        channelCount: 1,
        channelCountMode: "explicit",
        channelInterpretation: "speakers",
      });
      node.port.onmessage = (e: MessageEvent<ArrayBuffer>) => onPCM(new Int16Array(e.data));
      src.connect(node);
      node.connect(mute);
      return { how: "worklet", rate: audio.sampleRate, stop: () => close([src, node, mute]) };
    } catch {
      /* fall back to the script processor */
    } finally {
      URL.revokeObjectURL(url);
    }
  }
  const proc = audio.createScriptProcessor(4096, 1, 1);
  const dec = new Decimator(audio.sampleRate, onPCM);
  proc.onaudioprocess = (e) => dec.feed(e.inputBuffer.getChannelData(0));
  src.connect(proc);
  proc.connect(mute);
  return { how: "script", rate: audio.sampleRate, stop: () => close([src, proc, mute]) };
}

/** Int16 samples as little-endian bytes (the WAV's order on every platform). */
export function pcmBytes(chunks: Int16Array[], total: number): Uint8Array {
  const out = new Uint8Array(total * 2);
  const dv = new DataView(out.buffer);
  let o = 0;
  for (const c of chunks) {
    for (let i = 0; i < c.length; i++, o += 2) dv.setInt16(o, c[i], true);
  }
  return out;
}

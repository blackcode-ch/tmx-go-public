// Mock recordings for ?mock=…: a few finished recordings with transcripts, and a working
// recorder API (begin, chunks, frames, pause, stop) so the capture flow runs without a daemon.
// There is no media: the player shows a placeholder, and frames only their times.
import { ApiError } from "../transport";
import type { Recording, RecordingFrame, RecordingSegment, RecordingTranscript } from "../recording-types";
import { HOME } from "./data";

type Emit = (type: string, data: unknown) => void;

const DIR = `${HOME}/Library/Application Support/tmx-go/recordings`;
const SETTINGS = { video_kbps: 1500, fps: 15, max_minutes: 0, segment_mb: 20, segment_minutes: 5 };

function segs(id: string, ext: string, mic: boolean, parts: [number, number][], t0: number): RecordingSegment[] {
  let t = t0;
  return parts.map(([ms, bytes], i) => {
    const n = i + 1;
    const base = `${DIR}/${id}/seg-${String(n).padStart(4, "0")}`;
    const g: RecordingSegment = {
      n,
      file: base + ext,
      bytes,
      duration_ms: ms,
      started_at: t,
      sha256: "9f2c…",
      ...(mic ? { wav: base + ".wav", wav_bytes: 44 + (ms / 1000) * 32000 } : {}),
    };
    t += ms;
    return g;
  });
}

/** frames every 30 s and one at the end, like the recorder takes them */
function framesOf(id: string, duration: number): RecordingFrame[] {
  const out: RecordingFrame[] = [];
  for (let ms = 0; ms < duration; ms += 30_000) out.push({ ms, path: `${DIR}/${id}/frames/f-${String(ms).padStart(9, "0")}.jpg`, bytes: 84_000 });
  out.push({ ms: duration, path: `${DIR}/${id}/frames/f-${String(duration).padStart(9, "0")}.jpg`, bytes: 84_000 });
  return out;
}

function rec(p: Partial<Recording> & Pick<Recording, "id" | "title" | "kind" | "started_at">, parts: [number, number][]): Recording {
  const mic = p.mic ?? true;
  const ext = p.kind === "audio" ? ".m4a" : ".mp4";
  const segments = segs(p.id, ext, mic || !!p.system_audio, parts, p.started_at);
  const duration = segments.reduce((a, g) => a + g.duration_ms, 0);
  return {
    ...(p.kind === "audio" ? {} : { frames: framesOf(p.id, duration) }),
    state: "done",
    mime: p.kind === "audio" ? "audio/mp4;codecs=mp4a.40.2" : "video/mp4;codecs=avc1,mp4a.40.2",
    mic,
    dir: `${DIR}/${p.id}`,
    bytes: segments.reduce((a, g) => a + g.bytes, 0),
    duration_ms: duration,
    ended_at: p.started_at + duration + 4000,
    updated_at: p.started_at + duration + 4000,
    ...p,
    segments,
  };
}

const LINES: Record<string, string[]> = {
  "r5standup01/1": [
    "Morning everyone, quick round today.",
    "Rita: the import job finished overnight, two files failed on the date format.",
    "I'll take the date format fix, it's in the parser we touched last week.",
    "Rita: thanks, and the client call moved to Thursday at three.",
  ],
  "r4camclip01/1": ["Hi team, quick hello from the new office.", "The meeting room is on the left, and the coffee machine finally works."],
  "r7checkout1/1": [
    "Okay, so this is the checkout bug Bo reported this morning.",
    "I add two items to the cart, then switch the currency to euros.",
    "Watch the total: it still shows the old price in francs.",
    "Now I reload the page and it's correct, so it's a stale cache in the cart store.",
  ],
  "r7checkout1/2": ["Here's the network tab: the second request comes back with the new rates.", "But the cart never subscribes to the rate change."],
  "r7checkout1/3": ["That's the whole repro. I'd like the lead to add a test for this before fixing it."],
  "r3voicenote/1": [
    "Voice note for the launch plan.",
    "We ship the landing page on Thursday, the pricing table can follow next week.",
    "Ask the design lead for the final hero image by Wednesday noon.",
    "And remind me to check the analytics events before we announce anything.",
  ],
};

function transcriptOf(r: Recording, n: number, state: RecordingTranscript["state"] = "done"): RecordingTranscript {
  const key = `${r.id}/${n}`;
  const lines = LINES[key] ?? ["(Mock transcript.) This part was transcribed on this machine."];
  const seg = r.segments[n - 1];
  const step = Math.floor((seg?.duration_ms ?? 60_000) / (lines.length + 1));
  return {
    id: `tr-${key}`,
    source: `recording:${key}`,
    state,
    model: "base-q5_1",
    language: "en",
    text: lines.join(" "),
    segments: state === "done" ? lines.map((text, i) => ({ from_ms: i * step + 400, to_ms: (i + 1) * step, text })) : null,
    created_at: r.ended_at ?? r.started_at,
    updated_at: Date.now(),
  };
}

export class MockRecordings {
  private list: Recording[];
  private transcripts = new Map<string, RecordingTranscript>();

  constructor(private emit: Emit) {
    const now = Date.now();
    const hour = 3_600_000;
    this.list = [
      rec({ id: "r5standup01", title: "Standup with the Lisbon team", kind: "screen", system_audio: true, started_at: now - 40 * 60_000 }, [[384_000, 12_900_000]]),
      rec({ id: "r4camclip01", title: "Hello from the new office", kind: "camera", started_at: now - 90 * 60_000 }, [[48_000, 6_100_000]]),
      rec({ id: "r7checkout1", title: "Checkout bug repro", kind: "screen", started_at: now - 2 * hour }, [
        [600_000, 19_800_000],
        [598_000, 19_600_000],
        [131_000, 4_400_000],
      ]),
      rec({ id: "r3voicenote", title: "Voice note: launch plan", kind: "audio", started_at: now - 26 * hour }, [[161_000, 1_150_000]]),
      rec(
        {
          id: "r9designrv",
          title: "Design review with Sam",
          kind: "window",
          started_at: now - 3 * 24 * hour,
          state: "interrupted",
          error: "nothing arrived from the recording window for 20 s (was it closed?); what came before is kept",
        },
        [[412_000, 14_200_000]],
      ),
      rec({ id: "r2silentui", title: "Settings walkthrough (no sound)", kind: "window", mic: false, started_at: now - 6 * 24 * hour }, [[95_000, 3_100_000]]),
    ];
    for (const r of this.list.slice(0, 4)) for (const g of r.segments) this.putTranscript(transcriptOf(r, g.n));
  }

  private putTranscript(t: RecordingTranscript) {
    this.transcripts.set(t.source, t);
  }

  private find(id: string): Recording {
    const r = this.list.find((x) => x.id === id);
    if (!r) throw new ApiError(404, `no recording "${id}"`);
    return r;
  }

  private live(): Recording | undefined {
    return this.list.find((r) => r.state === "recording" || r.state === "paused");
  }

  private changed(r: Recording) {
    r.updated_at = Date.now();
    this.emit("recording.updated", r);
  }

  transcriptsFor(q: URLSearchParams): RecordingTranscript[] {
    const prefix = q.get("source_prefix") ?? "";
    return [...this.transcripts.values()].filter((t) => t.source.startsWith(prefix));
  }

  route(method: string, parts: string[], q: URLSearchParams, body: unknown): unknown {
    const [, id, action] = parts;
    if (!id) {
      if (method === "POST") return this.begin(body as { kind: Recording["kind"]; mic: boolean; system_audio?: boolean; mime: string; title?: string });
      const limit = Number(q.get("limit") || 0);
      const out = [...this.list].sort((a, b) => b.started_at - a.started_at);
      return limit ? out.slice(0, limit) : out;
    }
    if (id === "active" && method === "GET") return { recording: this.live() ?? null, settings: SETTINGS };
    const r = this.find(id);
    if (!action) {
      if (method === "PATCH") {
        r.title = String((body as { title?: string }).title ?? "").trim() || r.title;
        this.changed(r);
        return r;
      }
      if (method === "DELETE") {
        if (r === this.live()) throw new ApiError(409, "stop the recording before deleting it");
        this.list = this.list.filter((x) => x !== r);
        this.emit("recording.removed", { id });
        return { ok: true };
      }
      return r;
    }
    switch (action) {
      case "state":
        return this.setState(r, (body as { state: string }).state);
      case "chunks":
        return this.chunk(r, body as { segment: number; track: string; offset: number; dur_ms: number; data: string });
      case "ticket":
        return { ticket: "mock-ticket", expires_at: Date.now() + 600_000 };
      case "segments":
        throw new ApiError(404, "mock data has no media");
      case "share": {
        const b = body as { chat_id: string; body?: string };
        return { id: "m-" + Math.random().toString(36).slice(2, 8), chat_id: b.chat_id, body: b.body ?? `Recording: ${r.title}`, files: [] };
      }
      case "transcribe":
        return this.transcribe(r);
      case "frames": {
        const ms = Number(parts[3]);
        if (method === "GET") throw new ApiError(404, "mock data has no pictures");
        if (r.state !== "recording" && r.state !== "paused") throw new ApiError(409, `this recording is ${r.state}; nothing more can be added to it`);
        const f: RecordingFrame = { ms, path: `${r.dir}/frames/f-${String(ms).padStart(9, "0")}.jpg`, bytes: 80_000 };
        r.frames = [...(r.frames ?? []).filter((x) => x.ms !== ms), f].sort((a, b) => a.ms - b.ms);
        return f;
      }
    }
    throw new ApiError(404, "Not found.");
  }

  private begin(b: { kind: Recording["kind"]; mic: boolean; system_audio?: boolean; mime: string; title?: string }): Recording {
    if (this.live()) throw new ApiError(409, "a recording is already running; stop it first");
    const now = Date.now();
    const id = "r" + Math.random().toString(36).slice(2, 11);
    const what = b.kind === "audio" ? "Voice recording" : b.kind === "window" ? "Window recording" : b.kind === "camera" ? "Camera clip" : "Screen recording";
    const r: Recording = {
      id,
      title: b.title || `${what} ${new Date(now).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}`,
      kind: b.kind,
      state: "recording",
      mime: b.mime,
      mic: b.mic,
      system_audio: !!b.system_audio,
      dir: `${DIR}/${id}`,
      segments: [],
      bytes: 0,
      duration_ms: 0,
      resumed_at: now,
      started_at: now,
      updated_at: now,
    };
    this.list.unshift(r);
    this.changed(r);
    return r;
  }

  private setState(r: Recording, state: string): Recording {
    const now = Date.now();
    const live = r.state === "recording" || r.state === "paused";
    if (!live) {
      if (state === "stopped") return r;
      throw new ApiError(409, `this recording is ${r.state}; nothing more can be added to it`);
    }
    if (state === "paused" && r.state === "recording") {
      r.duration_ms += now - (r.resumed_at ?? now);
      r.resumed_at = 0;
      r.state = "paused";
      this.changed(r);
    } else if (state === "recording" && r.state === "paused") {
      r.resumed_at = now;
      r.state = "recording";
      this.changed(r);
    } else if (state === "stopped") {
      if (r.state === "recording") r.duration_ms += now - (r.resumed_at ?? now);
      r.resumed_at = 0;
      r.state = r.bytes ? "done" : "failed";
      if (!r.bytes) r.error = "nothing was recorded";
      r.ended_at = now;
      this.changed(r);
    }
    return r;
  }

  private chunk(r: Recording, c: { segment: number; track: string; offset: number; dur_ms: number; data: string }): { size: number } {
    if (r.state !== "recording" && r.state !== "paused") throw new ApiError(409, `this recording is ${r.state}; nothing more can be added to it`);
    const n = c.segment;
    if (n === r.segments.length + 1) {
      const base = `${r.dir}/seg-${String(n).padStart(4, "0")}`;
      r.segments.push({ n, file: base + (r.kind === "audio" ? ".webm" : ".webm"), bytes: 0, duration_ms: 0, started_at: Date.now(), ...(r.mic || r.system_audio ? { wav: base + ".wav", wav_bytes: 44 } : {}) });
      this.changed(r);
    }
    const g = r.segments[n - 1];
    if (!g) throw new ApiError(409, `segment ${n} cannot start before segment ${r.segments.length + 1}`);
    const size = Math.floor((c.data.length * 3) / 4) - (c.data.endsWith("==") ? 2 : c.data.endsWith("=") ? 1 : 0);
    if (c.track === "pcm") {
      g.wav_bytes = (g.wav_bytes ?? 44) + size;
      g.duration_ms = Math.floor(((g.wav_bytes ?? 44) - 44) / 32);
      return { size: (g.wav_bytes ?? 44) - 44 };
    }
    g.bytes += size;
    r.bytes += size;
    if (!r.mic && !r.system_audio) g.duration_ms = Math.max(g.duration_ms, c.dur_ms);
    return { size: g.bytes };
  }

  private transcribe(r: Recording): { queued: number; source_prefix: string } {
    if (r.state === "recording" || r.state === "paused") throw new ApiError(409, "the recording is still running; stop it first");
    const parts = r.segments.filter((g) => (g.wav_bytes ?? 0) > 44);
    if (!parts.length) throw new ApiError(400, "this recording has no sound to transcribe");
    parts.forEach((g, i) => {
      const queued = transcriptOf(r, g.n, "queued");
      this.putTranscript(queued);
      this.emit("transcript.updated", queued);
      setTimeout(() => {
        const running = { ...queued, state: "running" as const, updated_at: Date.now() };
        this.putTranscript(running);
        this.emit("transcript.updated", running);
      }, 600 + i * 900);
      setTimeout(
        () => {
          const done = transcriptOf(r, g.n);
          this.putTranscript(done);
          this.emit("transcript.updated", done);
        },
        1500 + i * 900,
      );
    });
    return { queued: parts.length, source_prefix: `recording:${r.id}/` };
  }
}

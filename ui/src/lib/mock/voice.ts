// Voice in the mock daemon: /api/voice, /api/transcribe and /api/transcripts. Ready by default
// (engine and Base model downloaded); add &voice=off for a machine where voice is not set up yet
// (Set up then simulates the downloads), &voice=none for a daemon without voice (older release).
import { ApiError } from "../transport";
import type { Transcript } from "../types";
import type { TranscribeResult, VoiceModel, VoiceSettingsPatch, VoiceStatus } from "../voice";

const MODELS: VoiceModel[] = [
  { name: "base-q5_1", label: "Base", file: "ggml-base-q5_1.bin", size: 59707625, ram_mb: 400, installed: true, default: true },
  { name: "small-q5_1", label: "Small", file: "ggml-small-q5_1.bin", size: 190085487, ram_mb: 900, installed: false },
  { name: "large-v3-turbo-q5_0", label: "Large v3 Turbo", file: "ggml-large-v3-turbo-q5_0.bin", size: 574041195, ram_mb: 1800, installed: false },
];
const HEAVY_WARNING = "This machine has 8 GB of memory: while it runs, this model can make it slow. Base is recommended here.";
const SAID = [
  "Let's move the launch to Thursday and ask the API team for the webhook retries first.",
  "Can you check why the checkout test fails on Windows and fix it before the release?",
  "Summarise what changed in the billing project this week, in three short points.",
];

function transcripts(): Transcript[] {
  const now = Date.now();
  const mk = (id: string, source: string, minutes: number, lines: string[]): Transcript => {
    const segments = lines.map((text, i) => ({ from_ms: i * 6500, to_ms: i * 6500 + 6000, text }));
    return {
      id, source, state: "done", model: "base-q5_1", language: "en", text: lines.join(" "), segments, error: "",
      created_at: now - minutes * 60_000, updated_at: now - minutes * 60_000 + 40_000,
    };
  };
  return [
    mk("tr4n5cr1pt", "recording:rc8k2m4n6p/1", 25, ["Okay, so the plan for the launch site.", "We keep one page, pricing comes later.", "The budget for ads is two thousand francs."]),
    mk("tr9v3x5z7b", "recording:rc8k2m4n6p/2", 15, ["Second part: the domain is booked.", "Ask the head to file the decisions on the board."]),
  ];
}

export class MockVoice {
  private s: VoiceStatus;
  private none: boolean;
  private list: Transcript[] = transcripts();
  private said = 0;
  private timer: ReturnType<typeof setInterval> | null = null;

  constructor(private emit: (type: string, data: unknown) => void) {
    const p = new URLSearchParams(typeof location === "undefined" ? "" : location.search).get("voice");
    this.none = p === "none";
    const off = p === "off";
    this.s = {
      platform: "darwin/arm64", ready: !off, engine: { version: "v1.9.5", available: true, installed: !off, size: off ? undefined : 7_340_032 },
      models: MODELS.map((m) => ({ ...m, installed: m.installed && !off, selected: m.name === "base-q5_1", warning: m.ram_mb > 500 ? HEAVY_WARNING : undefined })),
      vad: { name: "silero-v6.2.0", label: "Silero voice activity", file: "ggml-silero-v6.2.0.bin", size: 885098, ram_mb: 20, installed: !off },
      settings: { model: "base-q5_1", language: "auto", threads: 4, threads_auto: true, vad: true, auto_transcribe: true },
      queued: 0, memory_mb: 8192, busy: false,
    };
    this.refresh();
  }

  private refresh() {
    const s = this.s;
    for (const m of s.models) m.selected = m.name === s.settings.model;
    const model = s.models.find((m) => m.selected)!;
    s.ready = s.engine.installed && model.installed;
    s.reason = !s.engine.installed
      ? "The voice engine is not downloaded yet: set it up in Settings → Voice (tmx voice setup)."
      : !model.installed
        ? `The ${model.label} model (${model.name}) is not downloaded yet: download it in Settings → Voice (tmx voice get ${model.name}).`
        : undefined;
    s.busy = !!(s.engine.downloading || s.vad.downloading || s.models.some((m) => m.downloading));
  }

  private changed() {
    this.refresh();
    this.emit("voice.status", structuredClone(this.s));
  }

  /** Downloads the items in turn, a second or two each. */
  private download(items: { size: number; set: (p?: { done: number; total: number }, done?: boolean) => void }[]) {
    if (this.timer) return;
    const queue = [...items];
    let done = 0;
    this.timer = setInterval(() => {
      const it = queue[0];
      if (!it) {
        clearInterval(this.timer!);
        this.timer = null;
        this.changed();
        return;
      }
      done = Math.min(it.size, done + Math.max(it.size / 6, 400_000));
      if (done >= it.size) {
        it.set(undefined, true);
        queue.shift();
        done = 0;
      } else it.set({ done, total: it.size });
      this.changed();
    }, 350);
  }

  route(method: string, parts: string[], q: URLSearchParams, body: unknown): unknown {
    if (this.none) throw new ApiError(404, "no such API route");
    const [res, a, b] = parts;
    const s = this.s;
    if (res === "voice") {
      if (!a && method === "GET") return s;
      if (!a && method === "PUT") {
        const p = (body ?? {}) as VoiceSettingsPatch;
        if (p.model) s.settings.model = p.model;
        if (p.language) s.settings.language = p.language;
        if (p.vad !== undefined) s.settings.vad = p.vad;
        if (p.auto_transcribe !== undefined) s.settings.auto_transcribe = p.auto_transcribe;
        this.changed();
        return s;
      }
      if (a === "setup" && method === "POST") {
        const model = s.models.find((m) => m.selected)!;
        const items = [];
        if (!s.engine.installed) items.push({ size: 7_340_032, set: (p?: { done: number; total: number }, d?: boolean) => Object.assign(s.engine, { downloading: p, installed: !!d, size: d ? 7_340_032 : undefined }) });
        if (!model.installed) items.push({ size: model.size, set: (p?: { done: number; total: number }, d?: boolean) => Object.assign(model, { downloading: p, installed: !!d }) });
        if (!s.vad.installed) items.push({ size: s.vad.size, set: (p?: { done: number; total: number }, d?: boolean) => Object.assign(s.vad, { downloading: p, installed: !!d }) });
        if (items[0]) items[0].set({ done: 0, total: items[0].size });
        this.download(items);
        this.refresh();
        return s;
      }
      if (a === "models" && b) {
        const m = b === s.vad.name ? s.vad : s.models.find((x) => x.name === b);
        if (!m) throw new ApiError(400, `there is no model "${b}"`);
        if (method === "DELETE") {
          m.installed = false;
          m.downloading = undefined;
        } else if (!m.installed) {
          m.downloading = { done: 0, total: m.size };
          this.download([{ size: m.size, set: (p, d) => Object.assign(m, { downloading: p, installed: !!d }) }]);
        }
        this.changed();
        return s;
      }
    }
    if (res === "transcribe" && method === "POST") {
      if (!s.ready) throw new ApiError(409, "voice transcription isn't set up: " + (s.reason ?? "").replace(/^./, (c) => c.toLowerCase()));
      const text = SAID[this.said++ % SAID.length];
      const out: TranscribeResult = { text, language: "en", segments: [{ from_ms: 0, to_ms: 4200, text }] };
      return out;
    }
    if (res === "transcripts") {
      if (!a) {
        const words = (q.get("q") ?? "").toLowerCase().split(/\W+/).filter(Boolean);
        const prefix = q.get("source_prefix") ?? "";
        return this.list.filter((t) => t.source.startsWith(prefix) && words.every((w) => t.text.toLowerCase().includes(w)));
      }
      const t = this.list.find((x) => x.id === a);
      if (!t) throw new ApiError(404, "no such transcript");
      if (method === "DELETE") {
        this.list = this.list.filter((x) => x.id !== a);
        this.emit("transcript.removed", { id: a });
        return { ok: true };
      }
      return t;
    }
    throw new ApiError(404, "no such API route");
  }
}

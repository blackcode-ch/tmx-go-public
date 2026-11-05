// Recordings of the owner's own screen, camera, microphone and device audio (DESIGN.md
// "Recorder"); mirrors internal/model/recording.go, same JSON names.

export type RecordingKind = "screen" | "window" | "audio" | "camera";
export type RecordingState = "recording" | "paused" | "done" | "interrupted" | "failed";

export interface RecordingSegment {
  n: number;
  /** absolute path of the media file */
  file: string;
  bytes: number;
  /** absolute path of the 16 kHz mono PCM16 WAV of the microphone and device audio ("" without either) */
  wav?: string;
  wav_bytes?: number;
  duration_ms: number;
  sha256?: string;
  started_at: number;
}

export interface Recording {
  id: string;
  title: string;
  kind: RecordingKind;
  state: RecordingState;
  mime: string;
  mic: boolean;
  /** the device's own sound was captured with the screen (mixed with the microphone) */
  system_audio?: boolean;
  /** "clinic": patient data, the report is written by local models only (lib/clinic.ts) */
  sensitivity?: "clinic" | "";
  dir: string;
  segments: RecordingSegment[];
  /** still JPEGs for summaries: at the start, every 30 s of recorded time, at stop */
  frames?: RecordingFrame[];
  bytes: number;
  /** while live: the recorded time before resumed_at; once finished: the total */
  duration_ms: number;
  /** while recording: when the current stretch began (elapsed = duration_ms + now - resumed_at) */
  resumed_at?: number;
  error?: string;
  started_at: number;
  ended_at?: number;
  updated_at: number;
  /** web: chunks from the window; native: the macOS app's ScreenCaptureKit writes the files */
  source?: "web" | "native";
  /** a native recording asked to stop while the app finishes its last part */
  stopping?: boolean;
  /** Claude's summary of it (lib/summaries.ts): the text and where it is; in lists and reads, not in events */
  summary?: string;
  summary_state?: "transcribing" | "queued" | "running" | "done" | "failed" | "skipped";
}

/** One still frame: frames/f-000030000.jpg is at 0:30 of recorded time. */
export interface RecordingFrame {
  ms: number;
  /** absolute path of the JPEG */
  path: string;
  bytes: number;
  /** a key moment: the window saw the screen change */
  key?: boolean;
}

/** config.json "recorder", with the defaults filled in */
export interface RecorderSettings {
  video_kbps: number;
  fps: number;
  /** 0 = no limit */
  max_minutes: number;
  segment_mb: number;
  segment_minutes: number;
}

export interface ActiveRecording {
  recording: Recording | null;
  settings: RecorderSettings;
}

/** The transcribe service's transcript of one segment (GET /api/transcripts); a local copy of its type. */
export interface RecordingTranscript {
  id: string;
  /** "recording:<id>/<n>" */
  source: string;
  state: "queued" | "running" | "done" | "failed";
  model?: string;
  language?: string;
  text?: string;
  segments?: { from_ms: number; to_ms: number; text: string }[] | null;
  error?: string;
  created_at: number;
  updated_at: number;
}

/** Whether a recording has a 16 kHz track (microphone, device audio, or both mixed). */
export function hasSound(r: Pick<Recording, "mic" | "system_audio">): boolean {
  return r.mic || !!r.system_audio;
}

/** What a recording captured, e.g. "screen, microphone and device audio". */
export function captureLabel(r: Pick<Recording, "kind" | "mic" | "system_audio">): string {
  const parts = [r.kind === "camera" ? "camera" : r.kind === "audio" ? "" : r.kind === "window" ? "window" : "screen", r.mic ? "microphone" : "", r.system_audio ? "device audio" : ""].filter(Boolean);
  if (parts.length < 2) return parts[0] ?? "";
  return parts.slice(0, -1).join(", ") + " and " + parts[parts.length - 1];
}

export function isLive(r: Pick<Recording, "state"> | null | undefined): boolean {
  return !!r && (r.state === "recording" || r.state === "paused");
}

/** The recorded time now (pauses excluded). */
export function elapsedMs(r: Pick<Recording, "state" | "duration_ms" | "resumed_at">, now = Date.now()): number {
  let ms = r.duration_ms;
  if (r.state === "recording" && r.resumed_at) ms += Math.max(0, now - r.resumed_at);
  return ms;
}

/** 12:03 or 1:02:03 */
export function clock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

/** 12 min, 45 s, 1 h 5 min */
export function lengthLabel(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h ${m % 60} min`;
}

export function transcriptSegment(source: string): number {
  const m = /\/(\d+)$/.exec(source);
  return m ? Number(m[1]) : 0;
}

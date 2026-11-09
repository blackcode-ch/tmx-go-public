// Local voice transcription (DESIGN.md "Voice"): the daemon downloads whisper.cpp's engine and a
// model on demand and transcribes on this machine. The UI shows the state ("voice.status"
// events), lets the owner set it up, and sends dictation (lib/dictation.ts).
import { create } from "zustand";
import { toast } from "sonner";
import { ApiError } from "./transport";
import { onBusEvent, useStore } from "./store";
import type { BusEvent, Transcript, TranscriptSegment } from "./types";

export interface VoiceProgress {
  done: number;
  total: number;
}

export interface VoiceEngine {
  /** the pinned whisper.cpp release, e.g. v1.9.5 */
  version: string;
  /** built for this platform */
  available: boolean;
  installed: boolean;
  size?: number;
  downloading?: VoiceProgress;
  error?: string;
  /** the release repository is private and tmx has no GitHub token (Settings → Updates) */
  needs_token?: boolean;
}

export interface VoiceModel {
  name: string;
  label: string;
  file: string;
  size: number;
  ram_mb: number;
  installed: boolean;
  default?: boolean;
  selected?: boolean;
  /** e.g. too heavy for this machine's memory */
  warning?: string;
  downloading?: VoiceProgress;
  error?: string;
}

export interface VoiceSettings {
	via_peer?: string;
  model: string;
  /** "auto" or a language code */
  language: string;
  threads: number;
  threads_auto: boolean;
  vad: boolean;
  auto_transcribe: boolean;
}

export interface VoiceJob {
  source: string;
  phase: "waiting" | "transcribing";
  position: number;
  part?: number;
  parts?: number;
  started_at: number;
}

/** GET /api/voice and the "voice.status" event. */
export interface VoiceStatus {
  platform: string;
  ready: boolean;
  /** not ready: why, and what to do */
  reason?: string;
  engine: VoiceEngine;
  models: VoiceModel[];
  vad: VoiceModel;
  settings: VoiceSettings;
  job?: VoiceJob;
  queued: number;
  memory_mb?: number;
  /** a download is running */
  busy: boolean;
}

export interface VoiceSettingsPatch {
	via_peer?: string;
  model?: string;
  language?: string;
  threads?: number;
  vad?: boolean;
  auto_transcribe?: boolean;
}

/** POST /api/transcribe: dictation's answer (not stored). */
export interface TranscribeResult {
	report?: string;
  text: string;
  language: string;
  segments: TranscriptSegment[];
}

export const useVoice = create<{ status: VoiceStatus | null; loaded: boolean; pending: boolean }>(() => ({ status: null, loaded: false, pending: false }));

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}

function keep(s: VoiceStatus) {
  useVoice.setState({ status: s, loaded: true });
  return s;
}

const enc = encodeURIComponent;

export const voiceApi = {
  get: () => t().request<VoiceStatus>("GET", "/api/voice").then(keep),
  configure: (b: VoiceSettingsPatch) => t().request<VoiceStatus>("PUT", "/api/voice", b).then(keep),
  setup: () => t().request<VoiceStatus>("POST", "/api/voice/setup").then(keep),
  getModel: (name: string) => t().request<VoiceStatus>("POST", `/api/voice/models/${enc(name)}`).then(keep),
  removeModel: (name: string) => t().request<VoiceStatus>("DELETE", `/api/voice/models/${enc(name)}`).then(keep),
  /** data: a base64 16 kHz mono PCM16 WAV of at most 2 minutes */
  transcribe: async (data: string, language?: string): Promise<TranscribeResult> => {
    const answer = await t().request<TranscribeResult | { state: "pending"; transcript_id: string }>("POST", "/api/transcribe", { data, ...(language ? { language } : {}) });
    if (!("transcript_id" in answer)) return answer;
    useVoice.setState({ pending: true });
    toast.info("Transcription pending", { description: "The recording is saved on this machine and will retry automatically." });
    try {
      for (;;) {
        await new Promise((resolve) => setTimeout(resolve, 2000));
        const tr = await voiceApi.transcript(answer.transcript_id).catch((e: unknown) => {
          if (e instanceof ApiError && e.status >= 400 && e.status < 500) throw e;
          return null; // the recording and retry live on the daemon; reconnect without re-uploading
        });
        if (!tr) continue;
        if (tr.state === "done") return tr;
        if (tr.state === "failed") throw new Error(tr.error || "Transcription failed; recording retained locally.");
      }
    } finally { useVoice.setState({ pending: false }); }
  },
  transcripts: (q?: { q?: string; source_prefix?: string; limit?: number }) => {
    const p = new URLSearchParams();
    if (q?.q) p.set("q", q.q);
    if (q?.source_prefix) p.set("source_prefix", q.source_prefix);
    if (q?.limit) p.set("limit", String(q.limit));
    const qs = p.toString();
    return t().request<Transcript[]>("GET", "/api/transcripts" + (qs ? "?" + qs : ""));
  },
  transcript: (id: string) => t().request<Transcript>("GET", `/api/transcripts/${enc(id)}`),
  deleteTranscript: (id: string) => t().request<void>("DELETE", `/api/transcripts/${enc(id)}`),
};

/** Loads the status once (an older daemon answers 404/501: then voice is simply absent). */
export async function loadVoice() {
  try {
    await voiceApi.get();
  } catch {
    useVoice.setState({ status: null, loaded: true });
  }
}

onBusEvent((ev: BusEvent) => {
  if (ev.type === "voice.status" && ev.data) useVoice.setState({ status: ev.data as VoiceStatus, loaded: true });
});

/** The languages offered in the picker (whisper knows about a hundred; these are the usual ones). */
export const VOICE_LANGUAGES: { code: string; label: string }[] = [
  { code: "auto", label: "Detect automatically" },
  { code: "en", label: "English" },
  { code: "de", label: "German" },
  { code: "fr", label: "French" },
  { code: "it", label: "Italian" },
  { code: "es", label: "Spanish" },
  { code: "pt", label: "Portuguese" },
  { code: "nl", label: "Dutch" },
  { code: "pl", label: "Polish" },
  { code: "sv", label: "Swedish" },
  { code: "da", label: "Danish" },
  { code: "no", label: "Norwegian" },
  { code: "fi", label: "Finnish" },
  { code: "cs", label: "Czech" },
  { code: "tr", label: "Turkish" },
  { code: "ru", label: "Russian" },
  { code: "uk", label: "Ukrainian" },
  { code: "ar", label: "Arabic" },
  { code: "hi", label: "Hindi" },
  { code: "zh", label: "Chinese" },
  { code: "ja", label: "Japanese" },
  { code: "ko", label: "Korean" },
];

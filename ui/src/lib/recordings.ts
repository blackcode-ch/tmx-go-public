// The owner's recordings as every client sees them (DESIGN.md "Recorder"): the live one (the
// indicator in every window, also on another browser or the phone) and the list for the
// Recordings page, kept current by recording.* events and reloaded on every (re)connect.
// The capture itself lives in recorder.ts and runs only in the window that records.
import { create } from "zustand";
import { onBusEvent, useStore } from "./store";
import { ApiError } from "./transport";
import type { BusEvent } from "./types";
import type { ChatMessage } from "./chat-types";
import type { ActiveRecording, Recording, RecorderSettings, RecordingKind, RecordingTranscript } from "./recording-types";
import { isLive } from "./recording-types";

interface RecordingsState {
  /** the daemon's live recording (from any window), null when none */
  active: Recording | null;
  settings: RecorderSettings | null;
  /** newest first; null until the page loaded it */
  list: Recording[] | null;
  loading: boolean;
  error: string | null;
  /** false when this daemon has no recorder (501) */
  available: boolean;
}

export const useRecordings = create<RecordingsState>(() => ({
  active: null,
  settings: null,
  list: null,
  loading: false,
  error: null,
  available: true,
}));

const set = useRecordings.setState;
const get = useRecordings.getState;

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}
const enc = encodeURIComponent;

export const recApi = {
  active: () => t().request<ActiveRecording>("GET", "/api/recordings/active"),
  list: (limit = 0) => t().request<Recording[] | null>("GET", `/api/recordings${limit ? `?limit=${limit}` : ""}`),
  get: (id: string) => t().request<Recording>("GET", `/api/recordings/${enc(id)}`),
  /** only the window that captures calls it, once capture is running */
  begin: (b: { kind: RecordingKind; mic: boolean; system_audio?: boolean; mime: string; title?: string; sensitivity?: "clinic" | "" }) => t().request<Recording>("POST", "/api/recordings", b),
  setState: (id: string, state: "paused" | "recording" | "stopped" | "heartbeat") => t().request<Recording>("POST", `/api/recordings/${enc(id)}/state`, { state }),
  rename: (id: string, title: string) => t().request<Recording>("PATCH", `/api/recordings/${enc(id)}`, { title }),
  /** "clinic": local models only for its report; "": any model (owner only) */
  setSensitivity: (id: string, sensitivity: "clinic" | "") => t().request<Recording>("PATCH", `/api/recordings/${enc(id)}`, { sensitivity }),
  remove: (id: string) => t().request<void>("DELETE", `/api/recordings/${enc(id)}`),
  ticket: (id: string) => t().request<{ ticket: string; expires_at: number }>("POST", `/api/recordings/${enc(id)}/ticket`),
  /** one segment as base64 (the desktop bridge, or when the window cannot load the daemon's URL) */
  segmentData: (id: string, n: number) =>
    t().request<{ type: string; size: number; data: string }>("GET", `/api/recordings/${enc(id)}/segments/${n}?data=1`),
  share: (id: string, chatId: string, body?: string, segments?: number[]) =>
    t().request<ChatMessage>("POST", `/api/recordings/${enc(id)}/share`, { chat_id: chatId, ...(body ? { body } : {}), ...(segments?.length ? { segments } : {}) }),
  transcribe: (id: string, language?: string) =>
    t().request<{ queued: number; source_prefix: string }>("POST", `/api/recordings/${enc(id)}/transcribe`, language ? { language } : {}),
  /** one still frame as base64 (the desktop bridge, or when the window cannot load the daemon's URL) */
  frameData: (id: string, ms: number) => t().request<{ type: string; size: number; data: string }>("GET", `/api/recordings/${enc(id)}/frames/${ms}?data=1`),
  /** the transcribe service's transcripts of a recording's segments (404 where it is not installed) */
  transcripts: (id: string) =>
    t().request<RecordingTranscript[] | null>("GET", `/api/transcripts?source_prefix=${enc(`recording:${id}/`)}`),
};

/** The URL a <video>/<audio> plays segment n from (http transport only). */
export function segmentURL(id: string, n: number, ticket: string): string {
  const base = useStore.getState().transport?.baseURL ?? "";
  return `${base}/api/recordings/${enc(id)}/segments/${n}?ticket=${enc(ticket)}`;
}

/** The URL an <img> shows frame ms from (http transport only). */
export function frameURL(id: string, ms: number, ticket: string): string {
  const base = useStore.getState().transport?.baseURL ?? "";
  return `${base}/api/recordings/${enc(id)}/frames/${ms}?ticket=${enc(ticket)}`;
}

export async function loadActive(): Promise<void> {
  try {
    const a = await recApi.active();
    set({ active: isLive(a.recording) ? a.recording : null, settings: a.settings, available: true });
  } catch (e) {
    if (e instanceof ApiError && (e.status === 501 || e.status === 404)) set({ available: false, active: null });
  }
}

let inflight: Promise<void> | null = null;

export function loadRecordings(): Promise<void> {
  if (inflight) return inflight;
  set((s) => ({ loading: !s.list }));
  inflight = (async () => {
    try {
      const list = (await recApi.list()) ?? [];
      set({ list, loading: false, error: null, available: true });
    } catch (e) {
      const off = e instanceof ApiError && (e.status === 501 || e.status === 404);
      set({ loading: false, error: off ? null : (e as Error).message, available: !off, list: off ? [] : get().list });
    } finally {
      inflight = null;
    }
  })();
  return inflight;
}

/** Puts a recording the daemon sent into the store (list and indicator). */
export function upsertRecording(r: Recording) {
  set((s) => {
    let list = s.list;
    if (list) {
      const i = list.findIndex((x) => x.id === r.id);
      if (i >= 0) {
        if (list[i].updated_at > r.updated_at) return {}; // an older copy
        list = list.slice();
        list[i] = r;
      } else list = [r, ...list].sort((a, b) => b.started_at - a.started_at);
    }
    const active = isLive(r) ? r : s.active?.id === r.id ? null : s.active;
    return { list, active };
  });
}

export function dropRecording(id: string) {
  set((s) => ({ list: s.list?.filter((x) => x.id !== id) ?? null, active: s.active?.id === id ? null : s.active }));
}

// Listeners for one recording's changes (the capturing window follows a pause or stop made
// in another window).
type Listener = (r: Recording) => void;
const listeners = new Set<Listener>();
export function onRecordingUpdated(fn: Listener) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

type TranscriptListener = (tr: RecordingTranscript) => void;
const trListeners = new Set<TranscriptListener>();
/** transcript.updated events (the transcribe service's; data: one transcript). */
export function onTranscriptUpdated(fn: TranscriptListener) {
  trListeners.add(fn);
  return () => {
    trListeners.delete(fn);
  };
}

onBusEvent((ev: BusEvent) => {
  switch (ev.type) {
    case "snapshot": // every (re)connect: catch up
      void loadActive();
      if (get().list) void loadRecordings();
      break;
    case "recording.updated": {
      const r = ev.data as Recording;
      if (!r?.id) break;
      upsertRecording(r);
      listeners.forEach((fn) => fn(r));
      break;
    }
    case "recording.removed": {
      const id = (ev.data as { id?: string })?.id;
      if (id) dropRecording(id);
      break;
    }
    case "transcript.updated": {
      const tr = ev.data as RecordingTranscript;
      if (tr?.source?.startsWith("recording:")) trListeners.forEach((fn) => fn(tr));
      break;
    }
  }
});

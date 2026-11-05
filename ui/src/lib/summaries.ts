// Summaries of recordings (DESIGN.md "Summaries"): Claude's short account of each finished
// recording, written from its transcripts and still frames; mirrors internal/model/summary.go,
// same JSON names. Kept current by summary.updated events; lists show the first sentence.
import { create } from "zustand";
import { onBusEvent, useStore } from "./store";
import { useRecordings } from "./recordings";
import type { BusEvent } from "./types";
import type { Recording } from "./recording-types";

export type SummaryState = NonNullable<Recording["summary_state"]> | "none";

export interface SummaryItem {
  who?: string;
  what: string;
}

/** t_ms from the start of the recording; segment and offset_ms are where the player seeks */
export interface SummaryChapter {
  t_ms: number;
  title: string;
  segment: number;
  offset_ms: number;
}

export interface RecordingSummary {
  recording_id: string;
  state: SummaryState;
  model?: string;
  summary: string;
  key_points: string[];
  decisions: string[];
  action_items: SummaryItem[];
  chapters: SummaryChapter[];
  transcripts: number;
  frames: number;
  parts: number;
  note?: string;
  error?: string;
  requested?: boolean;
  created_at: number;
  started_at?: number;
  finished_at?: number;
  updated_at: number;
}

/** GET /api/summaries/settings */
export interface SummarySettings {
  auto: boolean;
  model: string;
  default_model: string;
  models: string[];
  /** the claude that runs, or why there is none */
  claude?: string;
  claude_error?: string;
  /** transcription exists here, and its auto_transcribe (PUT /api/voice) */
  voice: boolean;
  auto_transcribe: boolean;
  running?: string;
}

export function summaryPending(s: SummaryState | undefined): boolean {
  return s === "transcribing" || s === "queued" || s === "running";
}

/** The first sentence of a summary (the list shows it). */
export function firstSentence(text: string | undefined): string {
  const t = (text ?? "").trim();
  const m = /^(.+?[.!?])(\s|$)/s.exec(t);
  return (m ? m[1] : t).trim();
}

type Brief = { state: SummaryState; summary: string };

interface SummariesState {
  /** full summaries, by recording id (loaded or announced) */
  byId: Record<string, RecordingSummary>;
  /** what lists show, by recording id: from the list's own fields, then from events */
  briefs: Record<string, Brief>;
  settings: SummarySettings | null;
}

export const useSummaries = create<SummariesState>(() => ({ byId: {}, briefs: {}, settings: null }));

function t() {
  const tr = useStore.getState().transport;
  if (!tr) throw new Error("Not connected yet.");
  return tr;
}
const enc = encodeURIComponent;

function keep(x: RecordingSummary): RecordingSummary {
  useSummaries.setState((s) => {
    const cur = s.byId[x.recording_id];
    if (cur && cur.updated_at > x.updated_at) return {};
    return {
      byId: { ...s.byId, [x.recording_id]: x },
      briefs: { ...s.briefs, [x.recording_id]: { state: x.state, summary: x.summary } },
    };
  });
  return x;
}

function keepSettings(st: SummarySettings): SummarySettings {
  useSummaries.setState({ settings: st });
  return st;
}

export const summaryApi = {
  get: (id: string) => t().request<RecordingSummary>("GET", `/api/recordings/${enc(id)}/summary`).then(keep),
  /** transcribe what is missing, then write it again */
  regenerate: (id: string) => t().request<RecordingSummary>("POST", `/api/recordings/${enc(id)}/summarize`).then(keep),
  settings: () => t().request<SummarySettings>("GET", "/api/summaries/settings").then(keepSettings),
  configure: (b: { auto?: boolean; model?: string }) => t().request<SummarySettings>("PUT", "/api/summaries/settings", b).then(keepSettings),
};

/** What a list shows of rec's summary. */
export function useSummaryBrief(rec: Recording): Brief | undefined {
  const b = useSummaries((s) => s.briefs[rec.id]);
  if (b) return b;
  return rec.summary_state ? { state: rec.summary_state, summary: rec.summary ?? "" } : undefined;
}

// A list from the daemon carries each recording's summary fields (recording.updated events do
// not): remember them, so a later event does not blank the row.
useRecordings.subscribe((s, prev) => {
  if (!s.list || s.list === prev.list) return;
  const add: Record<string, Brief> = {};
  for (const r of s.list) if (r.summary_state) add[r.id] = { state: r.summary_state, summary: r.summary ?? "" };
  if (Object.keys(add).length) useSummaries.setState((x) => ({ briefs: { ...x.briefs, ...add } }));
});

onBusEvent((ev: BusEvent) => {
  if (ev.type === "summary.updated") {
    const x = ev.data as RecordingSummary;
    if (x?.recording_id) keep(x);
  } else if (ev.type === "recording.removed") {
    const id = (ev.data as { id?: string })?.id;
    if (!id) return;
    useSummaries.setState((s) => {
      const byId = { ...s.byId };
      const briefs = { ...s.briefs };
      delete byId[id];
      delete briefs[id];
      return { byId, briefs };
    });
  }
});

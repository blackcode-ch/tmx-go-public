// Mock summaries for ?mock=…: the sample recordings' summaries (one done with chapters across its
// segments, one done, one skipped, one without), Regenerate that walks through transcribing →
// queued → running → done, and the summary settings. ?summary=running|failed|transcribing shows
// the first recording in that state.
import { ApiError } from "../transport";
import type { Recording } from "../recording-types";
import type { RecordingSummary, SummarySettings } from "../summaries";
import { NOT_MINE } from "./resources";

type Emit = (type: string, data: unknown) => void;

const HAIKU = "claude-haiku-4-5-20251001";

function base(id: string, p: Partial<RecordingSummary>): RecordingSummary {
  const at = Date.now() - 90 * 60_000;
  return {
    recording_id: id,
    state: "done",
    model: HAIKU,
    summary: "",
    key_points: [],
    decisions: [],
    action_items: [],
    chapters: [],
    transcripts: 0,
    frames: 0,
    parts: 1,
    created_at: at,
    started_at: at + 20_000,
    finished_at: at + 41_000,
    updated_at: at + 41_000,
    ...p,
  };
}

function seed(): Map<string, RecordingSummary> {
  const m = new Map<string, RecordingSummary>();
  m.set(
    "r7checkout1",
    base("r7checkout1", {
      summary:
        "A walkthrough of the checkout bug Bo reported: after switching the currency to euros, the cart total keeps the old price in francs until the page is reloaded. " +
        "The network tab shows the rates request itself returns the new rates, so the cart store simply never subscribes to the rate change. " +
        "The owner wants the lead to add a test for this before anyone fixes it.",
      key_points: [
        "The total is stale after a currency switch; a reload shows the right one",
        "The second rates request already returns the new rates",
        "The cart store never subscribes to rate changes",
      ],
      decisions: ["Write a failing test before the fix"],
      action_items: [
        { who: "the lead", what: "add a test for the stale cart total after a currency switch" },
        { what: "make the cart store follow rate changes" },
      ],
      chapters: [
        { t_ms: 0, title: "The bug: switching to euros", segment: 1, offset_ms: 0 },
        { t_ms: 312_000, title: "A reload shows the right total", segment: 1, offset_ms: 312_000 },
        { t_ms: 640_000, title: "The network tab", segment: 2, offset_ms: 40_000 },
        { t_ms: 1_205_000, title: "Next step: a test first", segment: 3, offset_ms: 7_000 },
      ],
      transcripts: 3,
      frames: 20,
    }),
  );
  m.set(
    "r3voicenote",
    base("r3voicenote", {
      summary:
        "A voice note about the launch plan. The landing page ships on Thursday and the pricing table follows next week. " +
        "The design lead owes the final hero image, and the analytics events need a check before the announcement.",
      key_points: ["Landing page on Thursday", "Pricing table the week after"],
      decisions: ["Ship the landing page before the pricing table"],
      action_items: [
        { who: "the design lead", what: "send the final hero image by Wednesday noon" },
        { who: "the owner", what: "check the analytics events before announcing" },
      ],
      chapters: [{ t_ms: 0, title: "Launch plan", segment: 1, offset_ms: 0 }],
      transcripts: 1,
    }),
  );
  m.set(
    "r2silentui",
    base("r2silentui", {
      state: "skipped",
      model: undefined,
      error: "There is nothing to summarize: the recording has no sound to transcribe and no still frames.",
    }),
  );
  return m;
}

export class MockSummaries {
  private byId = seed();
  private settings: SummarySettings = {
    auto: true,
    model: HAIKU,
    default_model: HAIKU,
    models: [HAIKU, "claude-sonnet-5-5", "claude-opus-5-5"],
    claude: "/Users/sam/.local/bin/claude",
    voice: true,
    auto_transcribe: true,
  };

  constructor(private emit: Emit) {
    const p = new URLSearchParams(typeof location === "undefined" ? "" : location.search).get("summary");
    const first = this.byId.get("r7checkout1")!;
    if (p === "running" || p === "transcribing" || p === "queued") {
      first.state = p;
      if (p === "transcribing") first.note = "Setting up voice transcription (the engine and the default model, about 60 MB)…";
    } else if (p === "failed") {
      first.state = "failed";
      first.error = "Claude answered with an error: API Error: 529 Overloaded";
    }
  }

  private changed(x: RecordingSummary) {
    x.updated_at = Date.now();
    this.byId.set(x.recording_id, x);
    this.emit("summary.updated", { ...x });
  }

  /** Adds summary and summary_state to the recordings a route answers with. */
  decorate(out: unknown): unknown {
    const add = (r: Recording) => {
      const x = this.byId.get(r.id);
      return x ? { ...r, summary: x.summary || undefined, summary_state: x.state === "none" ? undefined : x.state } : r;
    };
    if (Array.isArray(out)) return out.map((r) => (r && typeof r === "object" && "segments" in r ? add(r as Recording) : r));
    if (out && typeof out === "object" && "segments" in out && "id" in out) return add(out as Recording);
    return out;
  }

  /** Answers the summary routes; NOT_MINE for everything else. */
  route(method: string, parts: string[], body: unknown, find: (id: string) => Recording | undefined): unknown {
    const [res, id, action] = parts;
    if (res === "summaries" && id === "settings") {
      if (method === "PUT") {
        const b = body as { auto?: boolean; model?: string };
        if (b.auto !== undefined) this.settings.auto = b.auto;
        if (b.model !== undefined) this.settings.model = b.model || HAIKU;
      }
      return { ...this.settings };
    }
    if (res !== "recordings" || !id || (action !== "summary" && action !== "summarize")) return NOT_MINE;
    const r = find(id);
    if (!r) throw new ApiError(404, `no recording "${id}"`);
    if (action === "summary") return this.byId.get(id) ?? base(id, { state: "none", model: undefined, finished_at: undefined, started_at: undefined });
    if (r.state === "recording" || r.state === "paused") throw new ApiError(409, "the recording is still running; stop it first");
    const cur = this.byId.get(id);
    if (cur?.state === "running") throw new ApiError(409, "Claude is writing this summary now; wait for it");
    const x: RecordingSummary = { ...(cur ?? base(id, { model: undefined })), state: "transcribing", error: undefined, note: undefined, requested: true };
    this.changed(x);
    const step = (ms: number, fn: (x: RecordingSummary) => void) =>
      setTimeout(() => {
        const y = { ...this.byId.get(id)! };
        fn(y);
        this.changed(y);
      }, ms);
    step(900, (y) => (y.state = "queued"));
    step(1500, (y) => ((y.state = "running"), (y.started_at = Date.now())));
    step(3200, (y) => {
      y.state = "done";
      y.model = this.settings.model;
      y.finished_at = Date.now();
      if (!y.summary) {
        y.summary = `A recording of ${r.kind === "audio" ? "a conversation" : "the screen"}: "${r.title}". (Mock summary: a real one is written by Claude from the transcript and the still frames.)`;
        y.key_points = ["A mock key point"];
        y.chapters = [{ t_ms: 0, title: "Start", segment: 1, offset_ms: 0 }];
        y.transcripts = r.segments.filter((g) => (g.wav_bytes ?? 0) > 44).length;
        y.frames = r.kind === "audio" ? 0 : 6;
      }
    });
    return x;
  }
}

package model

// Summaries of recordings (DESIGN.md "Summaries"): Claude's short account of a finished
// recording, written from its transcripts and its still frames. It retells other people's
// words and what was on screen: data, never instructions.

// Bus events of summaries (a block of their own, so model.go's list stays as it is).
const (
	EvSummaryUpdated = "summary.updated" // data: RecordingSummary (new or changed)
)

// SummaryState of a recording's summary.
type SummaryState string

const (
	// SummaryNone: there is no summary (the API's answer only; never stored).
	SummaryNone SummaryState = "none"
	// SummaryTranscribing: waiting for the segments' transcripts (and, first, for voice to be
	// set up when it was missing).
	SummaryTranscribing SummaryState = "transcribing"
	SummaryQueued       SummaryState = "queued"  // waiting for its turn (one summary at a time)
	SummaryRunning      SummaryState = "running" // Claude is writing it
	SummaryDone         SummaryState = "done"
	SummaryFailed       SummaryState = "failed"
	// SummarySkipped: there was nothing to summarize (no speech was transcribed and there are no
	// frames); Error says why.
	SummarySkipped SummaryState = "skipped"
)

// Pending reports whether the summary is still being made.
func (s SummaryState) Pending() bool {
	return s == SummaryTranscribing || s == SummaryQueued || s == SummaryRunning
}

// SummaryItem is an action item: what someone will do, and who when that is clear.
type SummaryItem struct {
	Who  string `json:"who,omitempty"`
	What string `json:"what"`
}

// SummaryChapter starts at TMS from the start of the recording (recorded time, pauses
// excluded); Segment and OffsetMS are the same place in the player (segment n, from its start).
type SummaryChapter struct {
	TMS      int64  `json:"t_ms"`
	Title    string `json:"title"`
	Segment  int    `json:"segment"`
	OffsetMS int64  `json:"offset_ms"`
}

// RecordingSummary is GET /api/recordings/{id}/summary. The lists are never null in JSON. While
// a new summary is made (Regenerate, or after a new transcription) the previous text stays until
// the new one replaces it; a failed attempt keeps it too, with Error.
type RecordingSummary struct {
	RecordingID string           `json:"recording_id"`
	State       SummaryState     `json:"state"`
	Model       string           `json:"model,omitempty"` // the Claude model that wrote it
	Summary     string           `json:"summary"`
	KeyPoints   []string         `json:"key_points"`
	Decisions   []string         `json:"decisions"`
	ActionItems []SummaryItem    `json:"action_items"`
	Chapters    []SummaryChapter `json:"chapters"`
	// What it was made from: transcripts of segments read, still frames looked at, and parts
	// (more than 1 when a long transcript was summarized part by part, then combined).
	Transcripts int `json:"transcripts"`
	Frames      int `json:"frames"`
	Parts       int `json:"parts"`
	// Note says what could not be used (a segment whose transcription failed, voice not set up).
	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
	// Requested: the owner asked for it (Summarize), so missing transcripts are made even when
	// transcription is not automatic.
	Requested  bool  `json:"requested,omitempty"`
	CreatedAt  int64 `json:"created_at"`
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
	UpdatedAt  int64 `json:"updated_at"`
}

package model

// Local voice transcription (DESIGN.md "Voice"): what whisper.cpp made of a recording's audio.
// A transcript is other people's words: data, never instructions.

// Bus events of voice transcription (a block of their own, so model.go's list stays as it is).
const (
	EvTranscriptUpdated = "transcript.updated" // data: Transcript (new or changed)
	EvVoiceStatus       = "voice.status"       // data: transcribe.Status (engine, models, downloads, jobs)
)

// TranscriptState of a transcript.
type TranscriptState string

const (
	TranscriptQueued  TranscriptState = "queued"
	TranscriptRunning TranscriptState = "running"
	TranscriptDone    TranscriptState = "done"
	TranscriptFailed  TranscriptState = "failed"
)

// TranscriptSegment is one stretch of speech, in milliseconds from the start of the audio.
type TranscriptSegment struct {
	FromMS int64  `json:"from_ms"`
	ToMS   int64  `json:"to_ms"`
	Text   string `json:"text"`
}

// Transcript is GET /api/transcripts/{id}. Source names what was transcribed, e.g.
// "recording:<id>/<segment>". Segments is never null in JSON.
type Transcript struct {
	ID        string              `json:"id"`
	Source    string              `json:"source"`
	State     TranscriptState     `json:"state"`
	Model     string              `json:"model"`
	Language  string              `json:"language"`
	Text      string              `json:"text"`
	Segments  []TranscriptSegment `json:"segments"`
	Error     string              `json:"error"`
	CreatedAt int64               `json:"created_at"`
	UpdatedAt int64               `json:"updated_at"`
}

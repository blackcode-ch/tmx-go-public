package model

// Recordings (DESIGN.md "Recorder"): the owner's own screen (whole or one window), camera,
// microphone and device audio, captured in the owner's window and stored by the daemon in
// segments. The daemon never captures anything itself.

type RecordingKind string

const (
	RecordingScreen RecordingKind = "screen" // the whole screen (with the microphone when it is on)
	RecordingWindow RecordingKind = "window" // one window
	RecordingAudio  RecordingKind = "audio"  // the microphone only
	RecordingCamera RecordingKind = "camera" // a clip from the camera (with the microphone when it is on)
)

type RecordingState string

const (
	RecordingActive      RecordingState = "recording"
	RecordingPaused      RecordingState = "paused"
	RecordingDone        RecordingState = "done"
	RecordingInterrupted RecordingState = "interrupted" // no chunks for a while (the window closed); what arrived is kept
	RecordingFailed      RecordingState = "failed"
)

// RecordingSource is who writes a recording's files.
type RecordingSource string

const (
	// RecordingSourceWeb: the window's MediaRecorder streams chunks and the daemon writes the files.
	RecordingSourceWeb RecordingSource = "web"
	// RecordingSourceNative: the macOS app captures with ScreenCaptureKit, writes the files into the
	// recording's folder itself and reports them (DESIGN.md "Recorder", native capture).
	RecordingSourceNative RecordingSource = "native"
)

// Live reports whether the recording is still being captured (recording or paused).
func (s RecordingState) Live() bool { return s == RecordingActive || s == RecordingPaused }

type Recording struct {
	ID    string         `json:"id"`
	Title string         `json:"title"`
	Kind  RecordingKind  `json:"kind"`
	State RecordingState `json:"state"`
	// Mime of the media files, e.g. video/mp4;codecs=avc1 or audio/webm;codecs=opus.
	Mime string `json:"mime"`
	Mic  bool   `json:"mic"` // the microphone is recorded (then every segment has a 16 kHz WAV)
	// SystemAudio: the device's own sound (what its speakers play) was captured with the screen
	// and mixed with the microphone; then every segment has a 16 kHz WAV too.
	SystemAudio bool `json:"system_audio"`
	// Sensitivity: SensitivityClinic = medical patient data (Metaesthetics, Aurea; nLPD): its report is written on
	// our own machines only, and fails with the reason when there is no local model. "" = a cloud model may write it.
	Sensitivity string `json:"sensitivity,omitempty"`
	// Dir is the recording's folder (absolute); every segment's files are in it.
	Dir      string             `json:"dir"`
	Segments []RecordingSegment `json:"segments"`
	// Frames: still pictures for summaries, in recorded-time order (absolute paths; none for
	// audio). They are files in frames/ inside Dir, so they need no table.
	Frames []RecordingFrame `json:"frames,omitempty"`
	Bytes  int64            `json:"bytes"` // media bytes, all segments
	// DurationMS: while live, the recorded time before ResumedAt; once finished, the total.
	DurationMS int64 `json:"duration_ms"`
	// ResumedAt: while recording, when the current stretch began (elapsed = DurationMS +
	// now - ResumedAt); 0 otherwise.
	ResumedAt int64  `json:"resumed_at,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	// Source: web (chunks from the window) or native (the macOS app writes the files).
	Source RecordingSource `json:"source"`
	// Stopping: a native recording was asked to stop and the app is still finishing its last
	// part; it ends when the app reports that part, or after the sweep's 20 s.
	Stopping bool `json:"stopping,omitempty"`
	// Summary is the text of the recording's summary and SummaryState where it is
	// (internal/summarize). The API fills them in lists and reads; they are not stored here.
	Summary      string       `json:"summary,omitempty"`
	SummaryState SummaryState `json:"summary_state,omitempty"`
}

// RecordingSegment is one independently playable media file of at most about 20 MB / 10
// minutes, with its 16 kHz mono PCM16 WAV next to it when the microphone is recorded.
type RecordingSegment struct {
	N          int    `json:"n"`    // 1, 2, …
	File       string `json:"file"` // absolute path of the media file (seg-0001.mp4)
	Bytes      int64  `json:"bytes"`
	WAV        string `json:"wav,omitempty"` // absolute path of seg-0001.wav ("" without a microphone)
	WAVBytes   int64  `json:"wav_bytes,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	SHA256     string `json:"sha256,omitempty"` // of the media file, once finished
	StartedAt  int64  `json:"started_at"`
}

// RecordingFrame is one still JPEG of a screen or camera recording: frames/f-000030000.jpg is
// at 0:30 of recorded time (pauses excluded), taken at the start, every 30 s and at stop.
type RecordingFrame struct {
	MS    int64  `json:"ms"`   // recorded time
	Path  string `json:"path"` // absolute path of the JPEG
	Bytes int64  `json:"bytes"`
	// Key: a key moment (the window saw the picture change), not only the 30-s still.
	Key bool `json:"key,omitempty"`
}

// Bus events of the recorder.
const (
	EvRecordingUpdated = "recording.updated" // data: Recording (new, changed or finished)
	EvRecordingRemoved = "recording.removed" // data: {"id"}
)

// SensitivityClinic marks a recording of medical patient data: local models only (nLPD).
const SensitivityClinic = "clinic"

// Clinic reports whether the recording holds clinic patient data (local models only).
func (r Recording) Clinic() bool { return r.Sensitivity == SensitivityClinic }

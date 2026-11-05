package config

// VoiceConfig is config.json "voice": whisper.cpp transcription (DESIGN.md "Voice"),
// with explicitly enabled fallback to configured local daemon hosts.
// Every field is optional; internal/transcribe fills the defaults.
type VoiceConfig struct {
	// FallbackHosts permits dictation on the explicitly configured local hosts when
	// this engine is busy or unavailable. Off by default; Overview access alone is read-only.
	FallbackHosts bool `json:"fallback_hosts,omitempty"`
	// Model is the catalog name of the model jobs use ("" = base-q5_1).
	Model string `json:"model,omitempty"`
	// Language: a whisper language code ("de", "en", ...); "" or "auto" = detected per file.
	Language string `json:"language,omitempty"`
	// Threads the engine uses; 0 = min(4, CPUs/2).
	Threads int `json:"threads,omitempty"`
	// VAD skips silence with the Silero model (fewer invented words); nil = on when downloaded.
	VAD *bool `json:"vad,omitempty"`
	// AutoTranscribe transcribes finished recordings by themselves; nil = on (setting voice up
	// first when it is missing).
	AutoTranscribe *bool `json:"auto_transcribe,omitempty"`
}

// AutoTranscribeOn reports config voice.auto_transcribe (default on).
func (v VoiceConfig) AutoTranscribeOn() bool { return v.AutoTranscribe == nil || *v.AutoTranscribe }

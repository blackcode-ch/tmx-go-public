package transcribe

import (
	"context"
	"encoding/json"
)

// DictationCapture is optional browser telemetry. Only the fields and values
// listed here are written to the diagnostic log.
type DictationCapture struct {
	Path                 string `json:"path"`
	Processor            string `json:"processor"`
	TrackMuted           *bool  `json:"track_muted"`
	TrackEnded           *bool  `json:"track_ended"`
	Permission           string `json:"permission"`
	Interruption         string `json:"interruption"`
	HiddenAtStart        *bool  `json:"hidden_at_start"`
	HiddenEvents         *int   `json:"hidden_events"`
	VisibleEvents        *int   `json:"visible_events"`
	ContextInterruptions *int   `json:"context_interruptions"`
	ContextState         string `json:"context_state"`
	Samples              *int   `json:"samples"`
	DurationMS           *int   `json:"duration_ms"`
}

type dictationCaptureKey struct{}

// WithDictationCapture marks an incoming composer upload, including legacy
// requests with no telemetry. Malformed optional telemetry never blocks audio.
func WithDictationCapture(ctx context.Context, raw json.RawMessage) context.Context {
	var capture DictationCapture
	if len(raw) > 1024 || (len(raw) != 0 && json.Unmarshal(raw, &capture) != nil) {
		capture = DictationCapture{}
	}
	return context.WithValue(ctx, dictationCaptureKey{}, capture)
}

const captureUnavailable = "unavailable"

func captureChoice(value string, choices ...string) string {
	for _, choice := range choices {
		if value == choice {
			return choice
		}
	}
	return captureUnavailable
}

func captureFlag(value *bool) any {
	if value == nil {
		return captureUnavailable
	}
	return *value
}

func captureCount(value *int, max int) any {
	if value == nil || *value < 0 || *value > max {
		return captureUnavailable
	}
	return *value
}

func (s *Service) logDictationCapture(audio []byte, w wavInfo, reason string, c DictationCapture) {
	var duration, frames, rms, peak any = captureUnavailable, captureUnavailable, captureUnavailable, captureUnavailable
	if w.Rate > 0 && w.Channels > 0 && w.Bits >= 8 {
		duration = w.Duration().Seconds()
		frames = w.DataSize / (int64(w.Channels) * int64(w.Bits/8))
		if w.pcm16() && w.DataSize >= 2 {
			rms, peak = pcmLevels(audio, w)
		}
	}
	reason = captureChoice(reason, "transcribed", "no_speech", "too_long", "invalid_language", "invalid_audio", "transcription_failed", "unexpected_language", "short", "suspect")
	s.log.Info("voice: dictation capture", "source", "dictation", "reason", reason,
		"duration_s", duration, "samples", frames, "rms", rms, "peak", peak,
		"capture_path", captureChoice(c.Path, "browser", "installed", "desktop"),
		"processor", captureChoice(c.Processor, "worklet", "script_processor"),
		"track_muted", captureFlag(c.TrackMuted), "track_ended", captureFlag(c.TrackEnded),
		"permission", captureChoice(c.Permission, "granted", "prompt", "denied"),
		"interruption", captureChoice(c.Interruption, "mute", "ended"),
		"hidden_at_start", captureFlag(c.HiddenAtStart),
		"hidden_events", captureCount(c.HiddenEvents, 255),
		"visible_events", captureCount(c.VisibleEvents, 255),
		"context_interruptions", captureCount(c.ContextInterruptions, 255),
		"context_state", captureChoice(c.ContextState, "running", "suspended", "interrupted", "closed"),
		"client_samples", captureCount(c.Samples, 2_000_000),
		"client_duration_ms", captureCount(c.DurationMS, 125_000))
}

package transcribe

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestDictationCaptureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, capture, reason string
		silent                bool
		fields                []string
	}{
		{"normal", `{"path":"installed","processor":"worklet","track_muted":false,"permission":"granted","samples":16000,"duration_ms":1000,"device":"private-input"}`, "transcribed", false,
			[]string{`"capture_path":"installed"`, `"processor":"worklet"`, `"track_muted":false`, `"client_samples":16000`, `"permission":"granted"`}},
		{"silent", `{"path":"browser","track_muted":true,"track_ended":true,"context_state":"suspended","interruption":"mute","hidden_at_start":false,"hidden_events":2,"visible_events":1,"context_interruptions":1}`, "no_speech", true,
			[]string{`"rms":0`, `"peak":0`, `"context_state":"suspended"`, `"track_ended":true`, `"hidden_events":2`, `"visible_events":1`, `"context_interruptions":1`, `"interruption":"mute"`, `"hidden_at_start":false`}},
		{"legacy", "", "transcribed", false, []string{`"capture_path":"unavailable"`, `"track_muted":"unavailable"`}},
		{"malformed", `{"track_muted":"private-input"}`, "transcribed", false, []string{`"track_muted":"unavailable"`}},
		{"unrecognized", `{"path":"private-input","samples":-1}`, "transcribed", false, []string{`"capture_path":"unavailable"`, `"client_samples":"unavailable"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hosts := &fakeHosts{hosts: []TranscriptionHost{{ID: "scratch", Ready: true}}}
			e := newEnv(t, noEngine)
			var logs bytes.Buffer
			e.s.log = slog.New(slog.NewJSONHandler(&logs, nil))
			ctx := WithDictationCapture(t.Context(), []byte(tc.capture))
			result, err := e.s.DictateWithHosts(ctx, fallbackWAV(tc.silent), "fr", hosts)
			if tc.silent {
				if !errors.Is(err, ErrNoSpeech) || hosts.calls != 0 {
					t.Fatalf("silent: err=%v calls=%d", err, hosts.calls)
				}
			} else if err != nil || result.Text != "remote words" || hosts.calls != 1 {
				t.Fatalf("normal: err=%v calls=%d", err, hosts.calls)
			}
			log := logs.String()
			if strings.Count(log, "voice: dictation capture") != 1 || strings.Contains(log, "remote words") || strings.Contains(log, "private-input") {
				t.Fatalf("unexpected diagnostic log: %s", log)
			}
			want := append(tc.fields, `"reason":"`+tc.reason+`"`, `"samples":16000`, `"duration_s":1`)
			for _, field := range want {
				if !strings.Contains(log, field) {
					t.Errorf("missing %s in %s", field, log)
				}
			}
		})
	}
}

package mcp

import (
	"bytes"
	"image"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

// Agents see device audio and the still frames of a screen recording, by absolute path.
func TestRecordingsToolFrames(t *testing.T) {
	d := apitest.New(t)
	cs := session(t, d, "head")
	r, err := d.Recorder.Begin(recorder.BeginRequest{Kind: model.RecordingScreen, Mic: true, SystemAudio: true, Mime: "video/webm;codecs=vp9,opus", Title: "Standup"})
	if err != nil {
		t.Fatal(err)
	}
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewGray(image.Rect(0, 0, 64, 36)), nil)
	f0, err := d.Recorder.PutFrame(r.ID, 0, jpg.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	f1, _ := d.Recorder.PutFrame(r.ID, 30_000, jpg.Bytes())
	d.Recorder.Append(r.ID, 1, recorder.TrackMedia, 0, 0, []byte("webm"))
	d.Recorder.SetState(r.ID, "stopped")

	out := mustOK(t, cs, "recordings", nil)
	if !strings.Contains(out, "with the microphone and device audio") || !strings.Contains(out, "2 frame(s) (JPEG) in ") || strings.Contains(out, f1.Path) {
		t.Fatalf("list: %s", out)
	}
	out = mustOK(t, cs, "recordings", map[string]any{"id": r.ID})
	if !strings.Contains(out, "frame 0s · "+f0.Path) || !strings.Contains(out, "frame 30s · "+f1.Path) {
		t.Fatalf("one: %s", out)
	}
}

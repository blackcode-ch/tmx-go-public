package mcp

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

func TestRecordingsTool(t *testing.T) {
	d := apitest.New(t)
	cs := session(t, d, "head")
	if out := mustOK(t, cs, "recordings", nil); out != "The owner has no recordings." {
		t.Fatalf("empty: %s", out)
	}
	// the owner records (through the API in real life)
	r, err := d.Recorder.Begin(recorder.BeginRequest{Kind: model.RecordingAudio, Mic: true, Mime: "audio/webm;codecs=opus", Title: "Call with Bo"})
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		if _, err := d.Recorder.Append(r.ID, n, recorder.TrackMedia, 0, 0, []byte("opus")); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Recorder.Append(r.ID, n, recorder.TrackPCM, 0, 0, make([]byte, 32000)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Recorder.SetState(r.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	full, _ := d.Recorder.Get(r.ID)
	out := mustOK(t, cs, "recordings", nil)
	if !strings.HasPrefix(out, RecordingsNote) || !strings.Contains(out, r.ID+` · "Call with Bo" · audio · done`) ||
		!strings.Contains(out, "4s · audio/webm;codecs=opus · 4 segment(s)") || !strings.Contains(out, "#1 1s · media "+full.Segments[0].File) ||
		!strings.Contains(out, "wav "+full.Segments[2].WAV) || strings.Contains(out, full.Segments[3].WAV) || !strings.Contains(out, "… 1 more") {
		t.Fatalf("list: %s", out)
	}
	out = mustOK(t, cs, "recordings", map[string]any{"id": r.ID})
	if !strings.Contains(out, "#4 1s · media "+full.Segments[3].File) || !strings.Contains(out, "wav "+full.Segments[3].WAV) {
		t.Fatalf("one: %s", out)
	}
	if out, isErr := call(t, cs, "recordings", map[string]any{"id": "nope"}); !isErr || !strings.Contains(out, "no recording") {
		t.Fatalf("unknown: %s", out)
	}
	// a worker sees them too: every local agent may
	if _, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if out := mustOK(t, session(t, d, "alpha-lead"), "recordings", nil); !strings.Contains(out, "Call with Bo") {
		t.Fatalf("lead: %s", out)
	}
}

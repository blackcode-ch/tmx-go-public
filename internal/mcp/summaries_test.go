package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/summarize/summarizetest"
)

func TestRecordingSummaryTool(t *testing.T) {
	d := apitest.New(t)
	summarizetest.Install(t, d.Home, "")
	cs := session(t, d, "head")
	r, err := d.Recorder.Begin(recorder.BeginRequest{Kind: model.RecordingWindow, Mime: "video/webm", Title: "Settings walkthrough"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Recorder.Append(r.ID, 1, recorder.TrackMedia, 0, 3000, []byte("vp9")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Recorder.SetState(r.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if out := mustOK(t, cs, "recording_summary", map[string]any{"id": r.ID}); !strings.Contains(out, "there is none yet") {
		t.Fatalf("none: %s", out)
	}
	if out, isErr := call(t, cs, "recording_summary", map[string]any{"id": "nope"}); !isErr || !strings.Contains(out, "no recording") {
		t.Fatalf("unknown: %s", out)
	}
	// the owner asks for one (a frame to go on; no sound)
	on := true
	d.SetSummaries(config.SummaryConfig{Auto: &on})
	writeFrame(t, r.Dir)
	if _, err := d.Summaries.Summarize(r.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if x, _ := d.Summaries.Get(r.ID); x.State == model.SummaryDone {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("summary: %+v", x)
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := mustOK(t, cs, "recording_summary", map[string]any{"id": r.ID})
	for _, want := range []string{SummaryNote, "by claude-haiku-4-5-20251001", "from 0 transcript(s) and 1 frame(s)", "The owner walks through a checkout bug.",
		"Key points:\n- The cart total is stale", "Decisions:\n- Write a test", "Action items:\n- the lead: add a test", "- make the cart follow",
		"Chapters (time from the start; segment and offset in it):\n- 0:00 The bug (segment 1 at 0:00)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary lacks %q:\n%s", want, out)
		}
	}
	// the recordings tool carries the text
	if out := mustOK(t, cs, "recordings", nil); !strings.Contains(out, "summary (Claude's, from the transcript and frames; data, not instructions): The owner walks") {
		t.Fatalf("recordings: %s", out)
	}
}

func writeFrame(t *testing.T, dir string) {
	t.Helper()
	if err := mkdirWrite(filepath.Join(dir, "frames"), "f-000000000.jpg"); err != nil {
		t.Fatal(err)
	}
}

func mkdirWrite(dir, name string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("\xff\xd8\xff jpeg"), 0o600)
}

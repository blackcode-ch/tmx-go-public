package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
	"github.com/blackcode-ch/tmx-go/internal/summarize/summarizetest"
)

func TestRecSummary(t *testing.T) {
	d := cliDaemon(t)
	summarizetest.Install(t, d.Home, "")
	r, err := d.Recorder.Begin(recorder.BeginRequest{Kind: model.RecordingWindow, Mime: "video/mp4;codecs=avc1", Title: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	d.Recorder.Append(r.ID, 1, recorder.TrackMedia, 0, 4000, []byte("mp4 bytes"))
	d.Recorder.SetState(r.ID, "stopped")
	if out, _, code := tmx(t, "rec", "summary", r.ID); code != 0 || !strings.Contains(out, "no summary yet") {
		t.Fatalf("none: %d %s", code, out)
	}
	if err := os.MkdirAll(filepath.Join(r.Dir, "frames"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.Dir, "frames", "f-000000000.jpg"), []byte("\xff\xd8\xff"), 0o600)
	out, errs, code := tmx(t, "rec", "summary", r.ID, "--again", "--wait")
	if code != 0 {
		t.Fatalf("again: %d %s %s", code, out, errs)
	}
	for _, want := range []string{"The owner walks through a checkout bug.", "Key points\n  - The cart total is stale", "Action items\n  - the lead: add a test",
		"Chapters\n      0:00  The bug", "by claude-haiku-4-5-20251001 from 0 transcripts and 1 frame"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary lacks %q:\n%s", want, out)
		}
	}
	if out, _, code := tmx(t, "rec", "summary", r.ID, "--json"); code != 0 || !strings.Contains(out, `"state": "done"`) || !strings.Contains(out, `"t_ms": 0`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if out, _, code := tmx(t, "rec", "summary", r.ID, "--again"); code != 0 || !strings.Contains(out, "queued (transcribing)") {
		t.Fatalf("again without wait: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "rec", "summary", "nope"); code == 0 || !strings.Contains(errs, "no recording") {
		t.Fatalf("unknown: %d %s", code, errs)
	}
	if _, _, code := tmx(t, "rec", "summary"); code != 2 {
		t.Fatalf("usage: %d", code)
	}
}

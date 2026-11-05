package cli

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/recorder"
)

func TestRecCommands(t *testing.T) {
	d := cliDaemon(t)
	if out, _, code := tmx(t, "rec", "ls"); code != 0 || !strings.Contains(out, "no recordings") {
		t.Fatalf("empty: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "rec", "start"); code == 0 || !strings.Contains(errs, "unknown rec command") {
		t.Fatalf("rec start exists: %d %s", code, errs)
	}
	r, err := d.Recorder.Begin(recorder.BeginRequest{Kind: model.RecordingScreen, Mic: true, Mime: "video/mp4;codecs=avc1", Title: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	d.Recorder.Append(r.ID, 1, recorder.TrackMedia, 0, 0, []byte("mp4 bytes"))
	d.Recorder.Append(r.ID, 1, recorder.TrackPCM, 0, 0, make([]byte, 64000))
	d.Recorder.SetState(r.ID, "stopped")
	if out, _, code := tmx(t, "rec", "ls"); code != 0 || !strings.Contains(out, r.ID) || !strings.Contains(out, "screen  done   0:02") || !strings.Contains(out, "Demo") {
		t.Fatalf("ls: %d %s", code, out)
	}
	if out, _, code := tmx(t, "rec", "show", r.ID); code != 0 || !strings.Contains(out, "seg-0001.mp4") || !strings.Contains(out, "16 kHz WAV") {
		t.Fatalf("show: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "rec", "rm", r.ID); code == 0 || !strings.Contains(errs, "--yes") {
		t.Fatalf("rm without --yes: %d %s", code, errs)
	}
	if out, _, code := tmx(t, "rec", "rm", r.ID, "--yes"); code != 0 || !strings.Contains(out, "deleted") {
		t.Fatalf("rm: %d %s", code, out)
	}
	if _, errs, code := tmx(t, "rec", "show", r.ID); code == 0 || !strings.Contains(errs, "no recording") {
		t.Fatalf("show deleted: %d %s", code, errs)
	}
}

// `tmx rec process` (the report worker's one command) takes exactly one recording id, nothing else, and runs only
// inside the worker (its agent env).
func TestRecProcessArgs(t *testing.T) {
	t.Setenv(fleet.EnvURL, "")
	t.Setenv(fleet.EnvToken, "")
	for _, args := range [][]string{{}, {"abc", "--json"}, {"abc; rm -rf /"}, {"../x"}, {"ABC"}, {strings.Repeat("a", 33)}} {
		if _, errs, code := tmx(t, append([]string{"rec", "process"}, args...)...); code == 0 || !strings.Contains(errs, "usage") {
			t.Fatalf("%q: %d %s", args, code, errs)
		}
	}
	if _, errs, code := tmx(t, "rec", "process", "abc123"); code == 0 || !strings.Contains(errs, "only inside the report worker") {
		t.Fatalf("outside a worker: %d %s", code, errs)
	}
}

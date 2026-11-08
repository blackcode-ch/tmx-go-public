package summarize

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/transcribe"
)

type fleetVoice struct {
	Voice
	down  atomic.Bool
	calls atomic.Int32
}

func (v *fleetVoice) ViaPeer() string { return "hosta" }
func (v *fleetVoice) FleetReport(_ context.Context, host, id, text string) (string, error) {
	v.calls.Add(1)
	if v.down.Load() {
		return "", transcribe.ErrPending
	}
	return "Fleet report: " + text, nil
}

func TestFleetRecordingNeverUsesCloudSummary(t *testing.T) {
	v := &fleetVoice{}
	v.down.Store(true)
	e := newEnv(t, "fail", func(o *Options) { v.Voice = o.Voice; o.Voice = v })
	r := e.record(model.RecordingAudio, true, 1, 2)
	e.stop(r.ID)
	e.wait(r.ID, model.SummaryTranscribing)
	// Explicitly exercise pending and completion with the cloud backend selected.
	deadline := time.Now().Add(3 * time.Second)
	for v.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v.calls.Load() == 0 || len(e.runs()) != 0 {
		t.Fatal("peer outage did not stay pending without cloud")
	}
	v.down.Store(false)
	got := e.wait(r.ID, model.SummaryDone)
	if got.Model != "peer:hosta" || got.Summary == "" || len(e.runs()) != 0 || v.calls.Load() == 0 {
		t.Fatalf("cloud boundary failed: %+v, cloud runs=%d", got, len(e.runs()))
	}
}

package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestSummaries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InsertRecording(model.Recording{ID: "r1", Title: "a", Kind: model.RecordingAudio, State: model.RecordingDone, Mime: "audio/webm", StartedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSummary("r1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("none: %v", err)
	}
	x := model.RecordingSummary{RecordingID: "r1", State: model.SummaryDone, Model: "m", Summary: "It works.", KeyPoints: []string{"k"},
		ActionItems: []model.SummaryItem{{Who: "Bo", What: "ship"}}, Chapters: []model.SummaryChapter{{TMS: 5, Title: "c", Segment: 1, OffsetMS: 5}},
		Transcripts: 2, Frames: 3, Parts: 1, Note: "n", Requested: true, CreatedAt: 1, StartedAt: 2, FinishedAt: 3, UpdatedAt: 3}
	if err := s.PutSummary(x); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSummary("r1")
	if err != nil || got.Summary != "It works." || got.KeyPoints[0] != "k" || got.Decisions == nil || got.ActionItems[0].Who != "Bo" ||
		got.Chapters[0].OffsetMS != 5 || !got.Requested || got.Frames != 3 || got.FinishedAt != 3 {
		t.Fatalf("got %+v %v", got, err)
	}
	x.State, x.UpdatedAt = model.SummaryQueued, 9
	s.PutSummary(x)
	s.PutSummary(model.RecordingSummary{RecordingID: "gone", State: model.SummaryTranscribing, CreatedAt: 1, UpdatedAt: 2})
	if list, _ := s.SummariesIn(model.SummaryQueued, model.SummaryTranscribing); len(list) != 2 || list[0].RecordingID != "gone" {
		t.Fatalf("in: %+v", list)
	}
	b, _ := s.SummaryBriefs([]string{"r1", "gone", "other"})
	if len(b) != 2 || b["r1"].Summary != "It works." || b["r1"].State != model.SummaryQueued {
		t.Fatalf("briefs: %+v", b)
	}
	if n, err := s.DeleteOrphanSummaries(); err != nil || n != 1 {
		t.Fatalf("orphans: %d %v", n, err)
	}
	if err := s.DeleteSummary("r1"); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.SummaryBriefs([]string{"r1"}); len(b) != 0 {
		t.Fatalf("deleted: %+v", b)
	}
}

package store

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func mkProgressPatch(done, total, version int, revision string) model.ProgressPatch {
	steps := make([]model.ProgressStep, total)
	for i := 0; i < total; i++ {
		steps[i] = model.ProgressStep{ID: fmt.Sprintf("s%d", i), Label: "Step", Done: i < done}
	}
	percent := int(math.Floor(100*float64(done)/float64(total) + .5))
	return model.ProgressPatch{ExpectedVersion: version, Percent: percent, HasPercent: true,
		Basis: model.ProgressBasis{Revision: revision, Steps: steps}, HasBasis: true}
}

// GetProject must read back exactly what SaveProjectProgress wrote, and — the point of
// PutProject excluding the progress columns from its own write — a later generic project save
// (a rename, say) must never clobber that progress, even though the caller's in-memory
// model.Project for the rename still holds the zero value for every progress field.
func TestProjectProgressRoundTripSurvivesUnrelatedSave(t *testing.T) {
	s := openTest(t)
	if err := s.PutProject(model.Project{ID: "p1", Slug: "alpha", Status: model.ProjectActive}); err != nil {
		t.Fatal(err)
	}
	step, err := model.UpdateProgress(model.Project{ID: "p1"}, mkProgressPatch(1, 2, 0, "v1"), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(step); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject("p1")
	if err != nil || got.Progress == nil || *got.Progress != *step.Progress ||
		!reflect.DeepEqual(got.ProgressBasis, step.ProgressBasis) || got.ProgressState != step.ProgressState ||
		got.ProgressVersion != step.ProgressVersion || got.ProgressUpdatedAt != step.ProgressUpdatedAt || got.ProgressRun != step.ProgressRun {
		t.Fatalf("got %+v, want progress of %+v (%v)", got, step, err)
	}

	if err := s.PutProject(model.Project{ID: "p1", Name: "Alpha Renamed", Slug: "alpha", Status: model.ProjectActive}); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetProject("p1")
	if err != nil || after.Name != "Alpha Renamed" || after.Progress == nil || *after.Progress != *step.Progress {
		t.Fatalf("unrelated save clobbered progress: %+v (%v)", after, err)
	}
}

// SaveProjectProgress must commit the latest checklist and a history sample together, reject a
// write computed from an already-superseded version (someone else wrote first), accept a
// no-op write without adding history, and drop all history when progress is cleared.
func TestSaveProjectProgress_CommitConflictAndClear(t *testing.T) {
	s := openTest(t)
	// SaveProjectProgress's 30-day retention prune (like the Python reference's) runs off the
	// real wall clock, independent of whatever "now" a caller passes to UpdateProgress — so
	// test timestamps must be realistic (not near-epoch), or every sample is immediately
	// "30 days old" and pruned on the next write.
	now0 := time.Now().UnixMilli()
	base := model.Project{ID: "p1", Slug: "alpha", Status: model.ProjectActive}
	if err := s.PutProject(base); err != nil {
		t.Fatal(err)
	}

	step1, err := model.UpdateProgress(base, mkProgressPatch(1, 4, 0, "v1"), now0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(step1); err != nil {
		t.Fatal(err)
	}
	if samples, err := s.ProjectProgressSamples("p1"); err != nil || len(samples) != 1 || samples[0].Percent != 25 {
		t.Fatalf("samples %+v %v", samples, err)
	}

	// A second writer that read `base` (before step1 committed) and computed its own update
	// from it: same resulting version number as step1, but different content, so it must
	// still be rejected rather than silently accepted because "the version matches".
	stale, err := model.UpdateProgress(base, mkProgressPatch(2, 4, base.ProgressVersion, "v1"), now0+100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(stale); !errors.Is(err, model.ErrProgressConflict) {
		t.Fatalf("want ErrProgressConflict, got %v", err)
	}
	if got, _ := s.GetProject("p1"); got.ProgressVersion != step1.ProgressVersion || *got.Progress != 25 {
		t.Fatalf("rejected write leaked into storage: %+v", got)
	}

	// Re-submitting exactly what's stored (the no-op result of UpdateProgress) must succeed
	// without adding history.
	if err := s.SaveProjectProgress(step1); err != nil {
		t.Fatal(err)
	}
	if samples, _ := s.ProjectProgressSamples("p1"); len(samples) != 1 {
		t.Fatalf("no-op wrote a sample: %+v", samples)
	}

	step2, err := model.UpdateProgress(step1, mkProgressPatch(2, 4, step1.ProgressVersion, "v1"), now0+200)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(step2); err != nil {
		t.Fatal(err)
	}
	if samples, _ := s.ProjectProgressSamples("p1"); len(samples) != 2 {
		t.Fatalf("want 2 samples, got %+v", samples)
	}

	cleared, err := model.UpdateProgress(step2, model.ProgressPatch{ExpectedVersion: step2.ProgressVersion, Clear: true}, now0+300)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(cleared); err != nil {
		t.Fatal(err)
	}
	if samples, _ := s.ProjectProgressSamples("p1"); len(samples) != 0 {
		t.Fatalf("clear left history behind: %+v", samples)
	}
	if got, _ := s.GetProject("p1"); got.Progress != nil {
		t.Fatalf("clear left a percent behind: %+v", got)
	}
}

// Retention is bounded to progressHistoryKeep samples per project, regardless of how many
// updates have landed.
func TestSaveProjectProgress_HistoryBoundedPerProject(t *testing.T) {
	s := openTest(t)
	if err := s.PutProject(model.Project{ID: "p1", Slug: "alpha"}); err != nil {
		t.Fatal(err)
	}
	cur := model.Project{ID: "p1"}
	now0 := time.Now().UnixMilli()
	for i := 0; i < progressHistoryKeep+5; i++ {
		var err error
		cur, err = model.UpdateProgress(cur, mkProgressPatch(i%4, 4, i, fmt.Sprintf("r%d", i)), now0+int64(i))
		if err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
		if err := s.SaveProjectProgress(cur); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	samples, err := s.ProjectProgressSamples("p1")
	if err != nil || len(samples) != progressHistoryKeep {
		t.Fatalf("want %d samples, got %d (%v)", progressHistoryKeep, len(samples), err)
	}
}

// PruneProgress removes history past progressHistoryMaxAge even without a new update ever
// landing, same as the Python reference's prune_progress.
func TestPruneProgress(t *testing.T) {
	s := openTest(t)
	if err := s.PutProject(model.Project{ID: "p1", Slug: "alpha"}); err != nil {
		t.Fatal(err)
	}
	cur, err := model.UpdateProgress(model.Project{ID: "p1"}, mkProgressPatch(1, 4, 0, "v1"), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(cur); err != nil {
		t.Fatal(err)
	}
	if samples, _ := s.ProjectProgressSamples("p1"); len(samples) != 1 {
		t.Fatalf("setup: want 1 fresh sample, got %+v", samples)
	}
	old := time.Now().Add(-progressHistoryMaxAge - time.Hour).UnixMilli()
	if _, err := s.db.Exec(`UPDATE progress_samples SET at=? WHERE project_id=?`, old, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneProgress(); err != nil {
		t.Fatal(err)
	}
	if samples, _ := s.ProjectProgressSamples("p1"); len(samples) != 0 {
		t.Fatalf("not pruned: %+v", samples)
	}
}

func TestSaveProjectProgress_UnknownProject(t *testing.T) {
	s := openTest(t)
	p, err := model.UpdateProgress(model.Project{ID: "missing"}, mkProgressPatch(1, 4, 0, "v1"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProjectProgress(p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

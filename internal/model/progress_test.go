package model

import (
	"errors"
	"math"
	"testing"
)

// checklist builds a basis of total steps with the first done of them marked done, and the
// percent that basis implies (the rounded completed fraction) — mirrors the Python reference
// test suite's patch() helper.
func checklist(done, total int, revision string) (int, ProgressBasis) {
	steps := make([]ProgressStep, total)
	for i := 0; i < total; i++ {
		steps[i] = ProgressStep{ID: string(rune('a' + i)), Label: "Step", Done: i < done}
	}
	percent := int(math.Floor(100*float64(done)/float64(total) + .5))
	return percent, ProgressBasis{Revision: revision, Steps: steps}
}

func patchAt(done, total, version int, revision string) ProgressPatch {
	percent, basis := checklist(done, total, revision)
	return ProgressPatch{ExpectedVersion: version, Percent: percent, HasPercent: true, Basis: basis, HasBasis: true}
}

func TestUpdateProgress_PassThroughWhenPatchTouchesNothing(t *testing.T) {
	cur := Project{ProgressVersion: 5, ProgressState: ProgressBlocked}
	out, err := UpdateProgress(cur, ProgressPatch{}, 1000)
	if err != nil || out.ProgressVersion != 5 || out.ProgressState != ProgressBlocked {
		t.Fatalf("got %+v %v", out, err)
	}
}

func TestUpdateProgress_ConflictOnStaleExpectedVersion(t *testing.T) {
	cur := Project{ProgressVersion: 2}
	_, err := UpdateProgress(cur, patchAt(1, 4, 1, "v1"), 1000)
	if !errors.Is(err, ErrProgressConflict) {
		t.Fatalf("want ErrProgressConflict, got %v", err)
	}
}

func TestUpdateProgress_RejectsOutOfRangeOrMismatchedPercent(t *testing.T) {
	_, basis := checklist(2, 4, "v1")
	for name, patch := range map[string]ProgressPatch{
		"percent>100": {Percent: 101, HasPercent: true, Basis: basis, HasBasis: true},
		"percent<0":   {Percent: -1, HasPercent: true, Basis: basis, HasBasis: true},
		"mismatched":  {Percent: 49, HasPercent: true, Basis: basis, HasBasis: true}, // 2/4 rounds to 50, not 49
		"empty steps": {Percent: 0, HasPercent: true, Basis: ProgressBasis{Revision: "v1"}, HasBasis: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := UpdateProgress(Project{}, patch, 1000); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestUpdateProgress_RejectsUnicodeFormatCharactersInLabels(t *testing.T) {
	basis := ProgressBasis{Revision: "v1", Steps: []ProgressStep{
		{ID: "a", Label: "Build‍it", Done: true}, // U+200D ZERO WIDTH JOINER (category Cf)
	}}
	patch := ProgressPatch{Percent: 100, HasPercent: true, Basis: basis, HasBasis: true}
	if _, err := UpdateProgress(Project{}, patch, 1000); err == nil {
		t.Fatal("want error: unicode format control character in label")
	}
}

func TestUpdateProgress_CompleteRequiresExactly100(t *testing.T) {
	percent, basis := checklist(2, 4, "v1")
	patch := ProgressPatch{Percent: percent, HasPercent: true, Basis: basis, HasBasis: true,
		State: ProgressComplete, HasState: true}
	if _, err := UpdateProgress(Project{}, patch, 1000); err == nil {
		t.Fatal("want error: complete at 50%")
	}
}

func TestUpdateProgress_ReopeningBelow100DowngradesStaleComplete(t *testing.T) {
	cur, err := UpdateProgress(Project{}, patchAt(4, 4, 0, "v1"), 1000) // 100%, state defaults to complete
	if err != nil || cur.ProgressState != ProgressComplete {
		t.Fatalf("setup: %+v %v", cur, err)
	}
	reopened, err := UpdateProgress(cur, patchAt(2, 4, 1, "v1"), 1100) // back to 50%, state not set explicitly
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ProgressState != ProgressActive {
		t.Fatalf("want active, got %v", reopened.ProgressState)
	}
}

func TestUpdateProgress_SameRevisionDifferentShapeRejected(t *testing.T) {
	cur, err := UpdateProgress(Project{}, patchAt(2, 4, 0, "v1"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateProgress(cur, patchAt(2, 5, 1, "v1"), 1100); err == nil {
		t.Fatal("want error: same revision, different step count")
	}
	// A genuinely new revision is fine even though the shape changed.
	if _, err := UpdateProgress(cur, patchAt(2, 5, 1, "v2"), 1100); err != nil {
		t.Fatalf("new revision should be accepted: %v", err)
	}
}

func TestUpdateProgress_NoChangeIsNoOpUnlessConfirmed(t *testing.T) {
	cur, err := UpdateProgress(Project{}, patchAt(2, 4, 0, "v1"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	same := patchAt(2, 4, 1, "v1")
	out, err := UpdateProgress(cur, same, 1600)
	if err != nil {
		t.Fatal(err)
	}
	if out.ProgressVersion != cur.ProgressVersion || out.ProgressUpdatedAt != cur.ProgressUpdatedAt {
		t.Fatalf("unconfirmed repeat must be a no-op, got %+v", out)
	}
	same.Confirm, same.HasConfirm = true, true
	confirmed, err := UpdateProgress(cur, same, 1600)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.ProgressVersion != cur.ProgressVersion+1 || confirmed.ProgressUpdatedAt != 1600 {
		t.Fatalf("confirm must refresh version and timestamp, got %+v", confirmed)
	}
}

func TestUpdateProgress_RunResetsOnRegressionNewRevisionStateChangeOrClock(t *testing.T) {
	a, err := UpdateProgress(Project{}, patchAt(1, 4, 0, "v1"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := UpdateProgress(a, patchAt(1, 5, 1, "v2"), 1100) // new revision
	if err != nil {
		t.Fatal(err)
	}
	if b.ProgressRun == a.ProgressRun {
		t.Fatal("new revision must reset the run")
	}
	c, err := UpdateProgress(b, patchAt(0, 5, 2, "v2"), 1200) // percent regresses (20% -> 0%)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProgressRun == b.ProgressRun {
		t.Fatal("percent regression must reset the run")
	}
	blocked, err := UpdateProgress(c, ProgressPatch{ExpectedVersion: c.ProgressVersion, State: ProgressBlocked, HasState: true,
		Percent: 0, HasPercent: true, Basis: c.ProgressBasis, HasBasis: true}, 1300)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.ProgressRun == c.ProgressRun {
		t.Fatal("a state change must reset the run")
	}
	backwards, err := UpdateProgress(blocked, ProgressPatch{ExpectedVersion: blocked.ProgressVersion, State: ProgressBlocked, HasState: true,
		Confirm: true, HasConfirm: true}, 1250) // clock before the last update
	if err != nil {
		t.Fatal(err)
	}
	if backwards.ProgressRun == blocked.ProgressRun {
		t.Fatal("a clock regression must reset the run")
	}
}

func TestUpdateProgress_Clear(t *testing.T) {
	cur, err := UpdateProgress(Project{}, patchAt(2, 4, 0, "v1"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := UpdateProgress(cur, ProgressPatch{ExpectedVersion: cur.ProgressVersion, Clear: true}, 1100)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Progress != nil || cleared.ProgressState != "" || cleared.ProgressRun != "" ||
		cleared.ProgressVersion != cur.ProgressVersion+1 || cleared.ProgressUpdatedAt != 1100 {
		t.Fatalf("clear left stale fields: %+v", cleared)
	}
}

func TestViewProgress_UnreportedWhenNeverSet(t *testing.T) {
	v := ViewProgress(Project{}, nil, 1000, 0)
	if v.State != "unreported" || v.ETAReason != "not-reported" || v.Percent != nil {
		t.Fatalf("%+v", v)
	}
}

func TestViewProgress_StaleAndClockChanged(t *testing.T) {
	cur, err := UpdateProgress(Project{}, patchAt(1, 4, 0, "v1"), 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if v := ViewProgress(cur, nil, 1_000_000+5*3600*1000, 4); v.ETAReason != "stale" || !v.Stale {
		t.Fatalf("want stale, got %+v", v)
	}
	if v := ViewProgress(cur, nil, 1_000_000-1000, 4); v.ETAReason != "clock-changed" {
		t.Fatalf("want clock-changed, got %+v", v)
	}
}

func TestViewProgress_NonActiveStateIsItsOwnReason(t *testing.T) {
	percent, basis := checklist(4, 4, "v1") // 100%, defaults to complete
	complete, err := UpdateProgress(Project{}, ProgressPatch{Percent: percent, HasPercent: true, Basis: basis, HasBasis: true}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if v := ViewProgress(complete, nil, 100000, 4); v.ETAReason != "complete" || v.Stale {
		t.Fatalf("complete must never go stale: %+v", v)
	}
	blocked, err := UpdateProgress(Project{}, ProgressPatch{Percent: 25, HasPercent: true,
		Basis:    ProgressBasis{Revision: "v1", Steps: []ProgressStep{{ID: "a", Label: "x", Done: true}, {ID: "b", Label: "y"}, {ID: "c", Label: "z"}, {ID: "d", Label: "w"}}},
		HasBasis: true, State: ProgressBlocked, HasState: true}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if v := ViewProgress(blocked, nil, 1000, 4); v.ETAReason != "blocked" {
		t.Fatalf("want blocked, got %+v", v)
	}
}

func TestViewProgress_ETARequiresThreeAdvancesSpanningTenMinutes(t *testing.T) {
	var cur Project
	var samples []ProgressSample
	var last ProgressView
	for i, at := range []int64{1_000_000, 1_300_000, 1_600_000} {
		var err error
		cur, err = UpdateProgress(cur, patchAt(i, 4, i, "v1"), at)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, ProgressSample{Version: i + 1, At: at, Run: cur.ProgressRun, Percent: *cur.Progress})
		last = ViewProgress(cur, samples, at, 4)
		if i < 2 && last.ETAReason != "insufficient-history" {
			t.Fatalf("step %d: want insufficient-history, got %+v", i, last)
		}
	}
	if last.ETAReason != "recent-pace" || last.ETASeconds == nil {
		t.Fatalf("want recent-pace with an ETA, got %+v", last)
	}
}

func TestViewProgress_UnevenRateHasNoETA(t *testing.T) {
	// Timestamps are Unix milliseconds throughout; a 10s then a 990s gap keeps the span check
	// happy (>=10 minutes total) while making the two rates wildly different.
	cur, err := UpdateProgress(Project{}, patchAt(2, 4, 0, "v1"), 2_000_000)
	if err != nil {
		t.Fatal(err)
	}
	samples := []ProgressSample{
		{Version: 0, At: 1_000_000, Run: cur.ProgressRun, Percent: 0},
		{Version: 1, At: 1_010_000, Run: cur.ProgressRun, Percent: 25},
		{Version: 2, At: 2_000_000, Run: cur.ProgressRun, Percent: 50},
	}
	if v := ViewProgress(cur, samples, 2_000_000, 4); v.ETAReason != "uneven-rate" || v.ETASeconds != nil {
		t.Fatalf("%+v", v)
	}
}

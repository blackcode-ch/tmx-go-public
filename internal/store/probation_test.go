package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

// probationSetup: an old binary that booted clean, then an updater install of "new binary" on
// probation (Prepare + MarkUpdate + BeginProbation + swap), as update.install does it.
func probationSetup(t *testing.T) (path, binary, checkpoint string, t0 time.Time) {
	t.Helper()
	dir := t.TempDir()
	path, binary, checkpoint = filepath.Join(dir, "tmx.db"), filepath.Join(dir, "tmx.exe"), filepath.Join(dir, "rollback")
	if err := os.WriteFile(binary, []byte("old binary"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	if err := rollback.Prepare(context.Background(), checkpoint, binary, path); err != nil {
		t.Fatal(err)
	}
	if err := rollback.MarkUpdate(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := rollback.BeginProbation(checkpoint, "fleet-v1.0.0", "fleet-v1.1.0", t0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("new binary"), 0700); err != nil {
		t.Fatal(err)
	}
	saved, savedV := probationNow, probationVersion
	t.Cleanup(func() { probationNow, probationVersion = saved, savedV })
	probationNow = func() time.Time { return t0.Add(time.Minute) }
	probationVersion = func() string { return "fleet-v1.1.0" } // the binary on probation
	return
}

func open(t *testing.T, path, binary, checkpoint string) error {
	t.Helper()
	s, err := OpenRecoverable(path, binary, checkpoint)
	if err == nil {
		s.Close()
	}
	return err
}

func binaryIs(t *testing.T, binary, want string) {
	t.Helper()
	if b, _ := os.ReadFile(binary); string(b) != want {
		t.Fatalf("binary %q, want %q", b, want)
	}
}

func TestProbationConfirmedKeepsTheNewBinary(t *testing.T) {
	path, binary, checkpoint, t0 := probationSetup(t)
	before, _ := os.ReadFile(filepath.Join(checkpoint, "current"))
	for i := 0; i < 2; i++ { // two starts on probation: no refresh of the pair to go back to
		if err := open(t, path, binary, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(checkpoint, "current")); string(after) != string(before) {
		t.Fatal("the retained pair was refreshed while the new binary was on probation")
	}
	if err := rollback.ConfirmProbation(checkpoint, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, on, _ := rollback.OnProbation(checkpoint); on {
		t.Fatal("still on probation after confirming")
	}
	if r, ok := rollback.LastProbation(checkpoint); !ok || r.Outcome != "confirmed" || r.To != "fleet-v1.1.0" {
		t.Fatalf("result %+v", r)
	}
	// Later starts, even past the old deadline and the start limit, never roll back.
	probationNow = func() time.Time { return t0.Add(time.Hour) }
	for i := 0; i < rollback.MaxProbationStarts+1; i++ {
		if err := open(t, path, binary, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	binaryIs(t, binary, "new binary")
}

func TestProbationDeadlineRestores(t *testing.T) {
	path, binary, checkpoint, t0 := probationSetup(t)
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatal(err)
	}
	probationNow = func() time.Time { return t0.Add(rollback.ProbationWindow + time.Second) }
	if err := open(t, path, binary, checkpoint); !errors.Is(err, rollback.ErrRolledBack) {
		t.Fatalf("past the deadline: %v", err)
	}
	binaryIs(t, binary, "old binary")
	if r, ok := rollback.LastProbation(checkpoint); !ok || r.Outcome != "rolled back" || !strings.Contains(r.Reason, "not confirmed healthy") {
		t.Fatalf("result %+v", r)
	}
	// The restored binary starts normally and never re-enters probation.
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, on, _ := rollback.OnProbation(checkpoint); on {
		t.Fatal("the restored binary is on probation")
	}
}

func TestProbationCrashLoopRestores(t *testing.T) {
	path, binary, checkpoint, _ := probationSetup(t)
	for i := 1; i < rollback.MaxProbationStarts; i++ {
		if err := open(t, path, binary, checkpoint); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		binaryIs(t, binary, "new binary")
	}
	if err := open(t, path, binary, checkpoint); !errors.Is(err, rollback.ErrRolledBack) {
		t.Fatalf("start %d: %v", rollback.MaxProbationStarts, err)
	}
	binaryIs(t, binary, "old binary")
}

func TestNoProbationNoRollback(t *testing.T) {
	dir := t.TempDir()
	path, binary, checkpoint := filepath.Join(dir, "tmx.db"), filepath.Join(dir, "tmx.exe"), filepath.Join(dir, "rollback")
	os.WriteFile(binary, []byte("manual binary"), 0700)
	saved := probationNow
	defer func() { probationNow = saved }()
	probationNow = func() time.Time { return time.Now().Add(24 * time.Hour) }
	for i := 0; i < rollback.MaxProbationStarts+1; i++ {
		if err := open(t, path, binary, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	binaryIs(t, binary, "manual binary")
	if _, ok := rollback.LastProbation(checkpoint); ok {
		t.Fatal("a probation result without an update")
	}
}

func TestProbationRestoreFailureIsReportedOnce(t *testing.T) {
	path, binary, checkpoint, t0 := probationSetup(t)
	pairs, _ := filepath.Glob(filepath.Join(checkpoint, "pair-*", "binary"))
	if len(pairs) != 1 {
		t.Fatalf("pairs %v", pairs)
	}
	if err := os.WriteFile(pairs[0], []byte("damaged"), 0700); err != nil { // checksum mismatch
		t.Fatal(err)
	}
	probationNow = func() time.Time { return t0.Add(rollback.ProbationWindow + time.Second) }
	// The restore refuses before changing anything: the daemon still starts (lockout rule), on
	// the binary it has, and the result says why.
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatalf("a failed restore kept the daemon from starting: %v", err)
	}
	binaryIs(t, binary, "new binary")
	if r, ok := rollback.LastProbation(checkpoint); !ok || r.Outcome != "rollback failed" {
		t.Fatalf("result %+v", r)
	}
	// Not looped: the probation is over; the next start runs (and reports) like any other.
	if _, on, _ := rollback.OnProbation(checkpoint); on {
		t.Fatal("still on probation after a failed restore")
	}
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatalf("the start after a failed restore: %v", err)
	}
	binaryIs(t, binary, "new binary")
}

// The running daemon marked its update failed (the deadline, or a drill) and restarted: that
// start restores at once, before the deadline and the start limit.
func TestProbationMarkedFailedRestoresAtTheNextStart(t *testing.T) {
	path, binary, checkpoint, _ := probationSetup(t)
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := rollback.MarkFailed(checkpoint, "rollback drill armed by the owner"); err != nil {
		t.Fatal(err)
	}
	if err := open(t, path, binary, checkpoint); !errors.Is(err, rollback.ErrRolledBack) {
		t.Fatalf("after MarkFailed: %v", err)
	}
	binaryIs(t, binary, "old binary")
	if r, _ := rollback.LastProbation(checkpoint); r.Reason != "rollback drill armed by the owner" {
		t.Fatalf("result %+v", r)
	}
}

// An armed drill belongs to the next install only.
func TestDrillIsOneShot(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := rollback.ArmDrill(dir); err != nil {
		t.Fatal(err)
	}
	if err := rollback.BeginProbation(dir, "a", "b", now); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := rollback.OnProbation(dir); !p.Drill || rollback.DrillArmed(dir) {
		t.Fatalf("drill not moved into the probation: %+v armed=%v", p, rollback.DrillArmed(dir))
	}
	rollback.EndProbation(dir)
	if err := rollback.BeginProbation(dir, "b", "c", now); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := rollback.OnProbation(dir); p.Drill {
		t.Fatal("a drill carried over to a second install")
	}
}

// An unreadable probation record never keeps the daemon from starting and never restores: its
// To can't be proven to be this install, and a database restore would lose data. It is set aside.
func TestUnreadableProbationRecordIsSetAside(t *testing.T) {
	path, binary, checkpoint, _ := probationSetup(t)
	rec := filepath.Join(checkpoint, "probation.json")
	if err := os.WriteFile(rec, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatalf("an unreadable record kept the daemon from starting: %v", err)
	}
	binaryIs(t, binary, "new binary")
	if r, ok := rollback.LastProbation(checkpoint); !ok || r.Outcome != "rollback skipped" || !strings.Contains(r.Reason, "unreadable probation record") {
		t.Fatalf("result %+v", r)
	}
	if _, err := os.Stat(rec); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the unreadable record is still in place")
	}
	if bad, _ := filepath.Glob(rec + ".bad-*"); len(bad) != 1 {
		t.Fatalf("quarantined %v", bad)
	}
}

// The owner replaced a failing update by hand: the record names another version, so this start
// supersedes it and never restores the retained pair (binary or database) over the manual install.
func TestManualInstallSupersedesTheProbation(t *testing.T) {
	path, binary, checkpoint, t0 := probationSetup(t)
	if err := os.WriteFile(binary, []byte("manual fix"), 0700); err != nil {
		t.Fatal(err)
	}
	probationVersion = func() string { return "fleet-v1.1.1-manual" }
	probationNow = func() time.Time { return t0.Add(rollback.ProbationWindow + time.Hour) } // past the deadline
	for i := 0; i < rollback.MaxProbationStarts+1; i++ {
		if err := open(t, path, binary, checkpoint); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	binaryIs(t, binary, "manual fix")
	if r, ok := rollback.LastProbation(checkpoint); !ok || r.Outcome != "superseded" {
		t.Fatalf("result %+v", r)
	}
	if _, on, _ := rollback.OnProbation(checkpoint); on {
		t.Fatal("still on probation")
	}
}

// A restore succeeded but the record could not be removed (an AV or a file lock): the restored
// binary's start sees a record for another version and supersedes it; it does not restore again.
func TestRestoredBinaryDoesNotRestoreAgain(t *testing.T) {
	path, binary, checkpoint, t0 := probationSetup(t)
	probationNow = func() time.Time { return t0.Add(rollback.ProbationWindow + time.Second) }
	if err := open(t, path, binary, checkpoint); !errors.Is(err, rollback.ErrRolledBack) {
		t.Fatalf("past the deadline: %v", err)
	}
	binaryIs(t, binary, "old binary")
	// As if EndProbation had failed: the record is back.
	if err := rollback.BeginProbation(checkpoint, "fleet-v1.0.0", "fleet-v1.1.0", t0); err != nil {
		t.Fatal(err)
	}
	probationVersion = func() string { return "fleet-v1.0.0" } // the restored binary
	if err := open(t, path, binary, checkpoint); err != nil {
		t.Fatalf("the restored binary's start: %v", err)
	}
	binaryIs(t, binary, "old binary")
	if r, _ := rollback.LastProbation(checkpoint); r.Outcome != "superseded" {
		t.Fatalf("result %+v", r)
	}
}

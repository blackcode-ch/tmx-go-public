package rollback

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Probation (DESIGN.md "Updates", post-swap watchdog): an install by the updater puts the new
// binary on probation. It must confirm itself healthy (serving its API, governor not degraded,
// database migrated) within ProbationWindow, else the retained pair is restored. Each start
// while on probation counts; the MaxProbationStarts-th start, or any start after the deadline,
// restores instead of running the new binary. A restore ends the probation, so the restored
// binary never re-enters it. A manual install has no probation record and is never rolled back.

// ProbationWindow is how long a new binary has to confirm itself after the swap.
const ProbationWindow = 10 * time.Minute

// MaxProbationStarts: the start that reaches it restores (two starts died before confirming).
const MaxProbationStarts = 3

// Probation is the record in the checkpoint folder while a new binary is on probation.
type Probation struct {
	From     string    `json:"from"`     // the version installed before
	To       string    `json:"to"`       // the version on probation
	Deadline time.Time `json:"deadline"` // confirm by then
	Starts   int       `json:"starts"`   // starts of the new binary so far
	// Failed: the running daemon judged the update failed and restarts; that start restores.
	Failed string `json:"failed,omitempty"`
	// Drill: the owner armed a rollback drill for this install; the watchdog fails it on purpose
	// once the new binary is healthy (the runbook's forced-rollback drill).
	Drill bool `json:"drill,omitempty"`
}

// ProbationResult is the last probation's outcome, kept for status (never deleted by a start).
type ProbationResult struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Outcome string    `json:"outcome"` // "confirmed", "rolled back", "rollback failed"
	Reason  string    `json:"reason,omitempty"`
	At      time.Time `json:"at"`
}

const (
	probationFile = "probation.json"
	resultFile    = "probation-result.json"
	drillFile     = "drill-armed"
)

// BeginProbation records that the updater is about to swap in version to (from from).
func BeginProbation(dir, from, to string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// An armed drill is one-shot: it belongs to this install and no other.
	drill := DrillArmed(dir)
	if err := writeJSON(filepath.Join(dir, probationFile), Probation{From: from, To: to, Deadline: now.Add(ProbationWindow), Drill: drill}); err != nil {
		return err
	}
	if drill {
		return DisarmDrill(dir)
	}
	return nil
}

// ArmDrill makes the next updater install fail its probation on purpose, once (owner-gated in
// the API). The restore is the real one: it brings back the previous binary and database.
func ArmDrill(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeSync(filepath.Join(dir, drillFile), []byte("fail the next update's probation\n"))
}

// DisarmDrill takes an armed drill back.
func DisarmDrill(dir string) error {
	err := os.Remove(filepath.Join(dir, drillFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// DrillArmed reports an armed drill.
func DrillArmed(dir string) bool { _, err := os.Stat(filepath.Join(dir, drillFile)); return err == nil }

// MarkFailed records that the running daemon judged its update failed (it restarts next; the
// start restores).
func MarkFailed(dir, reason string) error {
	p, ok, err := OnProbation(dir)
	if err != nil || !ok {
		return err
	}
	p.Failed = reason
	return writeJSON(filepath.Join(dir, probationFile), p)
}

// EndProbation drops the record without a result (the swap itself failed: nothing to judge).
func EndProbation(dir string) error {
	err := os.Remove(filepath.Join(dir, probationFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// OnProbation returns the open record, if there is one.
func OnProbation(dir string) (Probation, bool, error) {
	var p Probation
	b, err := os.ReadFile(filepath.Join(dir, probationFile))
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, false, fmt.Errorf("probation record: %w", err)
	}
	return p, true, nil
}

// LastProbation returns the last probation's outcome, if any.
func LastProbation(dir string) (ProbationResult, bool) {
	var r ProbationResult
	b, err := os.ReadFile(filepath.Join(dir, resultFile))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return ProbationResult{}, false
	}
	return r, true
}

// ErrRolledBack: a start found the new binary failed its probation and restored the previous
// binary and database; the caller starts the (restored) binary again.
var ErrRolledBack = errors.New("the update failed its health probation; previous binary and database restored")

// StartProbation runs at each daemon start before the store opens (the caller holds the daemon
// lock, no database users). Without a record it does nothing. A record is judged only by the
// binary it was written for (version == To): any other binary (the owner installed one by hand,
// or this is the restored one) records "superseded", drops the record and starts normally, so a
// manual install is never rolled back and a restore never repeats. The binary on probation counts
// the start and, at the start limit, past the deadline or when marked failed, restores the
// retained pair and returns ErrRolledBack. A restore that stopped half way leaves its own marker
// (Pending) for the next start to finish. Nothing here stops the daemon starting otherwise (the
// lockout rule): an unreadable record is set aside, unjudged (its To can't be proven).
func StartProbation(dir, binary, dbPath, version string, now time.Time) error {
	p, ok, err := OnProbation(dir)
	if err != nil {
		res := ProbationResult{Outcome: "rollback skipped", Reason: "unreadable probation record: " + err.Error(), At: now}
		bad := filepath.Join(dir, probationFile)
		if rerr := os.Rename(bad, bad+".bad-"+now.UTC().Format("20060102T150405Z")); rerr != nil {
			os.Remove(bad)
		}
		writeJSON(filepath.Join(dir, resultFile), res)
		return nil
	}
	if !ok {
		return nil
	}
	if p.To != version {
		writeJSON(filepath.Join(dir, resultFile), ProbationResult{From: p.From, To: p.To, Outcome: "superseded", Reason: "started by " + version + ", not the binary on probation", At: now})
		EndProbation(dir)
		return nil
	}
	p.Starts++
	var reason string
	switch {
	case p.Failed != "":
		reason = p.Failed
	case now.After(p.Deadline):
		reason = fmt.Sprintf("not confirmed healthy by %s", p.Deadline.Format(time.RFC3339))
	case p.Starts >= MaxProbationStarts:
		reason = fmt.Sprintf("start %d of the new binary without confirming healthy", p.Starts)
	default:
		return writeJSON(filepath.Join(dir, probationFile), p)
	}
	return rollbackProbation(dir, binary, dbPath, p, reason, now)
}

func rollbackProbation(dir, binary, dbPath string, p Probation, reason string, now time.Time) error {
	res := ProbationResult{From: p.From, To: p.To, Outcome: "rolled back", Reason: reason, At: now}
	// The updater always retains a proven binary; a pair holding only a database would bring back
	// the old data under the binary that just failed, so it is left alone.
	rerr := errors.New("the retained pair has no proven previous binary")
	if unproven, err := Unproven(dir, binary, dbPath); err != nil {
		rerr = err
	} else if !unproven {
		rerr = Restore(dir, binary, dbPath)
	}
	if rerr != nil {
		res.Outcome, res.Reason = "rollback failed", reason+": "+rerr.Error()
	}
	werr := writeJSON(filepath.Join(dir, resultFile), res)
	eerr := EndProbation(dir)
	if rerr != nil {
		if Pending(dir) {
			// A restore stopped half way: its marker makes the next start finish it (OpenRecoverable).
			return errors.Join(fmt.Errorf("update %s failed its probation (%s) and the rollback stopped half way: %w", p.To, reason, rerr), werr, eerr)
		}
		// Nothing on disk changed: start the current binary rather than lock the owner out. The
		// outcome stays in the result file (update status shows it).
		return nil
	}
	// Restored: report it even if the record could not be removed; the restored binary's version
	// is not p.To, so its start supersedes the record instead of restoring again.
	_ = eerr
	return errors.Join(fmt.Errorf("%w: %s (%s)", ErrRolledBack, p.To, reason), werr)
}

// ConfirmProbation ends the probation as healthy.
func ConfirmProbation(dir string, now time.Time) error {
	p, ok, err := OnProbation(dir)
	if err != nil || !ok {
		return err
	}
	if err := writeJSON(filepath.Join(dir, resultFile), ProbationResult{From: p.From, To: p.To, Outcome: "confirmed", At: now}); err != nil {
		return err
	}
	return EndProbation(dir)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeSync(tmp, append(b, '\n')); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

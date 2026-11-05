package fleet

import (
	"fmt"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Recovery is bounded independently of the OS process-tree/resume watchdog.
// Attempts are retained for an hour; a deliberate start resets the budget.
type crashRecovery struct {
	attempts   []time.Time
	generation uint64
	pending    bool
	cancelWait chan struct{}
}

func (w *crashRecovery) next(now time.Time) (time.Duration, string) {
	kept := w.attempts[:0]
	recent := 0
	for _, at := range w.attempts {
		if now.Sub(at) < time.Hour {
			kept = append(kept, at)
			if now.Sub(at) < 15*time.Minute {
				recent++
			}
		}
	}
	w.attempts = kept
	if recent >= 3 {
		return 0, "crash loop: 3 automatic restarts within 15 minutes"
	}
	if len(kept) >= 3 {
		return 0, "automatic restart limit: 3 attempts within an hour"
	}
	return []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}[len(kept)], ""
}

func (w *crashRecovery) cancel(reset bool) {
	if w.cancelWait != nil {
		close(w.cancelWait)
		w.cancelWait = nil
	}
	w.generation++
	w.pending = false
	if reset {
		w.attempts = nil
	}
}

// scheduleRecoveryLocked is called only for an unexpected exit of a managed
// process. The generation fences delayed work from stop/start/remove races.
func (f *Fleet) scheduleRecoveryLocked(r *agentRec) {
	if f.closed || f.agents[r.a.ID] != r || r.a.Desired != "running" || r.recovery.pending {
		return
	}
	delay, reason := r.recovery.next(time.Now())
	if reason != "" {
		r.a.Desired = "stopped"
		r.a.Error = reason + "; inspect the agent, then start it deliberately"
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: r.a.Error})
		return
	}
	r.recovery.cancel(false)
	r.recovery.cancelWait = make(chan struct{})
	cancelWait := r.recovery.cancelWait
	generation := r.recovery.generation
	r.recovery.pending = true
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: fmt.Sprintf("Automatic restart %d scheduled in %s", len(r.recovery.attempts)+1, delay)})
	f.goTracked(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-cancelWait:
			return
		case <-f.ctx.Done():
			return
		case <-timer.C:
		}
		f.restoreAfterCrash(r, generation)
	})
}

func (f *Fleet) restoreAfterCrash(r *agentRec, generation uint64) {
	if err := f.startAgentOpts(r.a.ID, "", startOpts{recovery: r, recoveryGeneration: generation}); err != nil {
		f.mu.Lock()
		if f.agents[r.a.ID] == r && r.recovery.generation == generation && r.proc == nil && !r.starting && r.a.Desired == "running" {
			// Admission/configuration errors require intervention, never a retry storm.
			r.a.Desired = "stopped"
			r.a.Error = "automatic restart failed: " + err.Error()
			f.appendLocked(r, model.TimelineEntry{Kind: model.EntryError, IsError: true, Text: r.a.Error})
			f.saveAgentLocked(r)
		}
		f.mu.Unlock()
	}
}

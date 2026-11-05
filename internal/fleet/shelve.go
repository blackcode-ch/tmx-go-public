package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"time"
)

const idleShelveAfter = 2 * time.Hour

func (f *Fleet) autoShelveEnabled() bool { return f.opts.AutoShelve == nil || *f.opts.AutoShelve }

// MarkDone records an explicit completion signal from the authenticated agent.
// The current turn can finish before the idle event triggers shelving.
func (f *Fleet) MarkDone(c Caller, id string) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(id)
	if err != nil {
		return model.Agent{}, err
	}
	if c.Owner || c.Companion || c.AgentID != r.a.ID {
		return model.Agent{}, errf(KindForbidden, "agents may report only their own completion")
	}
	if f.closed || r.proc == nil || r.stopping || r.a.Desired != "running" {
		return model.Agent{}, errf(KindConflict, "agent is not running")
	}
	next := r.a
	next.DoneAt = model.Now()
	if err := f.st.PutAgent(next); err != nil {
		return model.Agent{}, err
	}
	r.a.DoneAt = next.DoneAt
	f.saveAgentLocked(r)
	return r.a, nil
}

func (f *Fleet) shelveEligibleLocked(r *agentRec, now time.Time) bool {
	if f.closed || !f.autoShelveEnabled() || r.a.Level != model.LevelWorker ||
		r.a.Harness == model.HarnessShell || r.a.Protected || r.a.ShelvedAt != 0 ||
		r.starting || r.stopping || r.waiting != 0 || r.a.Queued != 0 {
		return false
	}
	if r.a.State != model.StateIdle {
		return false
	}
	last := r.a.LastActivityAt
	if last == 0 {
		last = r.a.CreatedAt
	}
	return r.a.DoneAt != 0 || (last > 0 && now.UnixMilli()-last >= idleShelveAfter.Milliseconds())
}

// SweepShelved is also used on startup and daily. Admission is checked again
// inside stopProcessIf, so an agent becoming busy after the scan is not stopped.
func (f *Fleet) SweepShelved(now time.Time) {
	f.mu.Lock()
	var ids []string
	for id, r := range f.agents {
		if f.shelveEligibleLocked(r, now) {
			ids = append(ids, id)
		}
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.stopProcessIf(f.ctx, id, false, func(r *agentRec) bool {
			if !f.shelveEligibleLocked(r, now) {
				return false
			}
			next := r.a
			next.ShelvedAt, next.Desired = now.UnixMilli(), "stopped"
			// Persist the intent before stopping so a restart never resurrects it.
			if err := f.st.PutAgent(next); err != nil {
				f.log.Error("persist automatic shelving", "agent", id, "err", err)
				return false
			}
			r.a.ShelvedAt, r.a.Desired = next.ShelvedAt, next.Desired
			f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: "Shelved automatically; start to resume."})
			return true
		})
	}
}

func (f *Fleet) startShelveLoop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.shelveLoopStarted || !f.autoShelveEnabled() {
		return
	}
	f.shelveLoopStarted = true
	f.goTracked(func() {
		minute := time.NewTicker(time.Minute)
		daily := time.NewTicker(24 * time.Hour)
		defer minute.Stop()
		defer daily.Stop()
		for {
			select {
			case <-f.ctx.Done():
				return
			case now := <-minute.C:
				f.SweepShelved(now)
			case now := <-daily.C:
				f.SweepShelved(now)
			}
		}
	})
}

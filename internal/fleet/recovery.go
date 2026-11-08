package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"time"
)

// Start brings the fleet up after a (re)start of the daemon: nothing from the previous
// daemon is alive, so live-looking states are reset and open permission requests expire;
// the head is ensured; then every agent with desired=running is started on its session.
// Pending questions (ask_owner) stay pending: their answer is delivered as mail.
func (f *Fleet) Start() error {
	// Inspect persisted idle/completion markers before startup resets live states.
	f.SweepShelved(time.Now())
	// Working folders are admitted and created per agent in the spawn path.
	// An unsafe workspace or elevated daemon must not disable owner controls.
	pending, err := f.st.ListApprovals(model.ApprovalPending)
	if err != nil {
		return err
	}

	f.mu.Lock()
	f.applyRuleDefaultsLocked() // log the rule-switch defaults once (the owner, 3 Nov 22:14)
	for i := range pending {
		if pending[i].Kind == model.ApprovalPermission || pending[i].Kind == model.ApprovalGuard {
			f.finishApprovalLocked(&pending[i], model.ApprovalExpired, "")
		}
	}
	for _, r := range f.agents {
		switch r.a.State {
		case model.StateStarting, model.StateWorking, model.StateIdle, model.StateWaiting, model.StatePaused:
			r.a.State = model.StateStopped
		}
		if r.a.PID != 0 || r.a.State == model.StateStopped {
			r.a.PID = 0
			f.saveAgentLocked(r)
		}
	}
	if _, _, err := f.ensureHeadLocked(); err != nil {
		f.mu.Unlock()
		return err
	}
	var toStart []string
	for _, a := range f.agentsLocked() { // creation order: head, then leads, then workers
		if a.Desired == "running" {
			toStart = append(toStart, a.ID)
		}
	}
	toStart = f.bootOrderLocked(toStart)
	f.mu.Unlock()

	for _, id := range toStart {
		if err := f.startAgentOpts(id, "", startOpts{reattach: true}); err != nil {
			f.log.Warn("restart agent", "agent", id, "err", err)
			f.mu.Lock()
			if r := f.agents[id]; r != nil && r.proc == nil && !r.starting {
				r.a.State, r.a.Error = model.StateError, err.Error()
				f.saveAgentLocked(r)
			}
			f.mu.Unlock()
		}
	}
	f.adoptTools()
	f.startShelveLoop()
	f.startReminderLoop() // fires what came due while the daemon was down, then keeps time
	return nil
}

// adoptTools takes over the agents' tools an earlier daemon left running in their session
// hosts; a tool whose agent is gone is stopped.
func (f *Fleet) adoptTools() {
	for _, p := range f.processes.Adopt() {
		f.mu.Lock()
		_, ok := f.agents[p.OwnerID]
		f.mu.Unlock()
		if !ok {
			f.processes.Stop(p.ID)
			continue
		}
		f.log.Info("tool still running after the daemon restarted", "process", p.ID, "agent", p.OwnerID, "purpose", p.Purpose)
	}
}

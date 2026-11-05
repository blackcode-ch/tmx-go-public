package fleet

import (
	"fmt"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// consume drains one process's events until the channel closes. It always drains, even
// after the agent is gone, so a runner never blocks on a full channel.
func (f *Fleet) consume(r *agentRec, proc runner.Process, done chan struct{}) {
	defer close(done)
	exited := false
	for ev := range proc.Events() {
		if ev.Kind == runner.EvExit {
			exited = true
			f.handleExit(r, proc, ev.ExitCode, ev.Err)
			continue
		}
		if f.handleEvent(r, proc, ev) {
			id := r.a.ID // safe: ID never changes
			f.goTracked(func() { f.tryDeliver(id) })
		}
	}
	if !exited {
		f.handleExit(r, proc, 0, nil)
	}
	f.afterExit(r)
	f.afterSwitchExit(r)
}

// handleEvent applies one event; it reports whether queued mail may now be deliverable.
func (f *Fleet) handleEvent(r *agentRec, proc runner.Process, ev runner.Event) (deliver bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.proc != proc || f.agents[r.a.ID] != r {
		return false // a stale process (restarted or removed agent)
	}
	f.observeTasksLocked(r, ev)
	switch ev.Kind {
	case runner.EvMailDelivery:
		if ev.Delivery == "submitted" || ev.Delivery == "unconfirmed" {
			f.mailReceiptLocked(r.a.ID, ev.MessageIDs, ev.Delivery)
		}
	case runner.EvState:
		st := ev.State
		if f.pausedStateLocked(r, st) {
			return false
		}
		if r.waiting > 0 && st == model.StateWorking {
			st = model.StateWaiting // a permission request is still open
		}
		if st == r.a.State {
			return false
		}
		r.a.State = st
		r.a.LastActivityAt = model.Now()
		f.saveAgentLocked(r)
		if st == model.StateIdle {
			r.idleAt = time.Now()
			f.scheduleSwitchLocked(r) // a switch waiting for the end of the turn
			if r.a.DoneAt != 0 && f.autoShelveEnabled() && !f.closed {
				f.goTracked(func() { f.SweepShelved(time.Now()) })
			}
		}
		return st == model.StateIdle
	case runner.EvTimeline:
		// The fleet records user turns itself (with sender meta) when it sends them, so a
		// runner's echo of the sent text would be a duplicate. What someone typed into the
		// agent's own screen (meta source "screen") only the runner knows: a new turn.
		if ev.Entry != nil && (ev.Entry.Kind != model.EntryUser || typedInScreen(*ev.Entry)) {
			if ev.Entry.Kind == model.EntryUser && isHead(r) {
				f.beginTurnLocked(r, model.GeneralThread, false)
			}
			f.appendLocked(r, *ev.Entry)
			if ev.Entry.Kind == model.EntryResult && isHead(r) {
				f.endTurnLocked(r)
			}
		}
	case runner.EvApproval:
		if ev.Approval != nil {
			f.addPermissionLocked(r, proc, *ev.Approval)
		}
	case runner.EvApprovalCancelled:
		if ev.Approval != nil {
			f.cancelPermissionLocked(r, ev.Approval.ID)
		}
	case runner.EvSession:
		r.sessionConfirmed = true
		if ev.SessionID != "" && ev.SessionID != r.a.SessionID {
			r.a.SessionID = ev.SessionID
			f.saveAgentLocked(r)
		}
	case runner.EvUsage:
		// Each result's cost and turns are counted as that turn's and added (claude's
		// total_cost_usd is not verified to be cumulative across turns or --resume).
		r.a.CostUSD += ev.CostUSD
		r.a.Turns += ev.Turns
		f.saveAgentLocked(r)
	case runner.EvOutput:
		r.a.LastActivityAt = model.Now()
		f.fanOutLocked(r, ev.Output)
	case runner.EvRateLimit:
		if ev.RateLimit != nil && f.rateLimited != nil {
			go f.rateLimited(r.a.Harness, r.a.Account, *ev.RateLimit) // never under f.mu
		}
	}
	return false
}

func (f *Fleet) handleExit(r *agentRec, proc runner.Process, code int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.proc != proc {
		return
	}
	if r.detaching { // the daemon is stopping; the agent runs on in its session host
		r.detaching = false
		r.proc = nil
		return
	}
	f.observeTasksLocked(r, runner.Event{Kind: runner.EvExit})
	f.processes.StopOwner(r.a.ID)
	if ids, receiptErr := f.st.PendingMailReceipts(r.a.ID); receiptErr == nil {
		f.mailReceiptLocked(r.a.ID, ids, "unconfirmed")
	} else {
		f.log.Error("interrupted mail receipts", "err", receiptErr)
	}
	r.proc = nil
	r.a.PID = 0
	if r.token != "" {
		delete(f.tokens, r.token)
		r.token = ""
	}
	text, switched := f.exitModeLocked(r, code, err) // mode.go
	switch {
	case switched:
	case r.stopping || r.a.Desired == "stopped":
		r.a.State = model.StateStopped
		text = "stopped"
	case err != nil:
		r.a.State, r.a.Error = model.StateError, err.Error()
		text = "process failed: " + err.Error()
	case code != 0:
		r.a.State, r.a.Error = model.StateError, fmt.Sprintf("exited with code %d", code)
		text = "process " + r.a.Error
	default:
		r.a.State, text = model.StateExited, "process exited"
	}
	unexpected := !switched && !r.stopping && r.a.Desired == "running"
	r.stopping = false
	f.expirePermsLocked(r.a.ID)
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: text, IsError: r.a.State == model.StateError})
	if unexpected {
		f.scheduleRecoveryLocked(r)
	}
	f.saveAgentLocked(r)
	f.closeTermsLocked(r)
}

// appendLocked assigns seq/agent/time, stores the entry and publishes timeline.appended.
func (f *Fleet) appendLocked(r *agentRec, e model.TimelineEntry) model.TimelineEntry {
	if e.Kind == model.EntryUser || e.Kind == model.EntryMessageIn {
		r.a.DoneAt = 0 // new work supersedes the previous completion report
	}
	r.seq++
	e.Seq, e.AgentID = r.seq, r.a.ID
	if isHead(r) {
		e.ThreadID = threadOr(r.thread)
	}
	if e.At == 0 {
		e.At = model.Now()
	}
	r.a.LastActivityAt = e.At
	if err := f.st.AppendTimeline(e); err != nil {
		f.log.Error("store timeline", "agent", r.a.Name, "err", err)
	}
	f.bus.Publish(model.BusEvent{Type: model.EvTimelineAppended, AgentID: r.a.ID, ProjectID: r.a.ProjectID, Data: e})
	return e
}

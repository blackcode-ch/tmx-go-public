package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// consume drains one process's events until the channel closes. It always drains, even
// after the agent is gone, so a runner never blocks on a full channel.
func (f *Fleet) consume(r *agentRec, proc runner.Process, done chan struct{}) {
	defer close(done)
	// The ID is stable, but controls replace the whole r.a value under f.mu.
	f.mu.Lock()
	id := r.a.ID
	f.mu.Unlock()
	exited := false
	for ev := range proc.Events() {
		if ev.Kind == runner.EvExit {
			exited = true
			f.handleExit(r, proc, ev.ExitCode, ev.Err)
			continue
		}
		if f.handleEvent(r, proc, ev) {
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
		if ev.Delivery == "submitted" || ev.Delivery == "unconfirmed" || ev.Delivery == "menu_wait" || ev.Delivery == "dispatching" {
			f.mailReceiptLocked(r.a.ID, ev.MessageIDs, ev.Delivery)
			if d, ok := proc.(runner.MailDiagnoser); ok && (ev.Delivery == "submitted" || ev.Delivery == "unconfirmed") {
				for _, id := range ev.MessageIDs {
					d.ForgetMailStage(id)
				}
			}
		}
	case runner.EvState:
		st := ev.State
		if st == model.StateIdle {
			f.confirmClaudeAccountSwitchLocked(r)
			r.mailInterrupt = false
			r.updateIdleAt = time.Now()
		} else {
			r.updateIdleAt = time.Time{}
			// Runner transitions are activity even if the fleet's stale state
			// already says working (for example after a missed idle event).
			r.updateActivity = time.Now()
		}
		// A positively observed idle input box supersedes stale screen-menu approvals.
		// Other permission/guard requests remain in force.
		if st == model.StateIdle && typedTerminal(r.a) {
			for id, pr := range f.perms {
				if pr.agentID == r.a.ID && pr.proc == proc {
					if ap, err := f.st.GetApproval(id); err == nil && ap.Tool == model.ToolTerminalMenu {
						f.cancelPermissionLocked(r, pr.requestID)
					}
				}
			}
		}
		if f.pausedStateLocked(r, st) {
			return false
		}
		if r.waiting > 0 && st == model.StateWorking {
			st = model.StateWaiting // a permission request is still open
		}
		if st == r.a.State {
			return st == model.StateIdle
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
				if r.nextThread != "" { // the owner wrote on another topic meanwhile (mail.go deliverOwner)
					f.beginTurnLocked(r, r.nextThread, r.nextExplicit)
					r.nextThread, r.nextExplicit = "", false
				}
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
		if origin := r.accountOrigin; origin != nil && origin.resume && ev.SessionID != "" && ev.SessionID != origin.session {
			if !origin.rejected {
				origin.rejected = true
				f.goTracked(func() {
					ctx, cancel := context.WithTimeout(f.ctx, stopGrace)
					defer cancel()
					if err := proc.Stop(ctx); err != nil {
						f.log.Warn("stop Claude alternate with wrong conversation", "err", err)
					}
				})
			}
			return false
		}
		r.sessionConfirmed = true
		if ev.SessionID != "" && ev.SessionID != r.a.SessionID {
			if r.codexLost && r.a.SessionID != "" {
				// The stored conversation could not be found for this start (codex_carry.go): the
				// new one is named, never stored over it.
				f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem,
					Text: "This start runs as a new conversation " + ev.SessionID + "; the stored conversation is still " + r.a.SessionID + "."})
				break
			}
			r.a.SessionID = ev.SessionID
			f.saveAgentLocked(r)
		}
	case runner.EvUsage:
		// Each result's cost and turns are counted as that turn's and added (claude's
		// total_cost_usd is not verified to be cumulative across turns or --resume).
		if r.a.Profile != model.FreeWorkerProfile {
			r.a.CostUSD += ev.CostUSD
		}
		r.a.Turns += ev.Turns
		if ev.Turns > 0 && r.a.Profile != model.FreeWorkerProfile {
			f.recordAccountTurnsLocked(r.a, ev.Turns)
		}
		f.saveAgentLocked(r)
	case runner.EvProgress:
		r.updateActivity = time.Now()
		r.updateIdleAt = time.Time{}
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
	f.deleteCodexGuardAuth(r.a.ID)
	f.observeTasksLocked(r, runner.Event{Kind: runner.EvExit})
	f.processes.StopOwner(r.a.ID)
	if ids, receiptErr := f.st.PendingMailReceipts(r.a.ID); receiptErr == nil {
		f.mailReceiptLocked(r.a.ID, ids, "unconfirmed")
	} else {
		f.log.Error("interrupted mail receipts", "err", receiptErr)
	}
	if ids, receiptErr := f.st.RequeueMenuMail(r.a.ID); receiptErr == nil {
		for _, id := range ids {
			if m, err := f.st.GetMessage(id); err == nil {
				f.bus.Publish(model.BusEvent{Type: model.EvMessageUpdated, AgentID: r.a.ID, Data: m})
			}
		}
		if n, err := f.st.CountQueued(r.a.ID); err == nil {
			r.a.Queued = n
		}
	} else {
		f.log.Error("recover unpasted menu mail", "err", receiptErr)
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
	switch {
	case r.accountOrigin != nil:
		origin, id := r.accountOrigin, r.a.ID
		r.a.Account, r.a.SessionID, r.sessionConfirmed = origin.account, origin.session, origin.confirmed
		r.accountOrigin = nil
		if unexpected && !f.closed {
			f.goTracked(func() {
				if err := f.startAgentOpts(id, "", startOpts{StartOptions: StartOptions{Force: true}, accountSwitch: true}); err != nil {
					f.log.Warn("restore Claude account after pre-ready exit", "agent", id, "err", err)
				}
			})
		}
	case unexpected && f.freshAfterLostConversationLocked(r, err):
		// The harness does not have the conversation it was told to resume: one fresh start
		// instead of restarts that fail the same way (resume_fallback.go).
		id := r.a.ID
		f.goTracked(func() { _ = f.startAgentOpts(id, "", startOpts{}) }) // a failed start leaves the agent in state error
	case unexpected:
		f.scheduleRecoveryLocked(r)
	}
	f.saveAgentLocked(r)
	f.closeTermsLocked(r)
}

// appendLocked assigns seq/agent/time, stores the entry and publishes timeline.appended.
func (f *Fleet) appendLocked(r *agentRec, e model.TimelineEntry) model.TimelineEntry {
	if e.Kind != model.EntrySystem {
		r.updateActivity = time.Now()
		r.updateTurnActivity = r.updateActivity
		r.updateIdleAt = time.Time{}
	}
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

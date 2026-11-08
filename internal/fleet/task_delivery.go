package fleet

import (
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Arm before claiming or sending any mail. A crash after arming is recovered as
// uncertain, never replayed. Input admitted over an existing turn invalidates it.
func (f *Fleet) armTasksLocked(r *agentRec, msgs []model.Message) bool {
	tasks, err := f.st.ActiveTasks(r.a.ID)
	if err != nil {
		f.log.Error("read task receipts", "err", err)
		return false
	}
	ids := map[string]bool{}
	for _, m := range msgs {
		ids[m.ID] = true
	}
	allowed := true
	for _, t := range tasks {
		if t.Receipt.State != "queued" {
			t.Receipt.Finish("uncertain", "Another input was admitted before this assigned turn settled.", "")
		} else if ids[t.Receipt.ID] {
			if model.TaskNow()-t.Receipt.CreatedAt >= 86400 {
				t.Receipt.Finish("expired", "The task expired before dispatch.", "")
				allowed = false
			} else if t.Receipt.AgentIdentity != model.AgentIdentity(r.a.ID) || r.a.SessionID == "" {
				t.Receipt.Finish("uncertain", "The accepted recipient or session could not be bound before submission.", "")
				allowed = false
			} else {
				session := r.a.SessionID
				t.Receipt.SessionID = &session
				t.AdmittedAt = model.TaskNow()
				t.Receipt.State, t.Receipt.UpdatedAt = "dispatching", max(t.AdmittedAt, t.Receipt.UpdatedAt)
				t.Ambiguous = len(msgs) != 1 || r.a.State != model.StateIdle
			}
		} else {
			continue
		}
		if err := f.st.SaveTask(t); err != nil {
			f.log.Error("arm task receipt", "err", err)
			return false
		}
	}
	return allowed
}

func (f *Fleet) taskSubmissionLocked(to string, ids []string, receipt string) {
	for _, id := range ids {
		t, err := f.st.Task(id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			f.log.Error("task submission", "err", err)
			continue
		}
		if t.AgentID != to || !t.Receipt.Active() || t.Receipt.State == "queued" {
			continue
		}
		switch receipt {
		case "accepted", "submitted":
			if t.Ambiguous {
				t.Receipt.Finish("uncertain", "Submission could not be attributed to a separate assigned turn.", "")
			} else {
				t.Submitted = true
				t.Receipt.State = "running"
				t.Receipt.UpdatedAt = max(model.TaskNow(), t.Receipt.UpdatedAt)
			}
		case "unconfirmed":
			t.Receipt.Finish("uncertain", "Submission could not be proved; this input will not be replayed.", "")
		default:
			continue
		}
		if err := f.st.SaveTask(t); err != nil {
			f.log.Error("save task submission", "err", err)
		}
	}
}

func (f *Fleet) observeTasksLocked(r *agentRec, ev runner.Event) {
	if ev.Kind != runner.EvState && ev.Kind != runner.EvTimeline && ev.Kind != runner.EvExit && ev.Kind != runner.EvSession {
		return
	}
	tasks, err := f.st.ActiveTasks(r.a.ID)
	if err != nil {
		f.log.Error("observe task receipts", "err", err)
		return
	}
	for _, t := range tasks {
		if t.Receipt.State == "queued" {
			continue
		}
		before := t
		if t.Receipt.SessionID == nil || *t.Receipt.SessionID != r.a.SessionID || (ev.Kind == runner.EvSession && ev.SessionID != "" && ev.SessionID != *t.Receipt.SessionID) {
			t.Receipt.Finish("uncertain", "The active session changed during the assigned turn.", "")
		} else if !r.humanInputAt.IsZero() && float64(r.humanInputAt.UnixMicro())/1e6 >= t.AdmittedAt {
			t.Receipt.Finish("uncertain", "Other input overlapped this assigned turn.", "")
		} else if ev.Kind == runner.EvExit {
			t.Receipt.Finish("interrupted", "The agent exited before a correlated turn result was settled.", t.Receipt.Report)
		} else if ev.Kind == runner.EvTimeline && ev.Entry != nil && ev.Entry.Kind == model.EntryUser && typedInScreen(*ev.Entry) {
			t.Receipt.Finish("uncertain", "Input from the agent's screen overlapped this assigned turn.", "")
		} else if t.Submitted {
			if ev.Kind == runner.EvState && ev.State == model.StateWorking {
				t.SawWorking = true
			}
			if ev.Kind == runner.EvTimeline && ev.Entry != nil && ev.Entry.Kind == model.EntryResult && r.a.Mode == model.ModeChat {
				// Structured chat results are ordered behind this one submitted turn.
				t.Receipt.Finish("turn_finished", "The assigned turn produced a result. Objective completion is unverified.", ev.Entry.Text)
			} else if ev.Kind == runner.EvState && ev.State == model.StateIdle && t.SawWorking && !t.Ambiguous && r.waiting == 0 && !f.pausedLocked(r.a.ID) {
				t.Receipt.Finish("turn_finished", "An observed working turn returned to idle. Objective completion is unverified.", t.Receipt.Report)
			}
		}
		if t == before {
			continue
		}
		if err := f.st.SaveTask(t); err != nil {
			f.log.Error("save task observation", "err", err)
		}
	}
}

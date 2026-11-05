package fleet

import (
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// RenameAgent keeps the stable ID (and all its mail, tokens and history). A running
// harness retains its launch-time name, so stop it before changing that identity.
func (f *Fleet) RenameAgent(c Caller, id, name string) (model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(id)
	if err != nil {
		return model.Agent{}, err
	}
	if err = f.authorizeActLocked(c, r); err != nil {
		return model.Agent{}, err
	}
	if r.a.Level == model.LevelHead {
		return model.Agent{}, errf(KindForbidden, "the head keeps its reserved name")
	}
	if !model.ValidName(name) || ReservedAgentName(name) {
		return model.Agent{}, errf(KindInvalid, "invalid or reserved agent name")
	}
	if name == r.a.Name {
		return r.a, nil
	}
	if _, err := f.resolveLocked(name); err == nil {
		return model.Agent{}, errf(KindConflict, "agent name already exists")
	}
	if r.proc != nil || r.starting || r.stopping || r.hostUnknown || r.hostStopping || r.a.Desired == "running" {
		return model.Agent{}, errf(KindConflict, "stop the agent before renaming it")
	}
	next := r.a
	next.Name, next.UpdatedAt = name, model.Now()
	if err := f.st.PutAgent(next); err != nil {
		return model.Agent{}, err
	}
	r.a = next
	f.bus.Publish(model.BusEvent{Type: model.EvAgentUpdated, AgentID: next.ID, ProjectID: next.ProjectID, Data: next})
	return next, nil
}

// ShelveAgent persists the shelf intent under the same lock as stopping admission.
func (f *Fleet) ShelveAgent(c Caller, id, note string) (model.Agent, error) {
	id, err := f.authorizeAct(c, id)
	if err != nil {
		return model.Agent{}, err
	}
	if len([]rune(note)) > 500 {
		return model.Agent{}, errf(KindInvalid, "shelf note is longer than 500 characters")
	}
	f.stopProcessIf(f.ctx, id, false, func(r *agentRec) bool {
		if err = f.authorizeActLocked(c, r); err != nil {
			return false
		}
		next := r.a
		next.ShelvedAt, next.Desired = model.Now(), "stopped"
		if err = f.st.PutAgent(next); err != nil {
			return false
		}
		r.a = next
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem, Text: "Shelved. " + strings.TrimSpace(note)})
		return true
	})
	if err != nil {
		return model.Agent{}, err
	}
	return f.Agent(id)
}

func (f *Fleet) UnshelveAgent(c Caller, id string, start bool) (model.Agent, error) {
	f.mu.Lock()
	r, err := f.resolveLocked(id)
	if err == nil {
		err = f.authorizeActLocked(c, r)
	}
	if err != nil {
		f.mu.Unlock()
		return model.Agent{}, err
	}
	next := r.a
	next.ShelvedAt, next.DoneAt, next.UpdatedAt = 0, 0, model.Now()
	if err = f.st.PutAgent(next); err != nil {
		f.mu.Unlock()
		return model.Agent{}, err
	}
	r.a = next
	f.bus.Publish(model.BusEvent{Type: model.EvAgentUpdated, AgentID: next.ID, ProjectID: next.ProjectID, Data: next})
	f.mu.Unlock()
	if start {
		return f.StartAgent(c, next.ID)
	}
	return next, nil
}

// AgentAnswer resolves one live harness prompt, never an unrelated owner card or
// guard hold. The API must still run the ordinary approval gate before Decide.
func (f *Fleet) AgentAnswer(c Caller, id, choice string) (model.Approval, DecideRequest, error) {
	var ap model.Approval
	d := DecideRequest{}
	if !c.Owner {
		return ap, d, errf(KindForbidden, "only the owner answers harness prompts")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolveLocked(id)
	if err != nil {
		return ap, d, err
	}
	n := 0
	for aid, p := range f.perms {
		if p.agentID == r.a.ID && p.proc == r.proc {
			a, e := f.st.GetApproval(aid)
			if e != nil {
				return ap, d, e
			}
			if a.State == model.ApprovalPending {
				ap = a
				n++
			}
		}
	}
	if n != 1 {
		return ap, d, errf(KindConflict, "expected one live harness prompt; found %d", n)
	}
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return ap, d, errf(KindInvalid, "choice is required")
	}
	switch choice {
	case "approve", "allow":
		d.Decision = "allow"
	case "deny":
		d.Decision = "deny"
	default:
		if num, e := strconv.Atoi(choice); e == nil {
			if num < 1 || num > len(ap.Options) {
				return ap, d, errf(KindInvalid, "option number is out of range")
			}
			choice = ap.Options[num-1]
		}
		if choice == "approve_always" {
			return ap, d, errf(KindInvalid, "choose an explicit prompt option for persistent approval")
		}
		d.Decision, d.Answer = "answer", choice
	}
	return ap, d, nil
}

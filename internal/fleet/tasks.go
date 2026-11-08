package fleet

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

var taskID = regexp.MustCompile(`^[0-9a-f]{12,32}$`)

type TaskRequest struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	Body   string `json:"body"`
	From   string `json:"from,omitempty"`
	Urgent bool   `json:"urgent,omitempty"`
}

// Only the local operator is enabled while Companion process authentication is
// on hold. Existing restricted link credentials are not repurposed as task auth.
func (f *Fleet) taskSenderLocked(c Caller, requested string) (string, string, error) {
	if !c.Owner || c.Companion || c.Peer != "" || c.AgentID != "" {
		return "", "", errf(KindForbidden, "task assignment requires the local operator; Companion process authentication is not enabled")
	}
	host := strings.ToLower(f.Machine())
	name := requested
	if name == "" {
		name = "operator"
	}
	if name != "operator" && name != "companion" {
		return "", "", errf(KindForbidden, "task sender must be operator or companion")
	}
	return "operator@" + host, name + "@" + host, nil
}

func (f *Fleet) PostTask(c Caller, req TaskRequest) (model.TaskEnvelope, error) {
	var empty model.TaskEnvelope
	if !taskID.MatchString(req.ID) || strings.TrimSpace(req.Body) == "" || len(req.Body) > MaxBodyBytes || !utf8.ValidString(req.Body) {
		return empty, errf(KindInvalid, "invalid task id or body")
	}
	name, host, qualified := strings.Cut(strings.ToLower(strings.TrimSpace(req.To)), "@")
	local := strings.ToLower(f.Machine())
	if !model.ValidName(name) || (qualified && host != local) {
		return empty, errf(KindConflict, "task must target an agent on this daemon")
	}
	target := name + "@" + local
	f.mu.Lock()
	principal, sender, err := f.taskSenderLocked(c, req.From)
	if err != nil {
		f.mu.Unlock()
		return empty, err
	}
	// Replay precedes recipient existence and current subtree lookup: a renamed
	// or removed recipient cannot cause an already accepted task to be repeated.
	if prior, err := f.st.Task(req.ID); err == nil {
		f.mu.Unlock()
		if prior.Principal != principal || prior.Receipt.Sender != sender || prior.Receipt.To != target || prior.Body != req.Body || prior.Urgent != req.Urgent {
			return empty, errf(KindConflict, "task id already binds another request")
		}
		return prior.Receipt.Envelope(), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		f.mu.Unlock()
		return empty, err
	}
	if _, err := f.st.GetMessage(req.ID); err == nil {
		f.mu.Unlock()
		return empty, errf(KindConflict, "task id already belongs to ordinary mail")
	} else if !errors.Is(err, store.ErrNotFound) {
		f.mu.Unlock()
		return empty, err
	}
	r, err := f.resolveLocked(name)
	if err == nil {
		err = f.authorizeActLocked(c, r)
	}
	if err != nil {
		f.mu.Unlock()
		return empty, err
	}
	if r.a.Harness == model.HarnessShell {
		f.mu.Unlock()
		return empty, errf(KindConflict, "shells have no observable assigned turns")
	}
	active, err := f.st.ActiveTasks("")
	if err != nil {
		f.mu.Unlock()
		return empty, err
	}
	n, err := f.st.TaskCount()
	if err != nil {
		f.mu.Unlock()
		return empty, err
	}
	if len(active) >= 256 || n >= 100000 {
		f.mu.Unlock()
		return empty, errf(KindTooMany, "task receipt capacity reached; existing receipts are retained")
	}
	now := model.TaskNow()
	t := model.TaskRecord{Principal: principal, AgentID: r.a.ID, Body: req.Body, Urgent: req.Urgent,
		Receipt: model.TaskReceipt{ID: req.ID, To: target, Sender: sender, State: "queued", AgentIdentity: model.AgentIdentity(r.a.ID), CreatedAt: now, UpdatedAt: now}}
	from := c.ID()
	if sender == "companion@"+local {
		from = model.CompanionParty
	}
	m := model.Message{ID: req.ID, From: from, To: r.a.ID, Body: req.Body, State: model.MsgQueued, CreatedAt: model.Now(), Receipt: "queued", ReceiptAt: model.Now(), ThreadID: model.GeneralThread}
	if err := f.checkLimitsLocked(m); err != nil {
		f.mu.Unlock()
		return empty, err
	}
	if err := f.st.AcceptTask(t, m); err != nil {
		f.mu.Unlock()
		return empty, err
	}
	f.refreshQueuedLocked(r)
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: r.a.ID, Data: m})
	f.mu.Unlock()
	f.tryDeliver(r.a.ID)
	return f.GetTask(c, req.ID)
}

func (f *Fleet) GetTask(c Caller, id string) (model.TaskEnvelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	principal, _, err := f.taskSenderLocked(c, "")
	if err != nil {
		return model.TaskEnvelope{}, err
	}
	t, err := f.st.Task(id)
	if errors.Is(err, store.ErrNotFound) {
		return model.TaskEnvelope{}, errf(KindNotFound, "task not found")
	}
	if err != nil {
		return model.TaskEnvelope{}, err
	}
	if !c.Owner && t.Principal != principal {
		return model.TaskEnvelope{}, errf(KindForbidden, "task belongs to another assigner")
	}
	if t.Receipt.Active() {
		now := model.TaskNow()
		startWindow, finishWindow := f.opts.Tasks.Windows()
		if t.Receipt.State == "queued" {
			mail, mailErr := f.st.GetMessage(t.Receipt.ID)
			if mailErr != nil && !errors.Is(mailErr, store.ErrNotFound) {
				return model.TaskEnvelope{}, mailErr
			}
			if _, exists := f.agents[t.AgentID]; !exists {
				t.Receipt.Finish("cancelled", "The recipient was removed before dispatch.", "")
			} else if now-t.Receipt.CreatedAt >= 86400 {
				t.Receipt.Finish("expired", "The task expired before dispatch.", "")
			} else if mailErr != nil || mail.State != model.MsgQueued {
				t.Receipt.Finish("uncertain", "Mail changed outside this task's delivery observation.", "")
			}
		} else if now-t.AdmittedAt >= finishWindow || (!t.SawWorking && now-t.AdmittedAt >= startWindow) {
			t.Receipt.Finish("uncertain", "No correlated turn transition was observed within the observation window.", "")
		}
		if !t.Receipt.Active() {
			if err := f.st.SaveTask(t); err != nil {
				return model.TaskEnvelope{}, err
			}
		}
	}
	return t.Receipt.Envelope(), nil
}

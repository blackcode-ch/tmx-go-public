package fleet

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// DecideRequest is POST /api/approvals/{id}.
type DecideRequest struct {
	Version  int64  `json:"version,omitempty"`
	AnswerID string `json:"answer_id,omitempty"`
	Decision string `json:"decision"` // allow | deny | answer
	Message  string `json:"message,omitempty"`
	Answer   string `json:"answer,omitempty"`
	FreeText bool   `json:"free_text,omitempty"` // owner-written words may be delivered as correspondence after closure
	Scope    string `json:"scope,omitempty"`     // allow_always on a guard hold: agent (its profile) | any
}

// AskRequest is POST /api/approvals (an extension for the MCP tool ask_owner).
type AskRequest struct {
	ID             string            `json:"id,omitempty"` // replace one's own pending owner card
	Type           string            `json:"type,omitempty"`
	Context        string            `json:"context,omitempty"`
	Category       string            `json:"category,omitempty"`
	Recommendation string            `json:"recommendation,omitempty"`
	Action         *model.CardAction `json:"action,omitempty"`
	Question       string            `json:"question"`
	Options        []string          `json:"options,omitempty"`
}

// Approvals lists approvals in a state ("" = all).
func (f *Fleet) Approvals(state model.ApprovalState) ([]model.Approval, error) {
	out, err := f.st.ListApprovals(state)
	if out == nil {
		out = []model.Approval{}
	}
	return out, err
}

// addPermissionLocked turns a harness request (a permission, or a question the harness
// asks: codex's user-input tool, a terminal agent's menu) into a pending Approval.
func (f *Fleet) addPermissionLocked(r *agentRec, proc runner.Process, req runner.ApprovalRequest) {
	kind := req.Kind
	if kind == "" {
		kind = model.ApprovalPermission
	}
	opts := req.Options
	if opts == nil {
		opts = []string{}
	}
	ap := model.Approval{
		ID: model.NewID(), AgentID: r.a.ID, Kind: kind, Tool: req.Tool,
		Input: req.Input, Description: req.Description, Question: req.Question, Options: opts,
		State: model.ApprovalPending, CreatedAt: model.Now(),
	}
	if err := f.st.PutApproval(ap); err != nil {
		f.log.Error("store approval", "err", err)
	}
	f.perms[ap.ID] = permRequest{agentID: r.a.ID, requestID: req.ID, proc: proc}
	r.waiting++
	r.a.State = model.StateWaiting
	f.saveAgentLocked(r)
	f.bus.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: r.a.ID, ProjectID: r.a.ProjectID, Data: ap})
}

// AskOwner records a question from an agent; the answer arrives later as owner mail.
func (f *Fleet) AskOwner(c Caller, req AskRequest) (model.Approval, error) {
	if c.Owner || c.Peer != "" {
		return model.Approval{}, errf(KindForbidden, "only agents ask the owner questions")
	}
	card, err := prepareOwnerCard(&req)
	if err != nil {
		return model.Approval{}, err
	}
	if strings.TrimSpace(req.Question) == "" {
		return model.Approval{}, errf(KindInvalid, "the question is empty")
	}
	if len(req.Question) > MaxBodyBytes {
		return model.Approval{}, errf(KindInvalid, "the question is longer than %d KB", MaxBodyBytes>>10)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveOwnerQuestionLocked(c, req, card, nil)
}

// saveOwnerQuestionLocked shares versioning and persistence across ordinary and project cards.
// The caller holds f.mu and has validated the typed card and project membership.
func (f *Fleet) saveOwnerQuestionLocked(c Caller, req AskRequest, card *model.OwnerCard, later *bool) (model.Approval, error) {
	r, err := f.callerRecLocked(c)
	if err != nil {
		return model.Approval{}, err
	}
	if r == nil {
		return model.Approval{}, errf(KindForbidden, "only agents ask the owner questions")
	}
	opts := req.Options
	if opts == nil {
		opts = []string{}
	}
	ap := model.Approval{
		ID: model.NewID(), AgentID: r.a.ID, Kind: model.ApprovalQuestion, Question: req.Question,
		Options: opts, State: model.ApprovalPending, CreatedAt: model.Now(),
		Version: 1, Card: card,
	}
	if isHead(r) {
		ap.ThreadID = threadOr(r.thread)
	}
	if req.ID != "" {
		old, err := f.st.GetApproval(req.ID)
		if err != nil {
			return model.Approval{}, errf(KindNotFound, "no owner card %q", req.ID)
		}
		if old.AgentID != r.a.ID || old.Card == nil {
			return model.Approval{}, errf(KindForbidden, "only the asker can edit an owner card")
		}
		if old.Card.ProjectID != card.ProjectID {
			return model.Approval{}, errf(KindForbidden, "use the matching project card operation")
		}
		if old.State != model.ApprovalPending {
			return model.Approval{}, errf(KindConflict, "this card was already resolved")
		}
		ap.ID, ap.CreatedAt, ap.ThreadID, ap.Version = old.ID, old.CreatedAt, old.ThreadID, old.Version
		ap.Deferred = old.Deferred
		if later != nil {
			ap.Deferred = *later
		}
		if old.Deferred != ap.Deferred || old.Question != ap.Question || !reflect.DeepEqual(old.Options, ap.Options) || !reflect.DeepEqual(old.Card, ap.Card) {
			ap.Version++
		}
	}
	if req.ID == "" && later != nil {
		ap.Deferred = *later
	}
	if err := f.st.PutApproval(ap); err != nil {
		return model.Approval{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: r.a.ID, ProjectID: r.a.ProjectID, Data: ap})
	return ap, nil
}

// Decide resolves a pending approval (owner only).
func (f *Fleet) Decide(c Caller, id string, d DecideRequest) (model.Approval, error) {
	if !c.Owner {
		return model.Approval{}, errf(KindForbidden, "only the owner decides approvals")
	}
	f.mu.Lock()
	ap, err := f.st.GetApproval(id)
	if err != nil {
		f.mu.Unlock()
		return model.Approval{}, errf(KindNotFound, "no approval %q", id)
	}
	if ap.Card != nil && ap.Kind == model.ApprovalQuestion {
		result, mail, fresh, err := f.decideOwnerCardLocked(ap, d)
		f.mu.Unlock()
		if err == nil && mail != nil {
			if fresh {
				f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: mail.To, Data: *mail})
			}
			f.deliverOwner(*mail)
		}
		return result, err
	}
	if ap.Tool == ToolOwnerRecovery {
		f.mu.Unlock()
		return model.Approval{}, errf(KindForbidden, "recovery notices cannot be decided; use the proven host UI session and a fresh host proof to upgrade")
	}
	if ap.State != model.ApprovalPending {
		f.mu.Unlock()
		return model.Approval{}, errf(KindConflict, "this approval was already resolved (%s)", ap.State)
	}
	if ap.Tool == ToolProcessStart {
		defer f.mu.Unlock()
		return f.decideProcessLocked(ap, d)
	}
	if ap.Card != nil && d.Version != ap.Version {
		f.mu.Unlock()
		return model.Approval{}, errf(KindConflict, "this question changed; review the current card before answering")
	}
	if _, live := f.perms[id]; ap.Kind == model.ApprovalQuestion && !live {
		f.mu.Unlock()
		return f.answerQuestion(ap, d) // ask_owner: the answer goes back as mail
	}
	if ap.Kind == model.ApprovalBrake {
		f.mu.Unlock()
		return f.decideBrake(ap, d)
	}
	if ap.Kind == model.ApprovalGuard {
		f.mu.Unlock()
		return f.decideGuard(ap, d)
	}
	if ap.Kind == model.ApprovalCompanion {
		f.mu.Unlock()
		return f.decideCompanion(ap, d)
	}
	if ap.Kind == model.ApprovalWhatsApp {
		f.mu.Unlock()
		return f.decideWhatsApp(ap, d) // whatsapp.go
	}

	allow, answer, err := harnessDecision(ap, d)
	if err != nil {
		f.mu.Unlock()
		return model.Approval{}, err
	}
	pr, live := f.perms[id]
	r := f.agents[ap.AgentID]
	if !live || r == nil || r.proc != pr.proc {
		f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		delete(f.perms, id)
		f.mu.Unlock()
		return ap, errf(KindConflict, "the agent's process has ended; the request expired")
	}
	state, verdict, recorded := model.ApprovalDenied, "denied "+ap.Tool, d.Message
	switch {
	case answer != "":
		state, verdict, recorded = model.ApprovalAnswered, fmt.Sprintf("answered %q", answer), answer
	case allow:
		state, verdict = model.ApprovalAllowed, "allowed "+ap.Tool
	}
	if ap.Tool == model.ToolTerminalMenu && answer == "" {
		verdict = strings.Replace(verdict, ap.Tool, "the request on the screen", 1)
	}
	proc := pr.proc
	requestID := pr.requestID
	delete(f.perms, id)
	f.mu.Unlock()

	msg := d.Message
	if !allow && msg == "" {
		msg = "The owner denied this."
	}
	if err := proc.Answer(requestID, runner.Decision{Allow: allow, Message: msg, Answer: answer}); err != nil {
		// Restore the pending request so the owner can retry.
		f.mu.Lock()
		f.perms[id] = permRequest{agentID: ap.AgentID, requestID: requestID, proc: proc}
		f.mu.Unlock()
		return ap, errf(KindConflict, "the decision could not be passed to the agent: %v", err)
	}

	f.mu.Lock()
	// The request may have been resolved meanwhile; only finalise if it is still pending.
	cur, gerr := f.st.GetApproval(id)
	if gerr != nil || cur.State != model.ApprovalPending {
		f.mu.Unlock()
		return ap, errf(KindConflict, "this approval was resolved while the decision was sent")
	}
	f.finishApprovalLocked(&ap, state, recorded)
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntrySystem,
		Text: "owner " + verdict, Tool: ap.Tool, Meta: map[string]any{"approval_id": ap.ID}})
	if r != nil {
		if r.waiting > 0 {
			r.waiting--
		}
		if r.waiting == 0 && r.a.State == model.StateWaiting {
			r.a.State = model.StateWorking
		}
		f.saveAgentLocked(r)
	}
	f.mu.Unlock()
	return ap, nil
}

func (f *Fleet) answerQuestion(ap model.Approval, d DecideRequest) (model.Approval, error) {
	f.mu.Lock()
	current, err := f.st.GetApproval(ap.ID)
	if err != nil || current.State != model.ApprovalPending || (current.Card != nil && current.Version != d.Version) {
		f.mu.Unlock()
		return model.Approval{}, errf(KindConflict, "this question changed or was already resolved")
	}
	ap = current
	var body string
	switch d.Decision {
	case "answer", "allow":
		ans := d.Answer
		if ans == "" {
			ans = d.Message
		}
		if strings.TrimSpace(ans) == "" {
			f.mu.Unlock()
			return model.Approval{}, errf(KindInvalid, "the answer is empty")
		}
		f.finishApprovalLocked(&ap, model.ApprovalAnswered, ans)
		f.mu.Unlock()
		body = fmt.Sprintf("Answer to your question %q: %s", ap.Question, ans)
	case "deny":
		f.finishApprovalLocked(&ap, model.ApprovalDenied, d.Message)
		f.mu.Unlock()
		body = fmt.Sprintf("The owner declined to answer your question %q.", ap.Question)
		if d.Message != "" {
			body += " " + d.Message
		}
	default:
		f.mu.Unlock()
		return model.Approval{}, errf(KindInvalid, "decision must be answer or deny for a question")
	}
	if _, err := f.SendMessage(OwnerCaller, SendRequest{To: ap.AgentID, Body: body, ThreadID: f.liveThread(ap.ThreadID)}); err != nil {
		f.log.Warn("deliver answer", "agent", ap.AgentID, "err", err)
	}
	return ap, nil
}

// finishApprovalLocked sets a final state, persists and publishes approval.resolved.
func (f *Fleet) finishApprovalLocked(ap *model.Approval, state model.ApprovalState, answer string) {
	ap.State, ap.Answer, ap.DecidedAt = state, answer, model.Now()
	if err := f.st.PutApproval(*ap); err != nil {
		f.log.Error("store approval", "err", err)
	}
	f.bus.Publish(model.BusEvent{Type: model.EvApprovalResolved, AgentID: ap.AgentID, Data: *ap})
}

// cancelPermissionLocked expires a permission request the harness withdrew.
func (f *Fleet) cancelPermissionLocked(r *agentRec, requestID string) {
	for id, pr := range f.perms {
		if pr.agentID != r.a.ID || pr.requestID != requestID {
			continue
		}
		delete(f.perms, id)
		if ap, err := f.st.GetApproval(id); err == nil && ap.State == model.ApprovalPending {
			f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		}
		if r.waiting > 0 {
			r.waiting--
		}
		if r.waiting == 0 && r.a.State == model.StateWaiting {
			r.a.State = model.StateWorking
			f.saveAgentLocked(r)
		}
		return
	}
}

// expirePermsLocked expires the open permission requests (and guard holds) of an agent whose
// process ended.
func (f *Fleet) expirePermsLocked(agentID string) {
	for id, p := range f.processRequests {
		if p.agentID == agentID {
			delete(f.processRequests, id)
			if ap, err := f.st.GetApproval(id); err == nil && ap.State == model.ApprovalPending {
				f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
			}
		}
	}
	f.expireHoldsLocked(agentID)
	for id, pr := range f.perms {
		if pr.agentID != agentID {
			continue
		}
		delete(f.perms, id)
		if ap, err := f.st.GetApproval(id); err == nil && ap.State == model.ApprovalPending {
			f.finishApprovalLocked(&ap, model.ApprovalExpired, "")
		}
	}
	if r, ok := f.agents[agentID]; ok {
		r.waiting = 0
	}
}

// harnessDecision reads a decision on a harness request: allow / deny, or (for a question
// or a menu with options) answer with an option or text.
func harnessDecision(ap model.Approval, d DecideRequest) (allow bool, answer string, err error) {
	choosable := ap.Kind == model.ApprovalQuestion || len(ap.Options) > 0
	switch d.Decision {
	case "deny":
		return false, "", nil
	case "allow":
		if ap.Kind != model.ApprovalQuestion {
			return true, "", nil
		}
	case "answer":
		if !choosable {
			return false, "", errf(KindInvalid, "decision must be allow or deny for a permission request")
		}
	default:
		if !choosable {
			return false, "", errf(KindInvalid, "decision must be allow or deny for a permission request")
		}
		return false, "", errf(KindInvalid, "decision must be answer, allow or deny")
	}
	answer = d.Answer
	if answer == "" {
		answer = d.Message
	}
	if strings.TrimSpace(answer) == "" {
		return false, "", errf(KindInvalid, "the answer is empty")
	}
	return true, strings.TrimSpace(answer), nil
}

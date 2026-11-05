package fleet

import (
	"regexp"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ProjectCardRequest keeps project-only metadata out of the ordinary ask_owner API.
// Nil Later preserves a card's parked state when its content is edited.
type ProjectCardRequest struct {
	AskRequest
	Priority  int    `json:"priority,omitempty"`
	Urgent    bool   `json:"urgent,omitempty"`
	Later     *bool  `json:"later,omitempty"`
	OwnerOnly bool   `json:"owner_only,omitempty"` // also read under model.LegacyOwnerOnlyKeys (ProjectImportRequest)
	Batchable bool   `json:"batchable,omitempty"`
	CloseWhen string `json:"close_when,omitempty"`
}

// projectCardStates are the project states a card can wait for (close_when state:<project>:<STATE>).
// The private build appends the fleet's own name for WAITING_ON_OWNER (fleet_private.go).
var projectCardStates = []string{"GOING", "WAITING_ON_OWNER", "WAITING_ON_ARCHITECT", "WAITING_ON_LEAD", "QUEUED", "DONE", "PAUSED"}

var projectCardCloseWhen = closeWhenRx()

func closeWhenRx() *regexp.Regexp {
	return regexp.MustCompile(`^(event:[a-z][a-z0-9-]{1,40}(:[a-z0-9][a-z0-9._-]{0,40})?|commit:[0-9a-f]{7,40}|enrolled|state:[a-z0-9][a-z0-9-]{0,60}:(` +
		strings.Join(projectCardStates, "|") + `))$`)
}

// projectCardAccessLocked excludes outsiders and Companion, including cross-project heads.
func (f *Fleet) projectCardAccessLocked(c Caller, project string) (*model.Project, error) {
	if c != OwnerCaller && (c.AgentID == "" || c != AgentCaller(c.AgentID)) {
		return nil, errf(KindForbidden, "only project members or the owner can access project cards")
	}
	me, err := f.callerRecLocked(c)
	if err != nil {
		return nil, err
	}
	p, err := f.projectLocked(project)
	if err != nil {
		return nil, err
	}
	if me != nil && me.a.ProjectID != p.ID {
		return nil, errf(KindForbidden, "only project members or the owner can access project cards")
	}
	return p, nil
}

// AskProjectOwner creates or edits one WAITING_ON_OWNER item. The item ID is
// the approval ID; the normal snapshot/events and Decide path handle the UI/mail.
func (f *Fleet) AskProjectOwner(c Caller, project string, req ProjectCardRequest) (model.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.askProjectOwnerLocked(c, project, req)
}

func (f *Fleet) askProjectOwnerLocked(c Caller, project string, req ProjectCardRequest) (model.Approval, error) {
	p, err := f.projectCardAccessLocked(c, project)
	if err != nil {
		return model.Approval{}, err
	}
	if c.Owner {
		return model.Approval{}, errf(KindForbidden, "only agents ask the owner questions")
	}
	card, err := prepareOwnerCard(&req.AskRequest)
	if err != nil {
		return model.Approval{}, err
	}
	if strings.TrimSpace(req.Question) == "" || len(req.Question) > MaxBodyBytes {
		return model.Approval{}, errf(KindInvalid, "project question is empty or too long")
	}
	if req.Priority < 0 {
		return model.Approval{}, errf(KindInvalid, "priority must be positive or zero for unspecified")
	}
	closeWhen := strings.TrimSpace(req.CloseWhen)
	if closeWhen != "" && !projectCardCloseWhen.MatchString(closeWhen) {
		return model.Approval{}, errf(KindInvalid, "close_when must be event:<kind>[:<agent>], commit:<sha>, enrolled or state:<project>:<STATE>")
	}
	card.ProjectID, card.Priority, card.Urgent = p.ID, req.Priority, req.Urgent
	card.Batchable, card.CloseWhen = req.Batchable, closeWhen
	card.OwnerOnly = req.OwnerOnly
	return f.saveOwnerQuestionLocked(c, req.AskRequest, card, req.Later)
}

// ProjectOwnerCards returns every project card, including parked and resolved
// items. Callers derive the open/later lists without a second source of truth.
func (f *Fleet) ProjectOwnerCards(c Caller, project string) ([]model.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.projectOwnerCardsLocked(c, project)
}

func (f *Fleet) projectOwnerCardsLocked(c Caller, project string) ([]model.Approval, error) {
	p, err := f.projectCardAccessLocked(c, project)
	if err != nil {
		return nil, err
	}
	all, err := f.st.ListApprovals("")
	if err != nil {
		return nil, err
	}
	items := []model.Approval{}
	for _, a := range all {
		if a.Kind == model.ApprovalQuestion && a.Card != nil && a.Card.ProjectID == p.ID {
			items = append(items, a)
		}
	}
	return items, nil
}

// CloseProjectOwnerCard retires a question without inventing an owner decision.
// Late typed/dictated answers remain deliverable through Decide as correspondence.
func (f *Fleet) CloseProjectOwnerCard(c Caller, project, id, reason string) (model.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeProjectOwnerCardLocked(c, project, id, reason)
}

func (f *Fleet) closeProjectOwnerCardLocked(c Caller, project, id, reason string) (model.Approval, error) {
	p, err := f.projectCardAccessLocked(c, project)
	if err != nil {
		return model.Approval{}, err
	}
	a, err := f.st.GetApproval(id)
	if err != nil {
		return model.Approval{}, errf(KindNotFound, "no project card %q", id)
	}
	if a.Kind != model.ApprovalQuestion || a.Card == nil || a.Card.ProjectID != p.ID {
		return model.Approval{}, errf(KindForbidden, "card does not belong to this project")
	}
	if !c.Owner && a.AgentID != c.AgentID {
		return model.Approval{}, errf(KindForbidden, "only the asker or owner can close this card")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > MaxBodyBytes {
		return model.Approval{}, errf(KindInvalid, "a nonempty close reason within the text limit is required")
	}
	if a.State == model.ApprovalExpired && a.Card.CloseReason == reason {
		return a, nil
	}
	if a.State != model.ApprovalPending {
		return model.Approval{}, errf(KindConflict, "this card was already resolved")
	}
	content := *a.Card
	a.Card = &content
	a.Card.CloseReason = reason
	a.State, a.Deferred, a.DecidedAt = model.ApprovalExpired, false, model.Now()
	a.Version++
	if err := f.st.PutApproval(a); err != nil {
		return model.Approval{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvApprovalResolved, AgentID: a.AgentID, ProjectID: p.ID, Data: a})
	return a, nil
}

// projectCardAnswerRecipientLocked chooses the recipient before the atomic answer
// commit. A retry uses the stored receipt/mail and never chooses another recipient.
func (f *Fleet) projectCardAnswerRecipientLocked(a model.Approval) string {
	if a.Card == nil || a.Card.ProjectID == "" {
		return a.AgentID
	}
	if IsPeer(a.AgentID) {
		return a.AgentID
	}
	asker := f.agents[a.AgentID]
	if cardAgentRunning(asker) {
		return a.AgentID
	}
	if p := f.projects[a.Card.ProjectID]; p != nil {
		if IsPeer(p.LeadID) {
			return p.LeadID
		}
		lead := f.agents[p.LeadID]
		if lead != nil && lead.a.ID != a.AgentID && lead.a.Level == model.LevelLead && lead.a.Name != "tmx-lead" && cardAgentRunning(lead) {
			return lead.a.ID
		}
	}
	if architect := f.cardArchitectLocked(); architect != nil {
		return architect.a.ID
	}
	if !f.cardRouteWarnings[a.ID] {
		if f.cardRouteWarnings == nil {
			f.cardRouteWarnings = map[string]bool{}
		}
		f.cardRouteWarnings[a.ID] = true
		f.log.Warn("project card has no fallback recipient; answer retained for original asker", "card", a.ID, "project", a.Card.ProjectID, "asker", a.AgentID)
	}
	return a.AgentID
}

// SupervisorName exposes the trusted local setting, not an agent-provided name.
// An absent key defaults to architect; only explicit empty permits head resolution.
func (f *Fleet) SupervisorName() string {
	if f.opts.Supervisor == nil {
		return "architect"
	}
	return strings.TrimSpace(*f.opts.Supervisor)
}

func cardAgentRunning(r *agentRec) bool {
	if r == nil || r.a.ShelvedAt != 0 || r.a.Desired != "running" || r.stopping {
		return false
	}
	switch r.a.State {
	case model.StateStarting, model.StateWorking, model.StateIdle, model.StateWaiting, model.StatePaused:
		return true
	}
	return false
}

func (f *Fleet) cardArchitectLocked() *agentRec {
	name := f.SupervisorName()
	var chosen *agentRec
	for _, r := range f.agents {
		matches := (name != "" && r.a.Name == name) || (name == "" && r.a.Level == model.LevelHead)
		if !matches {
			continue
		}
		// Ambiguity is never resolved by map order, even if one head is stopped.
		if chosen != nil {
			return nil
		}
		chosen = r
	}
	if chosen != nil && (chosen.a.Name == "tmx-lead" || !cardAgentRunning(chosen) || (chosen.a.ParentID != "" && chosen.a.Level != model.LevelHead)) {
		return nil
	}
	return chosen
}

package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

var cardRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// The caller holds f.mu throughout validation and the atomic store commit.
func (f *Fleet) decideOwnerCardLocked(ap model.Approval, d DecideRequest) (model.Approval, *model.Message, bool, error) {
	if d.AnswerID == "" {
		d.AnswerID = model.NewID()
	}
	if !cardRequestID.MatchString(d.AnswerID) {
		return ap, nil, false, errf(KindInvalid, "invalid answer_id")
	}
	request, _ := json.Marshal(d)
	if prev, result, messageID, err := f.st.CardAnswer(ap.ID, d.AnswerID); err == nil {
		if prev != string(request) {
			return ap, nil, false, errf(KindConflict, "answer_id already used for a different request")
		}
		// The receipt proves this operation committed, but a later operation may
		// have changed its card. Never resurrect old deferred/open UI state.
		current, err := f.st.GetApproval(result.ID)
		if err != nil {
			return ap, nil, false, err
		}
		result = current
		var mail *model.Message
		if messageID != "" {
			m, err := f.st.GetMessage(messageID)
			if err != nil {
				return ap, nil, false, err
			}
			mail = &m
		}
		return result, mail, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return ap, nil, false, err
	}
	// Late typed/dictated words are correspondence, never a new decision or
	// authorization. Bare choices and dismissals retain the stale-card guard.
	correspondence := ap.State != model.ApprovalPending && d.Decision == "answer" && d.FreeText && strings.TrimSpace(d.Answer) != ""
	if d.Version != ap.Version && !correspondence {
		return ap, nil, false, errf(KindConflict, "this question changed; read the current card before answering")
	}
	old := ap
	var body string
	switch d.Decision {
	case "defer":
		if ap.State != model.ApprovalPending {
			return ap, nil, false, errf(KindConflict, "this card was already resolved")
		}
		ap.Deferred = true
		ap.Version++ // invalidate any answer drafted before the deferral
	case "reask":
		if ap.State == model.ApprovalPending {
			if !ap.Deferred {
				return ap, nil, false, errf(KindConflict, "this question is already open")
			}
			ap.Deferred = false
			ap.Version++
		} else if ap.State == model.ApprovalAnswered || ap.State == model.ApprovalDenied {
			ap.ID, ap.Version, ap.State = model.NewID(), 1, model.ApprovalPending
			ap.Answer, ap.AnswerID, ap.DecidedAt, ap.CreatedAt, ap.Deferred = "", "", 0, model.Now(), false
			body = fmt.Sprintf("The owner asked again: %q. Wait for the new answer; do not act on the previous answer.", ap.Question)
		} else {
			return ap, nil, false, errf(KindConflict, "this question cannot be reopened")
		}
	case "answer", "allow", "deny":
		if correspondence {
			body = fmt.Sprintf("Correspondence after close (not a new decision), regarding version %d of your question %q: %s", d.Version, ap.Question, d.Answer)
			break
		}
		if ap.State != model.ApprovalPending {
			return ap, nil, false, errf(KindConflict, "this card was already resolved")
		}
		answer := d.Answer
		if answer == "" {
			answer = d.Message
		}
		if d.Decision != "deny" && strings.TrimSpace(answer) == "" {
			return ap, nil, false, errf(KindInvalid, "the answer is empty")
		}
		ap.State = model.ApprovalAnswered
		body = fmt.Sprintf("Answer to your question %q: %s", ap.Question, answer)
		if d.Decision == "deny" {
			ap.State = model.ApprovalDenied
			body = fmt.Sprintf("The owner declined to answer your question %q. %s", ap.Question, answer)
		}
		ap.Answer, ap.AnswerID, ap.DecidedAt, ap.Deferred = answer, d.AnswerID, model.Now(), false
	default:
		return ap, nil, false, errf(KindInvalid, "decision must be answer, deny, defer or reask")
	}
	var mail *model.Message
	if body != "" {
		if len(body) > MaxBodyBytes {
			return old, nil, false, errf(KindInvalid, "answer is too long")
		}
		to := f.projectCardAnswerRecipientLocked(ap)
		if to != ap.AgentID {
			body = fmt.Sprintf("Project card %s (original asker %s; routed because the asker is unavailable):\n%s", ap.ID, ap.AgentID, body)
			if len(body) > MaxBodyBytes {
				return old, nil, false, errf(KindInvalid, "answer is too long")
			}
		}
		mail = &model.Message{ID: model.NewID(), From: model.Owner, To: to, Body: body, State: model.MsgQueued, CreatedAt: model.Now(), ThreadID: f.liveThread(ap.ThreadID)}
		mail.Receipt, mail.ReceiptAt = "queued", mail.CreatedAt
	}
	if err := f.st.CommitCardAnswer(old, ap, d.AnswerID, string(request), mail, ownerPeerMapping(mail)...); err != nil {
		return old, nil, false, err
	}
	if mail != nil {
		if r := f.agents[mail.To]; r != nil {
			f.refreshQueuedLocked(r)
		}
	}
	event := model.EvApprovalCreated
	if ap.State != model.ApprovalPending {
		event = model.EvApprovalResolved
	}
	f.bus.Publish(model.BusEvent{Type: event, AgentID: ap.AgentID, Data: ap})
	return ap, mail, true, nil
}

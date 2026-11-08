package fleet

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// CompanionMailboxSend is the shipped desktop's mail protocol. Authentication is
// supplied by the API caller; process authentication is deliberately not inferred
// from a loopback address or a claimed from field.
func (f *Fleet) CompanionMailboxSend(c Caller, id, to, body, replyTo string, urgent bool) (model.Message, error) {
	if !c.Companion || c.Owner || c.AgentID != "" || c.Peer != "" {
		return model.Message{}, errf(KindForbidden, "only Companion uses its mailbox")
	}
	if id == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return model.Message{}, err
		}
		id = hex.EncodeToString(b[:])
	}
	if len(id) < 12 || len(id) > 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return model.Message{}, errf(KindInvalid, "id must be 12 to 32 lowercase hexadecimal characters")
	}
	if to == "" && replyTo == "" {
		return model.Message{}, errf(KindInvalid, "a recipient is required")
	}
	return f.companionSend(SendRequest{To: to, Body: body, ReplyTo: replyTo, RetryID: "mail:" + id}, id, urgent)
}

// CompanionMailbox returns only this principal's mail without consuming it.
func (f *Fleet) CompanionMailbox(c Caller, unread bool, limit, offset int) ([]model.Message, int, error) {
	if !c.Companion {
		return nil, 0, errf(KindForbidden, "only Companion uses its mailbox")
	}
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, errf(KindInvalid, "invalid mailbox page")
	}
	return f.st.CompanionInboxPage(unread, limit, offset)
}

func (f *Fleet) CompanionMailboxMessage(c Caller, id string, read bool) (model.Message, error) {
	if !c.Companion {
		return model.Message{}, errf(KindForbidden, "only Companion uses its mailbox")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.st.GetMessage(id)
	if err != nil || (m.From != model.CompanionParty && m.To != model.CompanionParty) {
		return model.Message{}, errf(KindNotFound, "no such Companion message")
	}
	if read {
		if m.To != model.CompanionParty {
			return model.Message{}, errf(KindForbidden, "only the recipient marks mail read")
		}
		m.State, m.Receipt, m.ReceiptAt = model.MsgRead, "read", model.Now()
		if err := f.st.SetMessageState(m.ID, m.State, 0); err != nil {
			return model.Message{}, err
		}
		if err := f.st.SetMessageReceipt(m.ID, m.Receipt, m.ReceiptAt); err != nil {
			return model.Message{}, err
		}
	}
	return m, nil
}

// A reply to desktop mail is durable inbox mail, not an outbound Companion task.
// It is restricted to the original recipient and preserves the correlation ID.
func (f *Fleet) replyCompanionMailbox(c Caller, req SendRequest, orig model.Message) (model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.callerRecLocked(c); err != nil {
		return model.Message{}, err
	}
	if c.Owner || c.AgentID == "" || orig.To != c.AgentID {
		return model.Message{}, errf(KindForbidden, "only the original recipient may reply")
	}
	if req.To != "" && req.To != model.CompanionParty {
		return model.Message{}, errf(KindInvalid, "reply must go to Companion")
	}
	m := model.Message{ID: model.NewID(), From: c.AgentID, To: model.CompanionParty, Body: req.Body, ReplyTo: orig.ID, CreatedAt: model.Now(), State: model.MsgDelivered, Receipt: "accepted"}
	m.DeliveredAt, m.ReceiptAt = m.CreatedAt, m.CreatedAt
	if err := f.checkLimitsLocked(m); err != nil {
		return model.Message{}, err
	}
	if err := f.st.PutMessage(m); err != nil {
		return model.Message{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: c.AgentID, Data: m})
	return m, nil
}

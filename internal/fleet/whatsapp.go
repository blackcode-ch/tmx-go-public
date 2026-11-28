package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// WhatsAppWatchTarget resolves a route without giving a remote recipient local
// WhatsApp tools or changing the existing read and send rules.
func (f *Fleet) WhatsAppWatchTarget(address string) (target string, remote bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	to, err := f.peerTargetLocked(address)
	if err != nil || IsPeer(to) {
		return to, IsPeer(to), err
	}
	r, err := f.resolveLocked(to)
	if err != nil {
		return "", false, err
	}
	return r.a.ID, false, nil
}

// QueueWhatsAppWatch makes the inbox or peer outbox entry the receipt. A crash
// after commit cannot turn the same WhatsApp line into another fleet message.
func (f *Fleet) QueueWhatsAppWatch(to, key, body string, urgent bool, seen ...string) error {
	return f.QueueWhatsAppWatchParts(to, key, []string{body}, urgent, seen...)
}

func (f *Fleet) QueueWhatsAppWatchParts(to, key string, bodies []string, urgent bool, seen ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.st.MessageByRetry(model.WhatsAppParty, key); !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_, host, remote := SplitPeer(to)
	r := f.agents[to]
	if !remote && r == nil {
		return errf(KindNotFound, "no agent %q", to)
	}
	var notices []model.Message
	var peers []store.PeerMessage
	for _, body := range bodies {
		m := model.Message{ID: model.NewID(), From: model.WhatsAppParty, To: to,
			Body: body, Urgent: urgent, State: model.MsgQueued, CreatedAt: model.Now()}
		if len(notices) == 0 {
			m.RetryID = key
		}
		if err := f.diskFloorLocked(m); err != nil {
			return err
		}
		if remote {
			if len(body) > MaxPeerBodyBytes {
				return errf(KindInvalid, "WhatsApp notice exceeds the fleet mail size")
			}
			m.Receipt, m.ReceiptAt = "queued", m.CreatedAt
			peers = append(peers, store.PeerMessage{LocalID: m.ID, Peer: host, WireID: newWireID(),
				Direction: store.PeerOut, SHA256: bodySHA(body), NextAttemptAt: m.CreatedAt})
		}
		notices = append(notices, m)
	}
	if err := f.st.PutWaWatchNotice(notices, peers, seen); err != nil {
		return err
	}
	if remote {
		select {
		case f.peer.wake <- struct{}{}:
		default:
		}
	} else {
		f.refreshQueuedLocked(r)
		f.goTracked(func() { f.tryDeliver(to) })
	}
	for _, m := range notices {
		f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: agentParty(m), Data: m})
	}
	return nil
}

// WhatsApp (DESIGN.md "WhatsApp", internal/whatsapp) touches the fleet in two ways: approvals of
// kind whatsapp (an agent's message waiting for the owner's Allow) and mail from the WhatsApp
// party (new messages in chats the owner chose, the outcome of an agent's message), already
// framed as outside data by the service.

// SetWhatsApp connects the WhatsApp service, which decides approvals of kind whatsapp.
func (f *Fleet) SetWhatsApp(d CompanionDecider) {
	f.mu.Lock()
	f.whatsapp = d
	f.mu.Unlock()
}

// WhatsAppApproval adds a pending Approval of kind whatsapp: the exact text an agent wants to
// send, and to whom. The agent carries on; the outcome reaches it as mail.
func (f *Fleet) WhatsAppApproval(agentID string, in model.WaSendInput) (model.Approval, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return model.Approval{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.agents[agentID]
	if !ok {
		return model.Approval{}, errf(KindNotFound, "no agent %q", agentID)
	}
	ap := model.Approval{ID: model.NewID(), AgentID: agentID, Kind: model.ApprovalWhatsApp, Tool: model.ToolWhatsAppSend, Input: b,
		Description: fmt.Sprintf("Send this WhatsApp message to %s", in.ChatName), Question: in.Text, Options: []string{},
		State: model.ApprovalPending, CreatedAt: model.Now()}
	if isHead(r) {
		ap.ThreadID = threadOr(r.thread)
	}
	if err := f.st.PutApproval(ap); err != nil {
		return model.Approval{}, err
	}
	f.Publish(model.BusEvent{Type: model.EvApprovalCreated, AgentID: agentID, ProjectID: r.a.ProjectID, Data: ap})
	return ap, nil
}

func (f *Fleet) decideWhatsApp(ap model.Approval, d DecideRequest) (model.Approval, error) {
	f.mu.Lock()
	dec := f.whatsapp
	f.mu.Unlock()
	if dec == nil {
		return model.Approval{}, errf(KindConflict, "WhatsApp is not set up in this daemon")
	}
	if d.Decision != "allow" && d.Decision != "deny" {
		return model.Approval{}, errf(KindInvalid, "decision must be allow or deny for a WhatsApp message")
	}
	if err := dec.Decide(ap, d.Decision, d.Answer, d.Message); err != nil {
		return model.Approval{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, err := f.st.GetApproval(ap.ID)
	if err != nil {
		return model.Approval{}, errf(KindNotFound, "no approval %q", ap.ID)
	}
	if cur.State != model.ApprovalPending {
		return cur, nil
	}
	state := model.ApprovalDenied
	if d.Decision == "allow" {
		state = model.ApprovalAllowed
	}
	f.finishApprovalLocked(&cur, state, d.Message)
	return cur, nil
}

// DeliverWhatsApp queues mail from the WhatsApp service for an agent (body already framed).
// The per-recipient queue limit applies.
func (f *Fleet) DeliverWhatsApp(agentID, body string) (model.Message, error) {
	if strings.TrimSpace(body) == "" {
		return model.Message{}, errf(KindInvalid, "the message is empty")
	}
	if len(body) > MaxBodyBytes {
		body = body[:MaxBodyBytes-40] + "\n[… cut by tmx]"
	}
	f.mu.Lock()
	r, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return model.Message{}, errf(KindNotFound, "no agent %q", agentID)
	}
	m := model.Message{ID: model.NewID(), From: model.WhatsAppParty, To: agentID, Body: body, State: model.MsgQueued, CreatedAt: model.Now()}
	if err := f.diskFloorLocked(m); err != nil {
		f.mu.Unlock()
		return model.Message{}, err
	}
	if err := f.st.PutMessage(m); err != nil {
		f.mu.Unlock()
		return model.Message{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: agentID, Data: m})
	f.refreshQueuedLocked(r)
	f.mu.Unlock()
	f.tryDeliver(agentID)
	return f.message(m.ID)
}

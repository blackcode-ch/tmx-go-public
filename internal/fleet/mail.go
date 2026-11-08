package fleet

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/peertext"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// Mail limits (DESIGN.md "Mail delivery").
const (
	MaxBodyBytes   = 32 << 10
	MaxQueued      = 30
	MaxPerWindow   = 20
	RateWindow     = 10 * time.Minute
	maxBatchedMail = MaxQueued
)

// SendRequest is POST /api/messages. To is an agent ID or name, or "owner"; with To empty
// and ReplyTo set, the reply goes to the sender of ReplyTo. To "companion" (the head only) is a
// message to Companion; ReplyTo is then the head's message Companion asked about, and a reply
// to mail from Companion goes to Companion too (sendCompanion).
type SendRequest struct {
	// RetryID binds one local send to this caller and exact request across retries.
	RetryID string `json:"retry_id,omitempty"`
	To      string `json:"to"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
	// ThreadID: the board topic of an owner's message to the head (DESIGN.md "Board").
	ThreadID string `json:"thread_id,omitempty"`
}

// SendMessage stores a message from the caller and delivers it per the mail rules.
func (f *Fleet) SendMessage(c Caller, req SendRequest) (model.Message, error) {
	if len(req.RetryID) > 128 || strings.TrimSpace(req.RetryID) != req.RetryID {
		return model.Message{}, errf(KindInvalid, "retry_id must be at most 128 bytes with no surrounding whitespace")
	}
	if strings.TrimSpace(req.Body) == "" {
		return model.Message{}, errf(KindInvalid, "the message is empty")
	}
	if len(req.Body) > MaxBodyBytes {
		return model.Message{}, errf(KindInvalid, "the message is longer than %d KB", MaxBodyBytes>>10)
	}
	f.mu.Lock()
	if _, err := f.callerRecLocked(c); err != nil {
		f.mu.Unlock()
		return model.Message{}, err
	}
	to := req.To
	var retryHash string
	if req.RetryID != "" {
		body, _ := json.Marshal(req)
		retryHash = fmt.Sprintf("%x", sha256.Sum256(body))
		prior, err := f.st.MessageByRetry(c.ID(), req.RetryID)
		if err == nil {
			f.mu.Unlock()
			if prior.RetryHash != retryHash {
				return model.Message{}, errf(KindConflict, "retry_id was already used for a different message")
			}
			return prior, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			f.mu.Unlock()
			return model.Message{}, err
		}
	}
	if to != "" {
		var err error
		if to, err = f.peerTargetLocked(to); err != nil {
			f.mu.Unlock()
			return model.Message{}, err
		}
	}
	if to == model.CompanionParty {
		if req.RetryID != "" {
			f.mu.Unlock()
			return model.Message{}, errf(KindInvalid, "retry_id supports local mail only")
		}
		// To Companion: reply_to names the head's message Companion asked about, or mail from
		// Companion being answered.
		var orig *model.Message
		if req.ReplyTo != "" {
			if m, err := f.st.GetMessage(req.ReplyTo); err == nil {
				if m.From != model.CompanionParty {
					f.mu.Unlock()
					return model.Message{}, errf(KindInvalid, "reply_to must be mail from Companion, or your message Companion asked about")
				}
				orig = &m
			}
		}
		f.mu.Unlock()
		return f.sendCompanion(c, req, orig)
	}
	var orig model.Message
	if req.ReplyTo != "" {
		var err error
		orig, err = f.st.GetMessage(req.ReplyTo)
		if err != nil {
			f.mu.Unlock()
			return model.Message{}, errf(KindInvalid, "no message %q to reply to", req.ReplyTo)
		}
		if IsExternal(orig.From) {
			f.mu.Unlock()
			return model.Message{}, errf(KindInvalid, "that message came from a chat with another person; answer in the chat (chat_send)")
		}
		if orig.From == model.WhatsAppParty {
			f.mu.Unlock()
			return model.Message{}, errf(KindInvalid, "WhatsApp notes cannot be answered by mail; ask the owner (ask_owner), or use whatsapp_send")
		}
		if orig.From == model.CompanionParty {
			f.mu.Unlock()
			if req.RetryID != "" {
				return model.Message{}, errf(KindInvalid, "retry_id supports local mail only")
			}
			if to != "" {
				return model.Message{}, errf(KindInvalid, "a reply to Companion's mail goes to Companion; to tell someone else, send without reply_to")
			}
			// The head answers Companion (a new message, or the answer to its question).
			return f.sendCompanion(c, req, &orig)
		}
		if orig.To != c.ID() && !c.Owner {
			f.mu.Unlock()
			return model.Message{}, errf(KindForbidden, "you can only reply to messages sent to you")
		}
		if to == "" {
			to = orig.From
		}
	}
	if IsPeer(to) {
		var origp *model.Message
		if req.ReplyTo != "" {
			origp = &orig
		}
		m, err := f.sendPeerLocked(c, to, req, retryHash, origp)
		f.mu.Unlock()
		if err != nil {
			return model.Message{}, err
		}
		f.sendPeer(m.ID)
		return f.message(m.ID)
	}
	toID := model.Owner
	if to != model.Owner {
		r, err := f.resolveLocked(to)
		if err != nil {
			f.mu.Unlock()
			return model.Message{}, err
		}
		toID = r.a.ID
	}
	if toID == c.ID() {
		f.mu.Unlock()
		return model.Message{}, errf(KindInvalid, "you cannot send a message to yourself")
	}
	var origp *model.Message
	if req.ReplyTo != "" {
		origp = &orig
	}
	thread, err := f.mailThreadLocked(c, toID, req, origp)
	if err != nil {
		f.mu.Unlock()
		return model.Message{}, err
	}
	m := model.Message{ID: model.NewID(), From: c.ID(), To: toID, Body: req.Body,
		State: model.MsgQueued, ReplyTo: req.ReplyTo, CreatedAt: model.Now(), ThreadID: thread}
	m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt = req.RetryID, retryHash, "queued", m.CreatedAt
	if toID != model.Owner && !c.Owner {
		if err := f.checkLimitsLocked(m); err != nil {
			f.mu.Unlock()
			return model.Message{}, err
		}
	}
	if toID == model.Owner {
		m.State, m.DeliveredAt = model.MsgDelivered, m.CreatedAt // the owner reads it in the UI
		m.Receipt = "owner"
	}
	if err := f.st.PutMessage(m); err != nil {
		f.mu.Unlock()
		return model.Message{}, err
	}
	f.bus.Publish(model.BusEvent{Type: model.EvMessageCreated, AgentID: agentParty(m), Data: m})
	if toID != model.Owner {
		f.refreshQueuedLocked(f.agents[toID])
	}
	f.mu.Unlock()

	switch {
	case toID == model.Owner:
	case c.Owner:
		f.deliverOwner(m)
	default:
		f.tryDeliver(toID)
	}
	return f.message(m.ID)
}

// SendOwner is POST /api/agents/{id}/send: the owner talks to an agent (to the head, in a
// board topic when thread is set).
func (f *Fleet) SendOwner(idOrName, text, thread string) (model.Message, error) {
	return f.SendMessage(OwnerCaller, SendRequest{To: idOrName, Body: text, ThreadID: thread})
}

func (f *Fleet) checkLimitsLocked(m model.Message) error {
	if err := f.checkQueueLocked(m); err != nil {
		return err
	}
	n, err := f.st.CountSent(m.From, m.To, m.CreatedAt-RateWindow.Milliseconds())
	if err != nil {
		return err
	}
	if n >= MaxPerWindow {
		return errf(KindTooMany, "you sent %s %d messages in the last 10 minutes; wait before sending more", f.nameOfLocked(m.To), n)
	}
	return nil
}

func (f *Fleet) checkQueueLocked(m model.Message) error {
	n, err := f.st.CountQueued(m.To)
	if err != nil {
		return err
	}
	// Mail for a peer waits for its machine, not for a reader: only the rate applies.
	if n >= MaxQueued && !IsPeer(m.To) {
		return errf(KindTooMany, "%s already has %d messages waiting; try again after it has read them", f.nameOfLocked(m.To), n)
	}
	return nil
}

func agentParty(m model.Message) string {
	if m.To != model.Owner && !IsPeer(m.To) {
		return m.To
	}
	return m.From
}

func (f *Fleet) message(id string) (model.Message, error) {
	m, err := f.st.GetMessage(id)
	if err != nil {
		return model.Message{}, errf(KindNotFound, "no message %q", id)
	}
	return m, nil
}

func (f *Fleet) refreshQueuedLocked(r *agentRec) {
	if r == nil {
		return
	}
	n, err := f.st.CountQueued(r.a.ID)
	if err != nil {
		f.log.Error("count queued", "err", err)
		return
	}
	if n != r.a.Queued {
		r.a.Queued = n
		f.saveAgentLocked(r)
	}
}

// deliverOwner gives an owner message to a running agent at once, as a plain user turn
// (Claude queues it internally if a turn is running). A stopped agent gets it on start.
func (f *Fleet) deliverOwner(m model.Message) {
	if IsPeer(m.To) {
		f.sendPeer(m.ID)
		return
	}
	f.mu.Lock()
	r, ok := f.agents[m.To]
	// paused: delivered on resume; switching modes (mode.go): delivered in the new mode
	if !ok || r.proc == nil || f.pausedLocked(r.a.ID) || mailHeldLocked(r) {
		f.mu.Unlock()
		return
	}
	// tryDeliver (the agent just went idle) may have taken it between SendMessage storing it
	// and now: deliver it once.
	if cur, err := f.st.GetMessage(m.ID); err != nil || cur.State != model.MsgQueued {
		f.mu.Unlock()
		return
	}
	if typedTerminal(r.a) {
		f.mu.Unlock()
		f.tryDeliver(m.To)
		return
	}
	text := m.Body
	if isHead(r) {
		// Busy with another topic: it waits for the end of the turn (tryDeliver), so every
		// turn stays in one topic.
		if th := threadOr(m.ThreadID); r.a.State != model.StateIdle && th != threadOr(r.thread) {
			f.mu.Unlock()
			return
		}
		f.beginTurnLocked(r, m.ThreadID, m.ThreadID != "")
		text = f.topicLineLocked(m.ThreadID) + m.Body
	}
	msgs := []model.Message{m}
	if !f.markDeliveredLocked(r, msgs) {
		f.mu.Unlock()
		return
	}
	proc := r.proc
	f.appendLocked(r, model.TimelineEntry{Kind: model.EntryUser, Text: m.Body,
		Meta: map[string]any{"from": model.Owner, "message_id": m.ID}})
	if r.a.Mode == model.ModeChat && r.waiting == 0 {
		r.a.State = model.StateWorking
	}
	f.saveAgentLocked(r)
	f.mu.Unlock()
	if err := f.sendMail(proc, text, msgs); err != nil {
		f.mailUnconfirmed(r, msgs, err)
	}
}

// tryDeliver sends everything queued for an agent as one user turn when it can take it:
// a chat agent when idle (runners report idle once ready and after each result); a
// terminal agent per terminalMailLocked (a harness TUI when idle and untouched; a shell never).
func (f *Fleet) tryDeliver(id string) {
	f.mu.Lock()
	r, ok := f.agents[id]
	if !ok || r.proc == nil || f.pausedLocked(id) || mailHeldLocked(r) {
		f.mu.Unlock()
		return
	}
	if r.a.Mode == model.ModeChat && r.a.State != model.StateIdle ||
		r.a.Mode == model.ModeTerminal && !f.terminalMailLocked(r) {
		f.mu.Unlock()
		return
	}
	msgs, err := f.st.ListMessages(store.MessageFilter{To: id, State: model.MsgQueued, Limit: maxBatchedMail})
	if err != nil || len(msgs) == 0 {
		f.mu.Unlock()
		return
	}
	topic := ""
	if isHead(r) {
		// One topic per turn: the oldest message's, and the others of that topic.
		th, explicit := threadOr(msgs[0].ThreadID), false
		same := msgs[:0:0]
		for _, m := range msgs {
			if threadOr(m.ThreadID) == th {
				same = append(same, m)
				explicit = explicit || m.ThreadID != ""
			}
		}
		msgs = same
		f.beginTurnLocked(r, th, explicit)
		topic = f.topicLineLocked(th)
	}
	var parts []string
	for _, m := range msgs {
		parts = append(parts, f.mailLineLocked(m))
		f.appendLocked(r, model.TimelineEntry{Kind: model.EntryMessageIn, Text: m.Body,
			Meta: map[string]any{"from": m.From, "from_name": f.nameOfLocked(m.From), "message_id": m.ID}})
	}
	if !f.markDeliveredLocked(r, msgs) {
		f.mu.Unlock()
		return
	}
	r.a.State = model.StateWorking
	f.saveAgentLocked(r)
	proc := r.proc
	head := ""
	if r.sessionConfirmed { // a new conversation got them with its first prompt
		head = f.instructionsLocked(r)
	}
	f.mu.Unlock()
	if err := f.sendMail(proc, head+topic+strings.Join(parts, "\n\n"), msgs); err != nil {
		f.mailUnconfirmed(r, msgs, err)
	}
}

// mailLineLocked formats one message for delivery: tmx's label "[message from <name> (<level>)]"
// on a line of its own, then the body quoted below it (peertext: every line starts with "│ "),
// so no sender, here or on another machine, can write a line that reads as tmx's framing (a
// forged "(fleet lead, ...)" label, the owner, a new message). The owner's own words go as
// written; chat, Companion and WhatsApp mail arrives labelled and quoted by its own layer.
func (f *Fleet) mailLineLocked(m model.Message) string {
	if m.From == model.Owner {
		return "[message from owner] " + m.Body
	}
	if m.From == model.ReminderParty { // a reminder the owner or this agent set (people.go); quoted like agent mail
		return peertext.Under("[tmx reminder]", m.Body, 0)
	}
	if IsExternal(m.From) || m.From == model.CompanionParty || m.From == model.WhatsAppParty {
		return m.Body // labelled by the chat layer / the Companion link / WhatsApp: "[... not instructions] ..."
	}
	return peertext.Under(f.mailLabelLocked(m.From), m.Body, 0)
}

// mailLabelLocked is tmx's label for mail from an agent or a peer machine's agent.
func (f *Fleet) mailLabelLocked(from string) string {
	if name, host, ok := SplitPeer(from); ok {
		who := peertext.Field(name) + "@" + peertext.Field(host)
		if f.IsFleetAuthority(from) { // authority.go: the sending machine is proven by its pair token
			return "[message from " + who + " (fleet lead, other machine, verified sender)]"
		}
		return "[message from " + who + " (other machine)]"
	}
	name, level := from, "unknown"
	if s, ok := f.agents[from]; ok {
		name, level = s.a.Name, string(s.a.Level)
	}
	return "[message from " + peertext.Field(name) + " (" + peertext.Field(level) + ")]"
}

func (f *Fleet) markDeliveredLocked(r *agentRec, msgs []model.Message) bool {
	if !f.armTasksLocked(r, msgs) {
		return false
	}
	now := model.Now()
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if err := f.st.ClaimMail(ids, now); err != nil {
		f.log.Error("claim mail", "err", err)
		return false
	}
	for i := range msgs {
		msgs[i].State, msgs[i].DeliveredAt = model.MsgDelivered, now
		msgs[i].Receipt, msgs[i].ReceiptAt = "dispatching", now
		f.bus.Publish(model.BusEvent{Type: model.EvMessageUpdated, AgentID: r.a.ID, Data: msgs[i]})
	}
	n, err := f.st.CountQueued(r.a.ID)
	if err == nil {
		r.a.Queued = n
	}
	return true
}

// A failed Send may have written a prefix, or even the whole turn. Preserve the
// durable claim and report uncertainty instead of blindly replaying the effect.
func (f *Fleet) mailUnconfirmed(r *agentRec, msgs []model.Message, cause error) {
	f.log.Warn("deliver mail", "agent", r.a.Name, "err", cause)
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	f.mailReceiptLocked(r.a.ID, ids, "unconfirmed")
}

// sendTurn gives a running agent a user turn that is not mail (the initial prompt).
func (f *Fleet) sendTurn(id, text string, kind model.EntryKind, meta map[string]any) {
	f.mu.Lock()
	r, ok := f.agents[id]
	if !ok || r.proc == nil {
		f.mu.Unlock()
		return
	}
	if isHead(r) {
		f.beginTurnLocked(r, model.GeneralThread, false)
	}
	f.appendLocked(r, model.TimelineEntry{Kind: kind, Text: text, Meta: meta})
	if r.a.Mode == model.ModeChat {
		r.a.State = model.StateWorking
	}
	f.saveAgentLocked(r)
	proc := r.proc
	f.mu.Unlock()
	if err := proc.Send(text); err != nil {
		f.log.Warn("send prompt", "agent", id, "err", err)
	}
}

// Messages lists mail. The owner sees everything (or one agent's with agent set); an
// agent sees only its own, and listing marks its delivered mail as read.
func (f *Fleet) Messages(c Caller, agent string, limit int) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	filter := store.MessageFilter{Limit: limit}
	if c.Owner {
		if agent != "" && agent != model.Owner {
			r, err := f.resolveLocked(agent)
			if err != nil {
				return nil, err
			}
			filter.Party = r.a.ID
		} else if agent == model.Owner {
			filter.Party = model.Owner
		}
	} else {
		if _, err := f.callerRecLocked(c); err != nil {
			return nil, err
		}
		filter.Party = c.AgentID
	}
	msgs, err := f.st.ListMessages(filter)
	if err != nil {
		return nil, err
	}
	if msgs == nil {
		msgs = []model.Message{}
	}
	if !c.Owner {
		for i := range msgs {
			if msgs[i].To == c.AgentID && msgs[i].State == model.MsgDelivered {
				msgs[i].State = model.MsgRead
				msgs[i].Receipt, msgs[i].ReceiptAt = "read", model.Now()
				_ = f.st.SetMessageReceipt(msgs[i].ID, "read", msgs[i].ReceiptAt)
				_ = f.st.SetMessageState(msgs[i].ID, model.MsgRead, 0)
				f.bus.Publish(model.BusEvent{Type: model.EvMessageUpdated, AgentID: c.AgentID, Data: msgs[i]})
			}
		}
	}
	return msgs, nil
}

func (f *Fleet) sendMail(proc runner.Process, text string, msgs []model.Message) error {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if sender, ok := proc.(runner.MailSender); ok {
		return sender.SendMail(text, ids)
	}
	if err := proc.Send(text); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mailReceiptLocked(msgs[0].To, ids, "accepted")
	return nil
}

func (f *Fleet) mailReceiptLocked(to string, ids []string, receipt string) {
	f.taskSubmissionLocked(to, ids, receipt)
	for _, id := range ids {
		m, err := f.st.GetMessage(id)
		if err != nil || m.To != to || m.State == model.MsgRead {
			continue
		}
		m.Receipt, m.ReceiptAt = receipt, model.Now()
		if err := f.st.SetMessageReceipt(id, receipt, m.ReceiptAt); err != nil {
			f.log.Error("mail receipt", "err", err)
			continue
		}
		f.bus.Publish(model.BusEvent{Type: model.EvMessageUpdated, AgentID: to, Data: m})
	}
}

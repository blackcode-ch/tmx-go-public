package fleet

import (
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// External mail: messages from outside this machine (chats with other people's tmx, see
// internal/chat and DESIGN.md "Chats") reach a local agent as ordinary mail whose sender is
// "chat:<chat id>" and whose body is already labelled as external. It is delivered like
// agent mail (when the agent is idle) and never carries any right: the agent reads it as a
// user turn and every tool call still goes through approvals and the guard.

// ExternalPrefix starts the From of external mail.
const ExternalPrefix = "chat:"

// IsExternal reports whether a mail party is a chat (another person's install).
func IsExternal(id string) bool { return strings.HasPrefix(id, ExternalPrefix) }

// HeadID returns the head agent's ID ("" when there is none).
func (f *Fleet) HeadID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.headLocked(); h != nil {
		return h.a.ID
	}
	return ""
}

// DeliverExternal queues labelled text from a chat for a local agent and delivers it per
// the mail rules. from is "chat:<chat id>". The per-recipient queue limit applies (the chat
// keeps the message either way; the agent can read it with chat_read).
func (f *Fleet) DeliverExternal(agentID, from, text string) (model.Message, error) {
	if !IsExternal(from) {
		return model.Message{}, errf(KindInvalid, "external mail must come from a chat")
	}
	if len(text) > MaxBodyBytes+1024 {
		return model.Message{}, errf(KindInvalid, "the message is too long")
	}
	f.mu.Lock()
	r, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return model.Message{}, errf(KindNotFound, "no agent %q", agentID)
	}
	n, err := f.st.CountQueued(agentID)
	if err != nil {
		f.mu.Unlock()
		return model.Message{}, err
	}
	if n >= MaxQueued {
		f.mu.Unlock()
		return model.Message{}, errf(KindTooMany, "%s already has %d messages waiting", r.a.Name, n)
	}
	m := model.Message{ID: model.NewID(), From: from, To: agentID, Body: text, State: model.MsgQueued, CreatedAt: model.Now()}
	if isHead(r) {
		m.ThreadID = f.chatThreadLocked(from) // each chat is its own string on the Board
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

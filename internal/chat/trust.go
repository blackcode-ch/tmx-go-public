package chat

import "github.com/blackcode-ch/tmx-go/internal/model"

// Group members talk to each other directly (DESIGN.md "Chats", Groups). A member paired
// only with the admin (through the admin's invite), so the node accepts the other members
// only once the chat layer trusts them: on every newer group state from the admin, each
// member calls Node.Trust for the other current members (with the addresses the admin
// passed on), and Node.Forget for peers it no longer shares anything with. A peer stays
// known to the node while it is a contact or a current member of a group we are in
// (active or pending); the set is recomputed from the store, so nothing extra is kept.

// addrSource is implemented by nodes that can tell where they last saw a peer
// ((*p2p.Host).PeerAddrs); the admin passes those addresses on in the group state.
type addrSource interface {
	PeerAddrs(peerID string) []string
}

// memberAddrsLocked: addresses to hand the other members for peer (ours for ourselves).
func (s *Service) memberAddrsLocked(peer string) []string {
	var addrs []string
	if peer == s.selfLocked() {
		addrs = s.node.Status().Addrs
	} else if src, ok := s.node.(addrSource); ok {
		addrs = src.PeerAddrs(peer)
	}
	if len(addrs) > maxMemberAddrs {
		addrs = addrs[:maxMemberAddrs]
	}
	return addrs
}

// trustMembersLocked makes the node accept (and reach) every other current member of a
// group. Contacts are paired already and are left alone.
func (s *Service) trustMembersLocked(chatID string, addrs map[string][]string) {
	self := s.selfLocked()
	for _, m := range s.currentMembersLocked(chatID) {
		if m.PeerID == self {
			continue
		}
		if _, err := s.st.GetContact(m.PeerID); err == nil {
			continue
		}
		if err := s.node.Trust(m.PeerID, m.Name, addrs[m.PeerID]); err != nil {
			s.log.Info("chat: trust group member", "peer", m.PeerID, "chat", chatID, "err", err)
		}
	}
}

// neededLocked reports whether the node must keep accepting peer: a contact, or a current
// member of a group we are (still or about to be) in.
func (s *Service) neededLocked(peer string) bool {
	if _, err := s.st.GetContact(peer); err == nil {
		return true
	}
	return s.sharesGroupLocked(peer)
}

func (s *Service) sharesGroupLocked(peer string) bool {
	chats, err := s.st.ListChats()
	if err != nil {
		return true // unsure: keep it
	}
	for _, c := range chats {
		if c.Kind != model.ChatGroup || (c.Status != model.ChatActive && c.Status != model.ChatPending) {
			continue
		}
		if _, ok := s.memberLocked(c.ID, peer); ok {
			return true
		}
	}
	return false
}

// untrustLocked makes the node forget each of peers that we no longer share anything with.
func (s *Service) untrustLocked(peers []string) {
	self := s.selfLocked()
	for _, p := range peers {
		if p == "" || p == self || s.neededLocked(p) {
			continue
		}
		s.node.Forget(p)
	}
}

// retrustLocked (at start) trusts the co-members of every group we are in, in case the
// node lost them; the node keeps trusted peers itself, so this normally changes nothing.
func (s *Service) retrustLocked() {
	chats, err := s.st.ListChats()
	if err != nil {
		return
	}
	for _, c := range chats {
		if c.Kind == model.ChatGroup && c.Status == model.ChatActive {
			s.trustMembersLocked(c.ID, nil)
		}
	}
}

// dropQueuedLocked removes what waits in a chat's outbox for peer ("" = everyone). Our
// messages among it wait for one receipt fewer (the peer left the group), or fail when
// fail is set (we are out of the group).
func (s *Service) dropQueuedLocked(chatID, peer string, fail bool) {
	items, err := s.st.TakeChatOutbox(chatID, peer)
	if err != nil {
		s.log.Error("chat: outbox", "err", err)
		return
	}
	for _, it := range items {
		if it.MessageID == "" {
			continue
		}
		m, err := s.st.GetChatMessage(it.ChatID, it.MessageID)
		if err != nil || m.State == model.ChatFailed {
			continue
		}
		if fail {
			if m.State != model.ChatDelivered {
				_ = s.st.SetChatMessageState(it.ChatID, it.MessageID, model.ChatFailed)
				m.State = model.ChatFailed
				s.publish(model.EvChatMessage, m)
			}
			continue
		}
		if m.Recipients > 0 {
			_ = s.st.SetChatMessageRecipients(it.ChatID, it.MessageID, m.Recipients-1)
		}
		s.refreshStateLocked(it.ChatID, it.MessageID)
	}
}

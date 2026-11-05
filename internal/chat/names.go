package chat

import (
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// NameChanged tells everyone who knows this install (contacts and the members of its
// groups) that its display name changed, and updates our own member rows. Call it after
// the name is set; without a node it does nothing.
func (s *Service) NameChanged() {
	if !s.Enabled() {
		return
	}
	name := cleanName(s.myName())
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.unlock(false)
	self := s.selfLocked()
	seen := map[string]bool{self: true}
	var peers []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			peers = append(peers, p)
		}
	}
	if cts, err := s.st.ListContacts(); err == nil {
		for _, ct := range cts {
			add(ct.PeerID)
		}
	}
	chats, _ := s.st.ListChats()
	for _, c := range chats {
		if c.Status != model.ChatActive {
			continue
		}
		if m, ok := s.memberLocked(c.ID, self); ok && m.Name != name {
			m.Name = name
			_ = s.st.PutChatMember(m)
			s.publishChatLocked(c.ID)
		}
		for _, p := range s.othersLocked(c.ID) {
			add(p)
		}
	}
	env, err := envelope(model.NewID(), TypeName, "", nameBody{V: WireVersion, Name: name})
	if err != nil {
		return
	}
	s.sendLocked(peers, env, "")
}

// onName applies a peer's new display name: to its contact (unless we renamed it here) and
// to its member rows in our chats (unless it is a contact: our own name for it wins, as in
// applyGroup). Only a contact or a current member of one of our chats is believed.
func (s *Service) onName(env p2p.Envelope) error {
	var b nameBody
	if err := decodeBody(env.Body, &b); err != nil {
		return err
	}
	name := cleanName(b.Name)
	if name == "" {
		return errors.New("empty name")
	}
	s.mu.Lock()
	defer s.unlock(false)
	known := false
	if _, err := s.st.GetContact(env.From); err == nil {
		known = true
		if _, err := s.upsertContactLocked(env.From, name, ""); err != nil {
			return err
		}
	}
	shown := name
	if ct, err := s.st.GetContact(env.From); err == nil {
		shown = ct.Name
	}
	chats, _ := s.st.ListChats()
	for _, c := range chats {
		m, ok := s.memberLocked(c.ID, env.From)
		if !ok {
			continue
		}
		known = true
		if m.Name != shown {
			m.Name = shown
			_ = s.st.PutChatMember(m)
			s.publishChatLocked(c.ID)
		}
	}
	if !known {
		return errors.New("name from a stranger")
	}
	return nil
}

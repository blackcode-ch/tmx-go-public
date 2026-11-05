package chat

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// ---- invites (proxied to the node)

// InviteRequest is POST /api/invites.
type InviteRequest struct {
	Kind    p2p.InviteKind `json:"kind"`               // dm | group
	GroupID string         `json:"group_id,omitempty"` // group invites: the chat ID
	TTLMs   int64          `json:"ttl_ms,omitempty"`   // 0 = default (24 hours)
}

func (s *Service) CreateInvite(req InviteRequest) (p2p.Invite, error) {
	if !s.Enabled() {
		return p2p.Invite{}, ErrOff
	}
	opts := p2p.InviteOptions{Kind: req.Kind, TTLMillis: req.TTLMs}
	switch req.Kind {
	case p2p.InviteDM:
	case p2p.InviteGroup:
		s.mu.Lock()
		c, err := s.st.GetChat(req.GroupID)
		if err != nil || c.Kind != model.ChatGroup {
			s.mu.Unlock()
			return p2p.Invite{}, notFound("no group %q", req.GroupID)
		}
		if c.Status != model.ChatActive || !s.isAdminLocked(c.ID, s.selfLocked()) {
			s.mu.Unlock()
			return p2p.Invite{}, forbidden("only the group's admin can invite people")
		}
		n := len(s.currentMembersLocked(c.ID))
		s.mu.Unlock()
		if n >= MaxGroupMembers {
			return p2p.Invite{}, invalid("the group already has %d members (the most a group can have)", n)
		}
		opts.GroupID, opts.GroupName = c.ID, c.Name
	default:
		return p2p.Invite{}, invalid("an invite is for a dm or a group")
	}
	inv, err := s.node.CreateInvite(opts)
	if err != nil {
		return p2p.Invite{}, invalid("%s", err.Error())
	}
	return inv, nil
}

func (s *Service) Invites() ([]p2p.Invite, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	out := s.node.Invites()
	if out == nil {
		out = []p2p.Invite{}
	}
	return out, nil
}

func (s *Service) RevokeInvite(id string) error {
	if !s.Enabled() {
		return ErrOff
	}
	if err := s.node.RevokeInvite(id); err != nil {
		return notFound("%s", err.Error())
	}
	return nil
}

func (s *Service) Preview(link string) (p2p.InvitePreview, error) {
	if !s.Enabled() {
		return p2p.InvitePreview{}, ErrOff
	}
	pv, err := s.node.Preview(strings.TrimSpace(link))
	if err != nil {
		return p2p.InvitePreview{}, invalid("%s", err.Error())
	}
	return pv, nil
}

// JoinResult is POST /api/invites/join.
type JoinResult struct {
	Chat    model.Chat    `json:"chat"`
	Contact model.Contact `json:"contact"`
}

// Join pairs with the inviter of link and lands in the DM or the group (pending until the
// admin's member list arrives).
func (s *Service) Join(ctx context.Context, link string) (JoinResult, error) {
	if !s.Enabled() {
		return JoinResult{}, ErrOff
	}
	link = strings.TrimSpace(link)
	pv, err := s.node.Preview(link)
	if err != nil {
		return JoinResult{}, invalid("%s", err.Error())
	}
	// A group's state may arrive before Join returns: prepare the pending chat first, so
	// the admin's envelope finds it (and roll back if the pairing fails).
	created := false
	if pv.Kind == p2p.InviteGroup {
		s.mu.Lock()
		created, err = s.prepareGroupLocked(pv.GroupID, pv.GroupName, pv.PeerID, pv.Name)
		s.mu.Unlock()
		if err != nil {
			return JoinResult{}, err
		}
	}
	p, err := s.node.Join(ctx, link)
	if err != nil {
		if created {
			s.mu.Lock()
			_ = s.st.DeleteChat(pv.GroupID)
			s.mu.Unlock()
		}
		if errors.Is(err, p2p.ErrUnreachable) {
			return JoinResult{}, offlineError{"could not reach " + pv.Name + "'s tmx right now; try again when they are online"}
		}
		return JoinResult{}, invalid("%s", err.Error())
	}
	if err := s.onPaired(p, true); err != nil {
		return JoinResult{}, err
	}
	var res JoinResult
	chatID := p.GroupID
	if p.Kind == p2p.InviteDM {
		chatID = DMChatID(s.self(), p.PeerID)
	}
	if res.Chat, err = s.Chat(chatID); err != nil {
		return JoinResult{}, err
	}
	if res.Contact, err = s.contact(p.PeerID); err != nil {
		return JoinResult{}, err
	}
	return res, nil
}

// prepareGroupLocked creates (or reopens) a group we are joining, as pending, with the
// inviter as its admin. It reports whether the chat is new. The ID is the inviter's word and
// names the group's folder of files, whose paths agents read in their mail: tmx makes it from
// letters and digits ("g" + model.NewID), so anything else is refused.
func (s *Service) prepareGroupLocked(id, name, adminPeer, adminName string) (bool, error) {
	if id == "" {
		return false, invalid("the invite names no group")
	}
	if !validFileID(id) {
		return false, invalid("the invite names no valid group")
	}
	now := model.Now()
	c, err := s.st.GetChat(id)
	created := errors.Is(err, store.ErrNotFound)
	switch {
	case created:
		c = model.Chat{ID: id, Kind: model.ChatGroup, Name: name, CreatedBy: adminPeer, AgentID: s.ag.HeadID(),
			Status: model.ChatPending, CreatedAt: now}
	case err != nil:
		return false, err
	case c.Kind != model.ChatGroup:
		return false, invalid("the invite's group clashes with another chat")
	case c.Status == model.ChatActive:
		return false, nil // already a member
	default:
		c.Status, c.Version = model.ChatPending, 0
	}
	c.UpdatedAt = now
	if err := s.st.PutChat(c); err != nil {
		return false, err
	}
	if err := s.st.PutChatMember(model.ChatMember{ChatID: id, PeerID: adminPeer, Name: adminName, Role: model.RoleAdmin, JoinedAt: now}); err != nil {
		return false, err
	}
	if err := s.st.PutChatMember(model.ChatMember{ChatID: id, PeerID: s.selfLocked(), Name: s.myName(), Role: model.RoleMember, JoinedAt: now}); err != nil {
		return false, err
	}
	return created, nil
}

// onPaired handles a completed pairing on either side (joiner: from Join; inviter: from
// Node.Paired).
func (s *Service) onPaired(p p2p.Paired, joiner bool) error {
	s.mu.Lock()
	defer s.unlock(false)
	ct, err := s.upsertContactLocked(p.PeerID, p.Name, p.SafetyCode)
	if err != nil {
		return err
	}
	switch p.Kind {
	case p2p.InviteDM:
		_, err := s.ensureDMLocked(ct, joiner)
		return err
	case p2p.InviteGroup:
		if joiner {
			if _, err := s.prepareGroupLocked(p.GroupID, p.GroupName, p.PeerID, ct.Name); err != nil {
				return err
			}
			s.publishChatLocked(p.GroupID)
			return nil
		}
		return s.admitLocked(p.GroupID, ct)
	}
	return nil
}

func (s *Service) upsertContactLocked(peer, remoteName, safety string) (model.Contact, error) {
	now := model.Now()
	remoteName = cleanName(remoteName)
	c, err := s.st.GetContact(peer)
	if errors.Is(err, store.ErrNotFound) {
		name := remoteName
		if name == "" {
			name = "Someone"
		}
		c = model.Contact{PeerID: peer, Name: name, RemoteName: remoteName, SafetyCode: safety, AddedAt: now}
	} else if err != nil {
		return c, err
	} else {
		if remoteName != "" {
			if c.Name == c.RemoteName || c.Name == "Someone" { // not renamed here: follow theirs
				c.Name = remoteName
			}
			c.RemoteName = remoteName
		}
		if safety != "" {
			c.SafetyCode = safety
		}
	}
	c.UpdatedAt = now
	if err := s.st.PutContact(c); err != nil {
		return c, err
	}
	c.Reachability = string(s.node.Reachable(peer))
	s.publish(model.EvContactUpdated, c)
	return c, nil
}

// ensureDMLocked creates (or reopens) the DM with a contact.
func (s *Service) ensureDMLocked(ct model.Contact, joiner bool) (model.Chat, error) {
	id := DMChatID(s.selfLocked(), ct.PeerID)
	now := model.Now()
	c, err := s.st.GetChat(id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		by := model.ChatMe
		if joiner {
			by = ct.PeerID
		}
		c = model.Chat{ID: id, Kind: model.ChatDM, Name: ct.Name, PeerID: ct.PeerID, CreatedBy: by,
			AgentID: s.ag.HeadID(), Status: model.ChatActive, CreatedAt: now, UpdatedAt: now}
		if err := s.st.PutChat(c); err != nil {
			return c, err
		}
		s.systemLocked(id, "You're connected with "+ct.Name+". Compare the safety code in their contact details to be sure it's them.")
	case err != nil:
		return c, err
	default:
		c.Name, c.UpdatedAt = ct.Name, now
		if c.Status != model.ChatActive {
			c.Status = model.ChatActive
			if err := s.st.PutChat(c); err != nil {
				return c, err
			}
			s.systemLocked(id, "You're connected with "+ct.Name+" again.")
		} else if err := s.st.PutChat(c); err != nil {
			return c, err
		}
	}
	s.publishChatLocked(id)
	return c, nil
}

// ---- contacts

func (s *Service) Contacts() ([]model.Contact, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	list, err := s.st.ListContacts()
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Reachability = string(s.node.Reachable(list[i].PeerID))
	}
	return list, nil
}

func (s *Service) contact(peer string) (model.Contact, error) {
	c, err := s.st.GetContact(peer)
	if errors.Is(err, store.ErrNotFound) {
		return c, notFound("no contact %q", peer)
	}
	if err == nil {
		c.Reachability = string(s.node.Reachable(peer))
	}
	return c, err
}

// ContactPatch is PATCH /api/contacts/{peer}.
type ContactPatch struct {
	Name *string `json:"name,omitempty"`
	Note *string `json:"note,omitempty"`
}

func (s *Service) UpdateContact(peer string, p ContactPatch) (model.Contact, error) {
	if !s.Enabled() {
		return model.Contact{}, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.contact(peer)
	if err != nil {
		return c, err
	}
	if p.Name != nil {
		n := cleanName(*p.Name)
		if n == "" {
			n = c.RemoteName
		}
		if n == "" {
			return c, invalid("the name is empty")
		}
		c.Name = n
	}
	if p.Note != nil {
		if len(*p.Note) > 2000 {
			return c, invalid("the note is longer than 2000 characters")
		}
		c.Note = strings.TrimSpace(*p.Note)
	}
	c.UpdatedAt = model.Now()
	if err := s.st.PutContact(c); err != nil {
		return c, err
	}
	s.publish(model.EvContactUpdated, c)
	if dm, err := s.st.GetChat(DMChatID(s.selfLocked(), peer)); err == nil && dm.Name != c.Name {
		dm.Name, dm.UpdatedAt = c.Name, model.Now()
		_ = s.st.PutChat(dm)
		s.publishChatLocked(dm.ID)
	}
	return c, nil
}

// RemoveContact forgets a peer: the node accepts nothing more from it (unless it is still
// a member of a group we are in: then only until one of us leaves it), envelopes of the DM
// queued for it fail, and the DM stays as read-only history (status removed).
func (s *Service) RemoveContact(peer string) error {
	if !s.Enabled() {
		return ErrOff
	}
	s.mu.Lock()
	if _, err := s.contact(peer); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.st.DeleteContact(peer); err != nil {
		s.mu.Unlock()
		return err
	}
	shared := s.sharesGroupLocked(peer)
	if !shared {
		s.node.Forget(peer)
	}
	dmID := DMChatID(s.selfLocked(), peer)
	if dm, err := s.st.GetChat(dmID); err == nil && dm.Status == model.ChatActive {
		dm.Status, dm.UpdatedAt = model.ChatRemoved, model.Now()
		_ = s.st.PutChat(dm)
		note := "You removed this contact. Their tmx can no longer reach yours."
		if shared {
			note = "You removed this contact. You are still in a group together, so their tmx can reach yours there until one of you leaves it."
		}
		s.systemLocked(dm.ID, note)
		s.publishChatLocked(dm.ID)
	}
	s.publish(model.EvContactRemoved, map[string]string{"peer_id": peer})
	var items []store.OutboxItem
	var err error
	if shared {
		items, err = s.st.TakeChatOutbox(dmID, peer) // group envelopes keep going
	} else {
		items, err = s.st.TakeOutbox(peer, 0)
	}
	s.mu.Unlock()
	if err == nil {
		s.failItems(items)
	}
	return nil
}

// cleanName trims a display name to one line of at most 60 characters.
func cleanName(n string) string {
	n = strings.Join(strings.Fields(n), " ")
	if utf8.RuneCountInString(n) > 60 {
		n = string([]rune(n)[:60])
	}
	return n
}

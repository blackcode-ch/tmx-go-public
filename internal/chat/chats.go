package chat

import (
	"errors"
	"strconv"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// ---- locking helpers: envelopes queued and agent mail collected under mu go out after it

type mailItem struct{ agentID, from, text string }

// unlock releases mu, then starts the workers of peers that got envelopes and delivers
// collected agent mail. With wait, it waits (up to SendWait) for the first pass of the
// workers of peers the node reaches now; an offline peer's (a slow failing dial) is not
// waited for, so a group message answers at once while a member is away.
func (s *Service) unlock(wait bool) {
	peers, mail := s.pendingPeers, s.pendingMail
	s.pendingPeers, s.pendingMail = nil, nil
	s.mu.Unlock()
	seen := map[string]bool{}
	var uniq []string
	for _, p := range peers {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	if wait {
		var now []string
		for _, p := range uniq {
			if s.node.Reachable(p) == p2p.Offline {
				s.kick(p, false)
			} else {
				now = append(now, p)
			}
		}
		s.kickAll(now)
	} else {
		for _, p := range uniq {
			s.kick(p, false)
		}
	}
	for _, m := range mail {
		if _, err := s.ag.DeliverExternal(m.agentID, m.from, m.text); err != nil {
			s.log.Info("chat: agent mail not delivered", "agent", m.agentID, "err", err)
		}
	}
}

// ---- reading chats

// Chats lists the chats the caller sees: the owner all of them, an agent the ones it takes
// part in.
func (s *Service) Chats(c fleet.Caller) ([]model.Chat, error) {
	if !s.Enabled() {
		return nil, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.st.ListChats()
	if err != nil {
		return nil, err
	}
	out := []model.Chat{}
	for _, ch := range list {
		if !c.Owner && ch.AgentID != c.AgentID {
			continue
		}
		out = append(out, s.decorateLocked(ch, false))
	}
	return out, nil
}

// Chat returns one chat with its members (owner view).
func (s *Service) Chat(id string) (model.Chat, error) {
	return s.ChatFor(fleet.OwnerCaller, id)
}

// ChatFor returns one chat if the caller may see it.
func (s *Service) ChatFor(c fleet.Caller, id string) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.chatForLocked(c, id)
	if err != nil {
		return ch, err
	}
	return s.decorateLocked(ch, true), nil
}

func (s *Service) chatForLocked(c fleet.Caller, id string) (model.Chat, error) {
	ch, err := s.st.GetChat(id)
	if errors.Is(err, store.ErrNotFound) {
		// Agents may name a chat instead of giving its ID.
		if list, lerr := s.st.ListChats(); lerr == nil {
			for _, x := range list {
				if strings.EqualFold(x.Name, id) && (c.Owner || x.AgentID == c.AgentID) {
					return x, nil
				}
			}
		}
		return ch, notFound("no chat %q", id)
	}
	if err != nil {
		return ch, err
	}
	if !c.Owner && ch.AgentID != c.AgentID {
		return ch, forbidden("you do not take part in chat %q", ch.Name)
	}
	return ch, nil
}

func (s *Service) decorateLocked(c model.Chat, members bool) model.Chat {
	self := s.selfLocked()
	if c.Kind == model.ChatDM {
		if ct, err := s.st.GetContact(c.PeerID); err == nil {
			c.Name = ct.Name
		}
	}
	c.Unread, _ = s.st.ChatUnread(c.ID, c.ReadSeq)
	c.Mentions, _ = s.st.ChatMentionsUnread(c.ID, c.ReadSeq)
	if last, err := s.st.LastChatMessage(c.ID); err == nil {
		c.LastMessage = &last
	}
	if c.AgentID != "" {
		if a, err := s.ag.Agent(c.AgentID); err == nil {
			c.AgentName = a.Name
		}
	}
	ms, _ := s.st.ChatMembers(c.ID)
	for i := range ms {
		ms[i].Me = ms[i].PeerID == self
		if ms[i].Me && ms[i].LeftAt == 0 && ms[i].Role == model.RoleAdmin {
			c.Admin = true
		}
	}
	if c.Kind == model.ChatDM {
		c.Admin = false
	}
	s.decorateAdminLocked(&c, ms) // admin.go: the open admin offer, who understands handovers
	if members || c.Kind == model.ChatGroup {
		c.Members = ms
	}
	return c
}

func (s *Service) publishChatLocked(id string) {
	if c, err := s.st.GetChat(id); err == nil {
		s.publish(model.EvChatUpdated, s.decorateLocked(c, true))
	}
}

// systemLocked adds a local notice to a chat ("Alex joined").
func (s *Service) systemLocked(chatID, text string) {
	now := model.Now()
	m, _, err := s.st.InsertChatMessage(model.ChatMessage{ID: model.NewID(), ChatID: chatID, Sender: model.ChatSystem,
		Body: text, At: now, State: model.ChatDelivered, ReceivedAt: now})
	if err != nil {
		s.log.Error("chat: store notice", "err", err)
		return
	}
	s.touchLocked(chatID, now)
	s.publish(model.EvChatMessage, m)
}

func (s *Service) touchLocked(chatID string, at int64) {
	if c, err := s.st.GetChat(chatID); err == nil {
		c.LastAt, c.UpdatedAt = at, model.Now()
		_ = s.st.PutChat(c)
	}
}

// ---- members

func (s *Service) currentMembersLocked(chatID string) []model.ChatMember {
	ms, _ := s.st.ChatMembers(chatID)
	var out []model.ChatMember
	for _, m := range ms {
		if m.LeftAt == 0 {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) memberLocked(chatID, peer string) (model.ChatMember, bool) {
	for _, m := range s.currentMembersLocked(chatID) {
		if m.PeerID == peer {
			return m, true
		}
	}
	return model.ChatMember{}, false
}

func (s *Service) isAdminLocked(chatID, peer string) bool {
	m, ok := s.memberLocked(chatID, peer)
	return ok && m.Role == model.RoleAdmin
}

func (s *Service) adminLocked(chatID string) string {
	for _, m := range s.currentMembersLocked(chatID) {
		if m.Role == model.RoleAdmin {
			return m.PeerID
		}
	}
	return ""
}

// othersLocked: the current members other than this install.
func (s *Service) othersLocked(chatID string) []string {
	self := s.selfLocked()
	var out []string
	for _, m := range s.currentMembersLocked(chatID) {
		if m.PeerID != self {
			out = append(out, m.PeerID)
		}
	}
	return out
}

func (s *Service) Members(id string) ([]model.ChatMember, error) {
	c, err := s.Chat(id)
	if err != nil {
		return nil, err
	}
	return c.Members, nil
}

// ---- creating chats

// CreateRequest is POST /api/chats: a group (name) or a DM with an existing contact (peer_id).
type CreateRequest struct {
	Kind   model.ChatKind `json:"kind,omitempty"` // group (default) | dm
	Name   string         `json:"name,omitempty"`
	PeerID string         `json:"peer_id,omitempty"`
}

func (s *Service) Create(req CreateRequest) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Kind == model.ChatDM {
		ct, err := s.contact(req.PeerID)
		if err != nil {
			return model.Chat{}, err
		}
		c, err := s.ensureDMLocked(ct, false)
		if err != nil {
			return c, err
		}
		return s.decorateLocked(c, true), nil
	}
	name := cleanName(req.Name)
	if name == "" {
		return model.Chat{}, invalid("give the group a name")
	}
	now := model.Now()
	c := model.Chat{ID: "g" + model.NewID(), Kind: model.ChatGroup, Name: name, CreatedBy: model.ChatMe,
		AgentID: s.ag.HeadID(), Status: model.ChatActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.st.PutChat(c); err != nil {
		return c, err
	}
	if err := s.st.PutChatMember(model.ChatMember{ChatID: c.ID, PeerID: s.selfLocked(), Name: s.myName(),
		Role: model.RoleAdmin, JoinedAt: now}); err != nil {
		return c, err
	}
	s.systemLocked(c.ID, "You created the group. Invite people with a QR code or a link.")
	s.publishChatLocked(c.ID)
	c, _ = s.st.GetChat(c.ID)
	return s.decorateLocked(c, true), nil
}

// ---- changing chats

// ChatPatch is PATCH /api/chats/{id}: the participating agent ("" = people only; an agent
// ID or name; "head" = the head) and, for a group's admin, its name.
type ChatPatch struct {
	AgentID *string `json:"agent_id,omitempty"`
	Name    *string `json:"name,omitempty"`
}

func (s *Service) Update(id string, p ChatPatch) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.chatForLocked(fleet.OwnerCaller, id)
	if err != nil {
		return c, err
	}
	if p.AgentID != nil {
		want := strings.TrimSpace(*p.AgentID)
		switch want {
		case "":
		case "head":
			if want = s.ag.HeadID(); want == "" {
				return c, notFound("this machine has no head agent")
			}
		default:
			a, err := s.ag.Agent(want)
			if err != nil {
				return c, notFound("no agent %q", want)
			}
			want = a.ID
		}
		c.AgentID = want
	}
	if p.Name != nil && c.Kind == model.ChatGroup {
		name := cleanName(*p.Name)
		if name == "" {
			return c, invalid("give the group a name")
		}
		if name != c.Name {
			if !s.isAdminLocked(c.ID, s.selfLocked()) || c.Status != model.ChatActive {
				return c, forbidden("only the group's admin can rename it")
			}
			c.Name = name
			c.Version++
			c.UpdatedAt = model.Now()
			if err := s.st.PutChat(c); err != nil {
				return c, err
			}
			s.systemLocked(c.ID, "You renamed the group to "+name+".")
			s.broadcastStateLocked(c.ID, groupEvent{Kind: evRenamed, Name: name})
		}
	}
	c.UpdatedAt = model.Now()
	if err := s.st.PutChat(c); err != nil {
		return c, err
	}
	s.publishChatLocked(c.ID)
	return s.decorateLocked(c, true), nil
}

// MarkRead marks everything in a chat as read.
func (s *Service) MarkRead(id string) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.chatForLocked(fleet.OwnerCaller, id)
	if err != nil {
		return c, err
	}
	seq, err := s.st.MaxChatSeq(c.ID)
	if err != nil {
		return c, err
	}
	if seq != c.ReadSeq {
		c.ReadSeq = seq
		if err := s.st.PutChat(c); err != nil {
			return c, err
		}
		s.publishChatLocked(c.ID)
	}
	return s.decorateLocked(c, true), nil
}

// Unread is GET /api/chats/unread.
type Unread struct {
	Total    int            `json:"total"`
	Chats    map[string]int `json:"chats"`
	Mentions int            `json:"mentions"` // unread messages that @mention the owner
}

func (s *Service) Unread() (Unread, error) {
	list, err := s.Chats(fleet.OwnerCaller)
	if err != nil {
		return Unread{}, err
	}
	u := Unread{Chats: map[string]int{}}
	for _, c := range list {
		if c.Unread > 0 {
			u.Chats[c.ID] = c.Unread
			u.Total += c.Unread
		}
		u.Mentions += c.Mentions
	}
	return u, nil
}

// Delete removes a chat's local history; only once it is over (group left or removed,
// DM whose contact was removed).
func (s *Service) Delete(id string) error {
	if !s.Enabled() {
		return ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.chatForLocked(fleet.OwnerCaller, id)
	if err != nil {
		return err
	}
	if c.Status == model.ChatActive || c.Status == model.ChatPending {
		if c.Kind == model.ChatDM {
			return errf(fleet.KindConflict, "remove the contact first; the DM's history can be deleted after that")
		}
		return errf(fleet.KindConflict, "leave the group first")
	}
	if err := s.st.DeleteChat(c.ID); err != nil {
		return err
	}
	s.removeChatFiles(c.ID)
	s.publish(model.EvChatRemoved, map[string]string{"id": c.ID})
	return nil
}

// ---- groups: membership changes (the admin is the authority for the member list)

func (s *Service) groupStateLocked(chatID string, ev groupEvent) (groupBody, error) {
	c, err := s.st.GetChat(chatID)
	if err != nil {
		return groupBody{}, err
	}
	b := groupBody{V: WireVersion, Name: c.Name, Version: c.Version, Event: ev, Members: []wireMember{},
		AdminProof: s.proofLocked(chatID)} // admin.go: who made us the admin (none for the creator)
	for _, m := range s.currentMembersLocked(chatID) {
		name := m.Name
		if m.PeerID == s.selfLocked() && s.myName() != "" {
			name = s.myName()
		}
		b.Members = append(b.Members, wireMember{PeerID: m.PeerID, Name: name, Role: m.Role, JoinedAt: m.JoinedAt,
			Addrs: s.memberAddrsLocked(m.PeerID)})
	}
	return b, nil
}

// broadcastStateLocked sends the group's state to every current member (and to extra peers,
// e.g. one just removed).
func (s *Service) broadcastStateLocked(chatID string, ev groupEvent, extra ...string) {
	b, err := s.groupStateLocked(chatID, ev)
	if err != nil {
		return
	}
	env, err := envelope(model.NewID(), TypeGroup, chatID, b)
	if err != nil {
		return
	}
	s.sendLocked(append(s.othersLocked(chatID), extra...), env, "")
}

// admitLocked adds a peer who used our group invite and tells everyone (or tells the
// joiner it could not be added).
func (s *Service) admitLocked(groupID string, ct model.Contact) error {
	c, err := s.st.GetChat(groupID)
	self := s.selfLocked()
	if err != nil || c.Kind != model.ChatGroup || c.Status != model.ChatActive || !s.isAdminLocked(groupID, self) {
		env, _ := envelope(model.NewID(), TypeGroup, groupID, groupBody{V: WireVersion, Name: c.Name, Version: c.Version + 1,
			Members: []wireMember{}, Event: s.refusedJoinEventLocked(c)}) // closed, or moved to the new admin
		s.sendLocked([]string{ct.PeerID}, env, "")
		return nil
	}
	if _, ok := s.memberLocked(groupID, ct.PeerID); ok {
		s.broadcastStateLocked(groupID, groupEvent{Kind: evJoined, PeerID: ct.PeerID, Name: ct.Name}) // rejoin: resend the state
		s.sendBoardToLocked(groupID, ct.PeerID)
		return nil
	}
	if len(s.currentMembersLocked(groupID)) >= MaxGroupMembers {
		b, err := s.groupStateLocked(groupID, groupEvent{Kind: evFull, PeerID: ct.PeerID, Name: ct.Name})
		if err != nil {
			return err
		}
		env, _ := envelope(model.NewID(), TypeGroup, groupID, b)
		s.sendLocked([]string{ct.PeerID}, env, "")
		s.systemLocked(groupID, ct.Name+" used your invite, but the group is full.")
		return nil
	}
	now := model.Now()
	if err := s.st.PutChatMember(model.ChatMember{ChatID: groupID, PeerID: ct.PeerID, Name: ct.Name, Role: model.RoleMember, JoinedAt: now}); err != nil {
		return err
	}
	c.Version++
	c.UpdatedAt = now
	if err := s.st.PutChat(c); err != nil {
		return err
	}
	s.systemLocked(groupID, ct.Name+" joined.")
	s.broadcastStateLocked(groupID, groupEvent{Kind: evJoined, PeerID: ct.PeerID, Name: ct.Name})
	s.sendBoardToLocked(groupID, ct.PeerID) // after the state: the board's topics and logs
	s.publishChatLocked(groupID)
	return nil
}

// RemoveMember: the admin removes someone from a group.
func (s *Service) RemoveMember(chatID, peer string) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.chatForLocked(fleet.OwnerCaller, chatID)
	if err != nil {
		return c, err
	}
	self := s.selfLocked()
	if c.Kind != model.ChatGroup || c.Status != model.ChatActive || !s.isAdminLocked(c.ID, self) {
		return c, forbidden("only the group's admin can remove people")
	}
	if peer == self || peer == model.ChatMe {
		return c, invalid("to leave the group, use leave")
	}
	m, ok := s.memberLocked(c.ID, peer)
	if !ok {
		return c, notFound("%q is not a member of this group", peer)
	}
	now := model.Now()
	m.LeftAt = now
	if err := s.st.PutChatMember(m); err != nil {
		return c, err
	}
	c.Version++
	c.UpdatedAt = now
	if err := s.st.PutChat(c); err != nil {
		return c, err
	}
	s.systemLocked(c.ID, "You removed "+m.Name+".")
	s.dropQueuedLocked(c.ID, peer, false)
	s.broadcastStateLocked(c.ID, groupEvent{Kind: evRemoved, PeerID: peer, Name: m.Name}, peer)
	s.untrustLocked([]string{peer})
	s.publishChatLocked(c.ID)
	return s.decorateLocked(c, true), nil
}

// Leave leaves a group. The admin leaving closes it for everyone (the admin is the only
// authority for the member list; see DESIGN.md "Chats").
func (s *Service) Leave(chatID string) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.chatForLocked(fleet.OwnerCaller, chatID)
	if err != nil {
		return c, err
	}
	if c.Kind != model.ChatGroup {
		return c, invalid("a DM ends by removing the contact")
	}
	if c.Status == model.ChatLeft || c.Status == model.ChatRemoved {
		return s.decorateLocked(c, true), nil
	}
	if s.deferLeaveLocked(c) { // admin.go: just handed over; leave once the new admin took over
		s.publishChatLocked(c.ID)
		return s.decorateLocked(c, true), nil
	}
	self := s.selfLocked()
	now := model.Now()
	others := s.othersLocked(c.ID)
	s.dropQueuedLocked(c.ID, "", true) // nothing more goes to the group, except the goodbye below
	if s.isAdminLocked(c.ID, self) {
		env, err := envelope(model.NewID(), TypeGroup, c.ID, groupBody{V: WireVersion, Name: c.Name, Version: c.Version + 1,
			Members: []wireMember{}, Event: groupEvent{Kind: evClosed, PeerID: self, Name: s.myName()}, AdminProof: s.proofLocked(c.ID)})
		if err != nil {
			return c, err
		}
		s.sendLocked(others, env, "")
		for _, m := range s.currentMembersLocked(c.ID) {
			m.LeftAt = now
			_ = s.st.PutChatMember(m)
		}
		c.Version++
		s.systemLocked(c.ID, "You closed the group.")
	} else {
		if admin := s.adminLocked(c.ID); admin != "" {
			env, err := envelope(model.NewID(), TypeLeave, c.ID, leaveBody{V: WireVersion})
			if err != nil {
				return c, err
			}
			s.sendLocked([]string{admin}, env, "")
		}
		if m, ok := s.memberLocked(c.ID, self); ok {
			m.LeftAt = now
			_ = s.st.PutChatMember(m)
		}
		s.systemLocked(c.ID, "You left the group.")
	}
	c.Status, c.UpdatedAt = model.ChatLeft, now
	if err := s.st.PutChat(c); err != nil {
		return c, err
	}
	s.untrustLocked(others) // the admin is a contact: our goodbye to it still goes out
	s.publishChatLocked(c.ID)
	return s.decorateLocked(c, true), nil
}

// onGroup applies a group's state from its admin, then replays the group messages held
// because their sender was not in our member list yet.
func (s *Service) onGroup(env p2p.Envelope) error {
	err := s.applyGroup(env)
	s.replayHeld(env.ChatID)
	s.afterAdminChange(env.ChatID) // admin.go: a completed handover (and then_leave)
	return err
}

// applyGroup applies a newer group state: the member list, the node's trust in the other
// members (they now send to us directly) and the end of trust in those who left.
func (s *Service) applyGroup(env p2p.Envelope) error {
	var b groupBody
	if err := decodeBody(env.Body, &b); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.st.GetChat(env.ChatID)
	if err != nil {
		return errors.New("unknown group")
	}
	if c.Kind != model.ChatGroup || c.Status == model.ChatLeft || c.Status == model.ChatRemoved {
		return errors.New("not in this group")
	}
	if c.Status != model.ChatPending && b.Version <= c.Version {
		return nil // stale or repeated
	}
	prevAdmin := s.adminLocked(c.ID)
	proof, err := s.verifyAdminLocked(c, env.From, b) // admin.go: the admin we know, or a handover its proof shows
	if err != nil {
		return err
	}
	if len(b.Members) > MaxGroupMembers {
		return errors.New("too many members")
	}
	for _, m := range b.Members {
		if m.PeerID == "" || len(m.PeerID) > 128 || len(m.Addrs) > maxMemberAddrs {
			return errors.New("bad member entry")
		}
	}
	self := s.selfLocked()
	now := model.Now()
	names := map[string]string{}
	incoming := map[string]wireMember{}
	for _, m := range b.Members {
		incoming[m.PeerID] = m
	}
	var gone []string // members who are no longer in the group
	for _, m := range s.currentMembersLocked(c.ID) {
		names[m.PeerID] = m.Name
		if _, ok := incoming[m.PeerID]; !ok {
			m.LeftAt = now
			_ = s.st.PutChatMember(m)
			if m.PeerID != self {
				gone = append(gone, m.PeerID)
			}
		}
	}
	for _, wm := range b.Members {
		name := cleanName(wm.Name)
		if wm.PeerID == self && s.myName() != "" {
			name = s.myName()
		}
		if name == "" {
			name = names[wm.PeerID]
		}
		if ct, err := s.st.GetContact(wm.PeerID); err == nil {
			name = ct.Name // our own name for people we know
		}
		role := model.RoleMember
		if wm.Role == model.RoleAdmin {
			role = model.RoleAdmin
		}
		joined := wm.JoinedAt
		if joined == 0 {
			joined = now
		}
		names[wm.PeerID] = name
		_ = s.st.PutChatMember(model.ChatMember{ChatID: c.ID, PeerID: wm.PeerID, Name: name, Role: role, JoinedAt: joined})
	}
	if n := cleanName(b.Name); n != "" {
		c.Name = n
	}
	c.Version = b.Version
	_, stillIn := incoming[self]
	wasPending := c.Status == model.ChatPending
	if stillIn {
		c.Status = model.ChatActive
	} else {
		c.Status = model.ChatRemoved
	}
	c.UpdatedAt = now
	if err := s.st.PutChat(c); err != nil {
		return err
	}
	who := b.Event.Name
	if n, ok := names[b.Event.PeerID]; ok && n != "" {
		who = n
	}
	if who == "" {
		who = "Someone"
	}
	admin := names[env.From]
	if admin == "" {
		admin = "The admin"
	}
	switch {
	case !stillIn && b.Event.Kind == evFull:
		s.systemLocked(c.ID, "The group is full ("+strconv.Itoa(MaxGroupMembers)+" members); you could not join.")
	case !stillIn && b.Event.Kind == evClosed:
		s.systemLocked(c.ID, admin+" closed the group.")
	case !stillIn && b.Event.Kind == evMoved:
		s.systemLocked(c.ID, admin+" handed the group to "+who+"; ask "+who+" for an invite.")
	case !stillIn:
		s.systemLocked(c.ID, admin+" removed you from the group.")
	case wasPending:
		s.systemLocked(c.ID, "You joined "+c.Name+".")
	case b.Event.Kind == evJoined && b.Event.PeerID != self:
		s.systemLocked(c.ID, who+" joined.")
	case b.Event.Kind == evRemoved:
		s.systemLocked(c.ID, who+" was removed.")
	case b.Event.Kind == evLeft:
		s.systemLocked(c.ID, who+" left.")
	case b.Event.Kind == evRenamed:
		s.systemLocked(c.ID, admin+" renamed the group to "+c.Name+".")
	}
	addrs := map[string][]string{}
	for _, wm := range b.Members {
		addrs[wm.PeerID] = wm.Addrs
	}
	if stillIn {
		for _, p := range gone {
			s.dropQueuedLocked(c.ID, p, false)
		}
		s.trustMembersLocked(c.ID, addrs)
		s.untrustLocked(gone)
	} else {
		// We are out: nothing more goes to this group, and its members are strangers again
		// unless they are contacts or share another group with us.
		s.dropQueuedLocked(c.ID, "", true)
		var all []string
		ms, _ := s.st.ChatMembers(c.ID)
		for _, m := range ms {
			all = append(all, m.PeerID)
		}
		s.untrustLocked(all)
	}
	s.adminAppliedLocked(c.ID, prevAdmin, proof, stillIn) // admin.go: "Alex made Bo the admin", our takeover
	s.publishChatLocked(c.ID)
	return nil
}

// onLeave: a member tells us (the admin) that it leaves.
func (s *Service) onLeave(env p2p.Envelope) error {
	var b leaveBody
	if err := decodeBody(env.Body, &b); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.st.GetChat(env.ChatID)
	if err != nil || c.Kind != model.ChatGroup || c.Status != model.ChatActive {
		return errors.New("unknown group")
	}
	if !s.isAdminLocked(c.ID, s.selfLocked()) {
		return errors.New("leave sent to a member that is not the admin")
	}
	m, ok := s.memberLocked(c.ID, env.From)
	if !ok {
		return errors.New("not a member")
	}
	now := model.Now()
	m.LeftAt = now
	if err := s.st.PutChatMember(m); err != nil {
		return err
	}
	c.Version++
	c.UpdatedAt = now
	if err := s.st.PutChat(c); err != nil {
		return err
	}
	s.systemLocked(c.ID, m.Name+" left.")
	s.dropQueuedLocked(c.ID, m.PeerID, false)
	s.broadcastStateLocked(c.ID, groupEvent{Kind: evLeft, PeerID: m.PeerID, Name: m.Name})
	s.untrustLocked([]string{m.PeerID})
	s.publishChatLocked(c.ID)
	return nil
}

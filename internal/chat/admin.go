package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// Handing a group's admin role to another member (DESIGN.md "Chats", Groups). The owner's
// decision (28 Oct 2026): it is automatic on the new admin's side, and the admin's owner
// confirms it with biometrics on their machine (the API asks; see api/chats.go).
//
//  1. Offer: the admin A sends the member B chat.admin_offer through the outbox (kept in
//     chats.admin_offer while it waits). B's tmx accepts by itself with chat.admin_accept.
//     An older tmx acknowledges the unknown type and drops it, so the role never goes to an
//     install that could not act as admin: A keeps waiting until it cancels (or 7 days).
//  2. Handover: on the accept A signs a certificate chat.admin {version: v+1, admin: B},
//     appends it to the group's chain (the last 8 kept), swaps the roles, revokes its invites
//     to the group and sends everyone the state at v+1 carrying the chain as admin_proof.
//  3. Takeover: B applies it and at once sends its own state at v+2 with the same chain, so
//     members who cannot reach A, or are offline until A is gone, learn it from B.
//  4. A member accepts a state from the admin it knows, or one naming a new admin only with a
//     chain that starts at a certificate from the admin it knows for a version above its own,
//     each later one issued by the admin the previous one named (verifyAdminLocked). The
//     chain travels with every state, so a member offline through any number of handovers (up
//     to 8) catches up from any later state, and an old certificate cannot be replayed once a
//     member is past its version.

const (
	maxAdminProof = 8        // certificates kept and accepted in a chain
	capAdmin      = "admin1" // advertised in receipts: this tmx understands admin handovers
)

// ownCaps: what this tmx tells others it understands (receiptBody.Caps).
var ownCaps = []string{capAdmin}

type adminOfferBody struct {
	V       int   `json:"v"`
	Version int64 `json:"version"` // the group's version when offered (information)
}

type adminAcceptBody struct {
	V  int    `json:"v"`
	ID string `json:"id"` // the offer's envelope ID
}

// adminCertBody is the signed statement (Envelope.From = the admin issuing it): from the
// group state Version on, Admin is the group's admin.
type adminCertBody struct {
	V       int    `json:"v"`
	Version int64  `json:"version"`
	Admin   string `json:"admin"`
	Name    string `json:"name,omitempty"` // the new admin's display name (a hint)
}

// offerRec is our offer (chats.admin_offer, JSON). After the handover it stays, Handed, until
// a state from the new admin above Version shows it took over: until then leaving waits
// (it would drop the handover still queued for it).
type offerRec struct {
	ID        string `json:"id"`
	PeerID    string `json:"peer_id"`
	Name      string `json:"name"`
	At        int64  `json:"at"`
	ThenLeave bool   `json:"then_leave,omitempty"`
	Handed    bool   `json:"handed,omitempty"`
	Version   int64  `json:"version,omitempty"` // handed: the version of our handover state
}

// adminMem is the Service's in-memory bookkeeping for handovers (under mu).
type adminMem struct {
	caps      map[string]bool // peers that advertised capAdmin (cache of peer_caps; nil = not loaded)
	behind    map[string]bool // chats where we told the owner we are too far behind to check the admin
	conflicts map[string]bool // conflicting certificates already logged (by certificate ID)
}

// AdminRequest is POST /api/chats/{id}/admin.
type AdminRequest struct {
	PeerID    string `json:"peer_id"`
	ThenLeave bool   `json:"then_leave,omitempty"` // leave the group once the new admin took over
}

// ---- stored state

func (s *Service) proofLocked(chatID string) []p2p.Envelope {
	raw, _, err := s.st.ChatAdmin(chatID)
	if err != nil || raw == "" {
		return nil
	}
	var out []p2p.Envelope
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.log.Error("chat: stored admin proof", "chat", chatID, "err", err)
		return nil
	}
	return out
}

func (s *Service) setProofLocked(chatID string, proof []p2p.Envelope) {
	if len(proof) > maxAdminProof {
		proof = proof[len(proof)-maxAdminProof:]
	}
	b, err := json.Marshal(proof)
	if err == nil {
		err = s.st.SetChatAdminProof(chatID, string(b))
	}
	if err != nil {
		s.log.Error("chat: store admin proof", "chat", chatID, "err", err)
	}
}

func (s *Service) offerLocked(chatID string) (offerRec, bool) {
	var o offerRec
	_, raw, err := s.st.ChatAdmin(chatID)
	if err != nil || raw == "" || json.Unmarshal([]byte(raw), &o) != nil || o.PeerID == "" {
		return offerRec{}, false
	}
	return o, true
}

func (s *Service) setOfferLocked(chatID string, o *offerRec) {
	raw := ""
	if o != nil {
		b, _ := json.Marshal(o)
		raw = string(b)
	}
	if err := s.st.SetChatAdminOffer(chatID, raw); err != nil {
		s.log.Error("chat: store admin offer", "chat", chatID, "err", err)
	}
}

// openOfferLocked is the offer still waiting for an answer: we are still the admin, the
// member still in the group, and it has not expired with the outbox.
func (s *Service) openOfferLocked(c model.Chat) (offerRec, bool) {
	o, ok := s.offerLocked(c.ID)
	if !ok || o.Handed || c.Kind != model.ChatGroup || c.Status != model.ChatActive || !s.isAdminLocked(c.ID, s.selfLocked()) {
		return offerRec{}, false
	}
	if _, member := s.memberLocked(c.ID, o.PeerID); !member || model.Now()-o.At > s.o.OutboxTTL.Milliseconds() {
		return offerRec{}, false
	}
	return o, true
}

// nameLocked: what this install calls peer in a chat (our contact name, else its member
// name, also a former member's).
func (s *Service) nameLocked(chatID, peer string) string {
	if peer == s.selfLocked() {
		if n := s.myName(); n != "" {
			return n
		}
	}
	if ct, err := s.st.GetContact(peer); err == nil && ct.Name != "" {
		return ct.Name
	}
	if m, ok := s.formerMemberLocked(chatID, peer); ok && m.Name != "" {
		return m.Name
	}
	return "Someone"
}

// ---- the admin's side

// AdminTarget checks that the owner may offer chatID's admin role to peer now, and returns the
// group's and the member's names (for the owner's confirmation prompt).
func (s *Service) AdminTarget(chatID, peer string) (group, name string, err error) {
	if !s.Enabled() {
		return "", "", ErrOff
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, m, err := s.offerTargetLocked(chatID, peer)
	return c.Name, m.Name, err
}

func (s *Service) offerTargetLocked(chatID, peer string) (model.Chat, model.ChatMember, error) {
	var none model.ChatMember
	c, err := s.chatForLocked(fleet.OwnerCaller, chatID)
	if err != nil {
		return c, none, err
	}
	self := s.selfLocked()
	switch {
	case c.Kind != model.ChatGroup:
		return c, none, invalid("only a group has an admin")
	case c.Status != model.ChatActive || !s.isAdminLocked(c.ID, self):
		return c, none, forbidden("only the group's admin can make someone else the admin")
	case strings.TrimSpace(peer) == "":
		return c, none, invalid("name the member who becomes the admin (peer_id)")
	case peer == self || peer == model.ChatMe:
		return c, none, invalid("you are the admin already")
	}
	m, ok := s.memberLocked(c.ID, peer)
	if !ok {
		return c, none, notFound("%q is not a member of this group", peer)
	}
	if o, ok := s.openOfferLocked(c); ok {
		return c, none, errf(fleet.KindConflict, "the admin role is offered to %s already; cancel that offer first", s.nameLocked(c.ID, o.PeerID))
	}
	m.Name = s.nameLocked(c.ID, m.PeerID)
	return c, m, nil
}

// OfferAdmin offers a member the group's admin role. The member's tmx takes it over by itself
// as soon as the offer reaches it; until then the chat's admin_offer shows the wait.
func (s *Service) OfferAdmin(chatID string, req AdminRequest) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, m, err := s.offerTargetLocked(chatID, req.PeerID)
	if err != nil {
		return c, err
	}
	id := model.NewID()
	env, err := envelope(id, TypeAdminOffer, c.ID, adminOfferBody{V: WireVersion, Version: c.Version})
	if err != nil {
		return c, err
	}
	// Tagged with the offer's ID (the outbox's message_id) so a cancel can take it back.
	if s.sendLocked([]string{m.PeerID}, env, id) == 0 {
		return c, errors.New("could not queue the offer")
	}
	s.setOfferLocked(c.ID, &offerRec{ID: id, PeerID: m.PeerID, Name: m.Name, At: model.Now(), ThenLeave: req.ThenLeave})
	s.publishChatLocked(c.ID)
	return s.decorateLocked(c, true), nil
}

// CancelAdminOffer withdraws the waiting offer (still queued: it is not sent; delivered: a
// late answer is ignored).
func (s *Service) CancelAdminOffer(chatID string) (model.Chat, error) {
	if !s.Enabled() {
		return model.Chat{}, ErrOff
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.chatForLocked(fleet.OwnerCaller, chatID)
	if err != nil {
		return c, err
	}
	o, ok := s.offerLocked(c.ID)
	switch {
	case ok && o.Handed:
		return c, errf(fleet.KindConflict, "%s is the admin already", s.nameLocked(c.ID, o.PeerID))
	case !ok:
		return c, notFound("no admin offer is waiting in this group")
	}
	if _, err := s.st.DropOutboxTagged(c.ID, o.ID); err != nil {
		return c, err
	}
	s.setOfferLocked(c.ID, nil)
	s.publishChatLocked(c.ID)
	return s.decorateLocked(c, true), nil
}

// onAdminAccept: the member's tmx took the role we offered; hand the group over.
func (s *Service) onAdminAccept(env p2p.Envelope) error {
	var b adminAcceptBody
	if err := decodeBody(env.Body, &b); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.st.GetChat(env.ChatID)
	if err != nil || c.Kind != model.ChatGroup {
		return errors.New("unknown group")
	}
	o, ok := s.openOfferLocked(c)
	if !ok || o.ID != b.ID || o.PeerID != env.From {
		return errors.New("no such admin offer (withdrawn, or we are no longer the admin)")
	}
	if err := s.handOverLocked(c, o); err != nil {
		return err
	}
	s.noteCapLocked(env.From, ownCaps)
	return nil
}

// handOverLocked signs the certificate, swaps the roles and tells everyone (step 2 above).
func (s *Service) handOverLocked(c model.Chat, o offerRec) error {
	self := s.selfLocked()
	me, ok1 := s.memberLocked(c.ID, self)
	them, ok2 := s.memberLocked(c.ID, o.PeerID)
	if !ok1 || !ok2 {
		return errors.New("not both in the group any more")
	}
	name := s.nameLocked(c.ID, o.PeerID)
	cert, err := envelope(model.NewID(), TypeAdminCert, c.ID, adminCertBody{V: WireVersion, Version: c.Version + 1, Admin: o.PeerID, Name: them.Name})
	if err != nil {
		return err
	}
	if cert, err = s.node.Sign(cert); err != nil {
		return fmt.Errorf("sign the admin certificate: %w", err)
	}
	s.setProofLocked(c.ID, append(s.proofLocked(c.ID), cert))
	me.Role, them.Role = model.RoleMember, model.RoleAdmin
	if err := s.st.PutChatMember(me); err != nil {
		return err
	}
	if err := s.st.PutChatMember(them); err != nil {
		return err
	}
	c.Version++
	c.UpdatedAt = model.Now()
	if err := s.st.PutChat(c); err != nil {
		return err
	}
	s.revokeInvitesLocked(c.ID) // joiners ask the new admin; one already pairing gets "moved"
	o.Handed, o.Version = true, c.Version
	s.setOfferLocked(c.ID, &o)
	s.systemLocked(c.ID, "You made "+name+" the admin.")
	if o.ThenLeave {
		s.systemLocked(c.ID, "You'll leave the group once "+name+"'s tmx has taken it over.")
	}
	s.broadcastStateLocked(c.ID, groupEvent{Kind: evAdmin, PeerID: o.PeerID, Name: them.Name})
	s.publishChatLocked(c.ID)
	return nil
}

func (s *Service) revokeInvitesLocked(chatID string) {
	for _, inv := range s.node.Invites() {
		if inv.GroupID == chatID && !inv.Revoked {
			if err := s.node.RevokeInvite(inv.ID); err != nil {
				s.log.Info("chat: revoke group invite", "invite", inv.ID, "err", err)
			}
		}
	}
}

// afterAdminChange runs after a group state was applied (onGroup): once the new admin's own
// state shows it took over, the handover is complete, and we leave if asked to (then_leave).
func (s *Service) afterAdminChange(chatID string) {
	s.mu.Lock()
	o, ok := s.offerLocked(chatID)
	c, err := s.st.GetChat(chatID)
	done := ok && o.Handed && err == nil && c.Version > o.Version
	if done {
		s.setOfferLocked(chatID, nil)
	}
	s.mu.Unlock()
	if done && o.ThenLeave && c.Status == model.ChatActive {
		if _, err := s.Leave(chatID); err != nil {
			s.log.Info("chat: leave after the handover", "chat", chatID, "err", err)
		}
	}
}

// deferLeaveLocked runs first in Leave. The admin leaving closes the group, and its open offer
// goes with it. Right after a handover, before the new admin's state is back, leaving would
// drop the handover still queued for it: the leave waits for that state (and leaving once
// more while it waits leaves at once).
func (s *Service) deferLeaveLocked(c model.Chat) bool {
	o, ok := s.offerLocked(c.ID)
	if !ok {
		return false
	}
	if !o.Handed || o.ThenLeave {
		s.setOfferLocked(c.ID, nil)
		return false
	}
	o.ThenLeave = true
	s.setOfferLocked(c.ID, &o)
	s.systemLocked(c.ID, "You'll leave the group once "+s.nameLocked(c.ID, o.PeerID)+"'s tmx has taken it over. Leave again to leave now.")
	return true
}

// ---- the new admin's side

// onAdminOffer: our admin offers us the role. This tmx takes it at once (automatic: the
// admin's owner confirmed it on their machine); the handover state follows.
func (s *Service) onAdminOffer(env p2p.Envelope) error {
	var b adminOfferBody
	if err := decodeBody(env.Body, &b); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.unlock(false)
	c, err := s.st.GetChat(env.ChatID)
	if err != nil || c.Kind != model.ChatGroup || c.Status != model.ChatActive {
		return errors.New("unknown group")
	}
	if !s.isAdminLocked(c.ID, env.From) {
		return errors.New("admin offer not from the group's admin")
	}
	if _, ok := s.memberLocked(c.ID, s.selfLocked()); !ok {
		return errors.New("not a member")
	}
	acc, err := envelope(model.NewID(), TypeAdminAccept, c.ID, adminAcceptBody{V: WireVersion, ID: env.ID})
	if err != nil {
		return err
	}
	s.sendLocked([]string{env.From}, acc, "")
	return nil
}

// ---- every member: who may send the group's state

// verifyAdminLocked decides whether a group state may change the member list (applyGroup):
// it comes from the admin we know and keeps it, or it names a new admin X proved by its
// admin_proof (see the top of this file). The sender must be X, or the admin we know handing
// over. It returns the proof to keep (nil: keep ours).
func (s *Service) verifyAdminLocked(c model.Chat, from string, b groupBody) ([]p2p.Envelope, error) {
	known := s.adminLocked(c.ID)
	if c.Status == model.ChatPending && s.isAdminLocked(c.ID, from) {
		known = from // joining: the inviter (a rejoin may still list an older admin too)
	}
	named := from // a state without members (closed, a refused join) speaks for its sender
	if len(b.Members) > 0 {
		named = ""
		n := 0
		for _, m := range b.Members {
			if m.Role == model.RoleAdmin {
				n++
				named = m.PeerID
			}
		}
		if n != 1 {
			return nil, errors.New("group state must name exactly one admin")
		}
	}
	if from == known && named == known {
		return nil, nil
	}
	if from != named && from != known {
		return nil, errors.New("group state not from the group's admin")
	}
	proof := b.AdminProof
	switch {
	case len(proof) == 0:
		return nil, errors.New("group state names a new admin without a proof")
	case len(proof) > maxAdminProof:
		return nil, errors.New("admin proof too long")
	}
	certs := make([]adminCertBody, len(proof))
	for i, e := range proof {
		if e.Type != TypeAdminCert || e.ChatID != c.ID {
			return nil, errors.New("admin proof holds something else")
		}
		if err := decodeBody(e.Body, &certs[i]); err != nil {
			return nil, fmt.Errorf("admin certificate: %w", err)
		}
	}
	floor := c.Version
	if c.Status == model.ChatPending {
		floor = 0 // any version while we wait for our first state
	}
	s.conflictsLocked(c.ID, proof, certs)
	start := -1
	for i, e := range proof {
		if e.From == known && certs[i].Version > floor {
			start = i
			break
		}
	}
	if start < 0 {
		s.behindLocked(c, proof, certs, floor, named)
		return nil, errors.New("admin proof does not start at the admin we know (forged, replayed, or too many handovers missed)")
	}
	issuer, last := known, floor
	for i := start; i < len(proof); i++ {
		e, cb := proof[i], certs[i]
		switch {
		case e.From != issuer:
			return nil, errors.New("admin proof: a certificate not issued by the admin before it")
		case cb.Version <= last:
			return nil, errors.New("admin proof: versions must rise")
		case cb.Admin == "" || cb.Admin == issuer:
			return nil, errors.New("admin proof: a certificate names no new admin")
		}
		if err := s.node.VerifyEnvelope(e); err != nil {
			return nil, fmt.Errorf("admin certificate: %w", err)
		}
		issuer, last = cb.Admin, cb.Version
	}
	if issuer != named {
		return nil, errors.New("admin proof names another admin than the state")
	}
	if b.Version < last {
		return nil, errors.New("group state older than its admin certificate")
	}
	return proof, nil
}

// conflictsLocked logs certificates that contradict ones we accepted (an admin naming two
// different successors for the same version): the first one seen stays.
func (s *Service) conflictsLocked(chatID string, proof []p2p.Envelope, certs []adminCertBody) {
	for _, have := range s.proofLocked(chatID) {
		var hb adminCertBody
		if decodeBody(have.Body, &hb) != nil {
			continue
		}
		for i, e := range proof {
			if e.ID == have.ID || e.From != have.From || certs[i].Version != hb.Version || certs[i].Admin == hb.Admin {
				continue
			}
			if s.adm.conflicts == nil {
				s.adm.conflicts = map[string]bool{}
			}
			if s.adm.conflicts[e.ID] || s.node.VerifyEnvelope(e) != nil {
				continue
			}
			s.adm.conflicts[e.ID] = true
			s.log.Warn("chat: conflicting admin certificates; the first one seen stays", "chat", chatID, "issuer", e.From,
				"version", hb.Version, "kept", hb.Admin, "other", certs[i].Admin)
		}
	}
}

// behindLocked: a full, consistent chain that starts after our version means we missed more
// handovers than the chain keeps. We cannot check the new admin; the owner is told once.
func (s *Service) behindLocked(c model.Chat, proof []p2p.Envelope, certs []adminCertBody, floor int64, named string) {
	if len(proof) < maxAdminProof || certs[0].Version <= floor || certs[len(certs)-1].Admin != named {
		return
	}
	for i, e := range proof {
		if s.node.VerifyEnvelope(e) != nil || i > 0 && (e.From != certs[i-1].Admin || certs[i].Version <= certs[i-1].Version) {
			return
		}
	}
	s.log.Warn("chat: missed too many admin handovers to check the group's admin", "chat", c.ID, "version", floor)
	if s.adm.behind == nil {
		s.adm.behind = map[string]bool{}
	}
	if !s.adm.behind[c.ID] {
		s.adm.behind[c.ID] = true
		s.systemLocked(c.ID, "The group changed admins too often while your tmx was away for it to check who runs the group now. Ask the admin to invite you again.")
	}
}

// adminAppliedLocked runs in applyGroup once a state is applied: it keeps the proof, says who
// made whom the admin, and if that is us, takes over at once (step 3 above).
func (s *Service) adminAppliedLocked(chatID, prevAdmin string, proof []p2p.Envelope, stillIn bool) {
	delete(s.adm.behind, chatID)
	if proof != nil {
		s.setProofLocked(chatID, proof)
	}
	admin := s.adminLocked(chatID)
	if !stillIn || admin == "" || admin == prevAdmin {
		return
	}
	self := s.selfLocked()
	issuer := prevAdmin
	if n := len(proof); n > 0 {
		issuer = proof[n-1].From
	}
	who := "you"
	if admin != self {
		who = s.nameLocked(chatID, admin)
	}
	s.systemLocked(chatID, s.nameLocked(chatID, issuer)+" made "+who+" the admin.")
	if admin != self {
		return
	}
	c, err := s.st.GetChat(chatID)
	if err != nil {
		return
	}
	c.Version++
	c.UpdatedAt = model.Now()
	if err := s.st.PutChat(c); err != nil {
		s.log.Error("chat: take over the group", "chat", chatID, "err", err)
		return
	}
	s.broadcastStateLocked(chatID, groupEvent{Kind: evAdmin, PeerID: self, Name: s.myName()})
}

// refusedJoinEventLocked: why a joiner on one of our invites is not added: we handed the group
// over (moved: ask the new admin), or it is closed.
func (s *Service) refusedJoinEventLocked(c model.Chat) groupEvent {
	if c.Kind == model.ChatGroup && c.Status == model.ChatActive {
		if admin := s.adminLocked(c.ID); admin != "" && admin != s.selfLocked() {
			return groupEvent{Kind: evMoved, PeerID: admin, Name: s.nameLocked(c.ID, admin)}
		}
	}
	return groupEvent{Kind: evClosed}
}

// ---- capabilities: who understands handovers

func (s *Service) loadCapsLocked() {
	if s.adm.caps != nil {
		return
	}
	s.adm.caps = map[string]bool{}
	all, err := s.st.PeerCaps()
	if err != nil {
		s.log.Error("chat: peer capabilities", "err", err)
		return
	}
	for p, c := range all {
		if slices.Contains(strings.Fields(c), capAdmin) {
			s.adm.caps[p] = true
		}
	}
}

// noteCapLocked records what a peer advertised. A peer seen with capAdmin for the first time
// may have been an older tmx until now, frozen at an old member list: it gets the current
// state of every group we run since a handover, so it catches up at once.
func (s *Service) noteCapLocked(peer string, caps []string) {
	if !slices.Contains(caps, capAdmin) {
		return
	}
	s.loadCapsLocked()
	if s.adm.caps[peer] {
		return
	}
	s.adm.caps[peer] = true
	if err := s.st.SetPeerCaps(peer, strings.Join(caps, " "), model.Now()); err != nil {
		s.log.Error("chat: store peer capabilities", "peer", peer, "err", err)
	}
	self := s.selfLocked()
	chats, _ := s.st.ListChats()
	for _, c := range chats {
		if c.Kind != model.ChatGroup || c.Status != model.ChatActive || !s.isAdminLocked(c.ID, self) || len(s.proofLocked(c.ID)) == 0 {
			continue
		}
		if _, ok := s.memberLocked(c.ID, peer); !ok {
			continue
		}
		b, err := s.groupStateLocked(c.ID, groupEvent{Kind: evAdmin, PeerID: self, Name: s.myName()})
		if err != nil {
			continue
		}
		if env, err := envelope(model.NewID(), TypeGroup, c.ID, b); err == nil {
			s.sendLocked([]string{peer}, env, "")
		}
	}
}

// decorateAdminLocked adds the derived handover fields to a group (decorateLocked).
func (s *Service) decorateAdminLocked(c *model.Chat, ms []model.ChatMember) {
	if c.Kind != model.ChatGroup {
		return
	}
	s.loadCapsLocked()
	self := s.selfLocked()
	for i := range ms {
		ms[i].Handover = ms[i].PeerID == self || s.adm.caps[ms[i].PeerID]
	}
	if o, ok := s.openOfferLocked(*c); ok {
		queued, _ := s.st.CountOutboxFor(c.ID, o.ID)
		c.AdminOffer = &model.ChatAdminOffer{PeerID: o.PeerID, Name: s.nameLocked(c.ID, o.PeerID), At: o.At, ThenLeave: o.ThenLeave, Sent: queued == 0}
	}
}

// offerSentLocked runs when an outbox item tagged tag went out (afterSent): our offer
// reached the member's tmx, which the UI shows while it waits for the answer.
func (s *Service) offerSentLocked(chatID, tag string) {
	if o, ok := s.offerLocked(chatID); ok && o.ID == tag && !o.Handed {
		s.publishChatLocked(chatID)
	}
}

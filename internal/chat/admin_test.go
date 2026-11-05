package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

// adminOf: the peer ID of the group's admin as s sees it ("" = none).
func adminOf(s *side, gid string) string {
	c, err := s.svc.Chat(gid)
	if err != nil {
		return ""
	}
	for _, m := range current(c.Members) {
		if m.Role == model.RoleAdmin {
			return m.PeerID
		}
	}
	return ""
}

func version(s *side, gid string) int64 {
	c, _ := s.st.GetChat(gid)
	return c.Version
}

// notices: the system lines of a chat on one side.
func notices(t *testing.T, s *side, gid string) []string {
	t.Helper()
	p, err := s.svc.Messages(owner, gid, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range p.Messages {
		if m.Sender == model.ChatSystem {
			out = append(out, m.Body)
		}
	}
	return out
}

func countLines(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, want) {
			n++
		}
	}
	return n
}

// handOver: from offers to the admin role and every side in all ends up seeing to as admin.
func handOver(t *testing.T, from, to *side, gid string, all ...*side) {
	t.Helper()
	c, err := from.svc.OfferAdmin(gid, AdminRequest{PeerID: to.node.ID()})
	if err != nil {
		t.Fatalf("%s offers %s: %v", from.name, to.name, err)
	}
	_ = c
	for _, s := range all {
		eventually(t, s.name+" sees "+to.name+" as admin", func() bool { return adminOf(s, gid) == to.node.ID() })
	}
}

// stateFrom builds a chat.group envelope as sent by from (for forgeries fed to onEnvelope).
func stateFrom(t *testing.T, from string, gid string, b groupBody) p2p.Envelope {
	t.Helper()
	env, err := envelope(model.NewID(), TypeGroup, gid, b)
	if err != nil {
		t.Fatal(err)
	}
	env.From = from
	return env
}

// members as a state lists them, with admin as the only admin.
func wireMembers(admin string, peers ...string) []wireMember {
	var out []wireMember
	for _, p := range peers {
		role := model.RoleMember
		if p == admin {
			role = model.RoleAdmin
		}
		out = append(out, wireMember{PeerID: p, Name: strings.TrimPrefix(p, "peer-"), Role: role, JoinedAt: 1})
	}
	return out
}

func cert(t *testing.T, signer *p2ptest.Fake, gid string, version int64, admin string) p2p.Envelope {
	t.Helper()
	env, err := envelope(model.NewID(), TypeAdminCert, gid, adminCertBody{V: WireVersion, Version: version, Admin: admin})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(env)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// Alex hands the group to Bo: Bo can invite, remove and rename, Alex no longer can, Alex's
// invites to the group are revoked, Bo's own state (v+2) follows, and everyone gets one line.
func TestAdminHandoverThreeMembers(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	v := version(alex, gid)
	if c, _ := alex.svc.Chat(gid); c.AdminOffer != nil {
		t.Fatalf("an offer before any: %+v", c.AdminOffer)
	}
	handOver(t, alex, bo, gid, alex, bo, cleo)
	for _, s := range []*side{alex, bo, cleo} {
		eventually(t, s.name+" has bo's own state", func() bool { return version(s, gid) == v+2 })
	}
	if c, _ := bo.svc.Chat(gid); !c.Admin || c.AdminOffer != nil {
		t.Fatalf("bo's view: admin %v offer %+v", c.Admin, c.AdminOffer)
	}
	if c, _ := alex.svc.Chat(gid); c.Admin || c.AdminOffer != nil || c.Status != model.ChatActive {
		t.Fatalf("alex's view: %+v", c)
	}
	for _, inv := range alex.node.Invites() {
		if inv.GroupID == gid && !inv.Revoked {
			t.Fatalf("alex's invite %s still works", inv.ID)
		}
	}
	eventually(t, "the lines", func() bool {
		return countLines(notices(t, alex, gid), "You made bo the admin.") == 1 &&
			countLines(notices(t, bo, gid), "alex made you the admin.") == 1 &&
			countLines(notices(t, cleo, gid), "alex made bo the admin.") == 1
	})
	// Bo runs it now.
	inv, err := bo.svc.CreateInvite(InviteRequest{Kind: p2p.InviteGroup, GroupID: gid})
	if err != nil {
		t.Fatalf("bo invites: %v", err)
	}
	dave := newSide(t, n, "dave")
	if _, err := dave.svc.Join(context.Background(), inv.Link); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*side{alex, cleo, dave} {
		eventually(t, s.name+" sees dave in", func() bool {
			c, _ := s.svc.Chat(gid)
			return c.Status == model.ChatActive && len(current(c.Members)) == 4
		})
	}
	name := "Trip 2"
	if _, err := bo.svc.Update(gid, ChatPatch{Name: &name}); err != nil {
		t.Fatalf("bo renames: %v", err)
	}
	if _, err := bo.svc.RemoveMember(gid, dave.node.ID()); err != nil {
		t.Fatalf("bo removes: %v", err)
	}
	for _, s := range []*side{alex, cleo} {
		eventually(t, s.name+" sees the rename and the removal", func() bool {
			c, _ := s.svc.Chat(gid)
			return c.Name == "Trip 2" && len(current(c.Members)) == 3
		})
	}
	// Alex is a member like the others.
	if _, err := alex.svc.CreateInvite(InviteRequest{Kind: p2p.InviteGroup, GroupID: gid}); fleet.KindOf(err) != fleet.KindForbidden {
		t.Fatalf("alex invites: %v", err)
	}
	if _, err := alex.svc.RemoveMember(gid, cleo.node.ID()); fleet.KindOf(err) != fleet.KindForbidden {
		t.Fatalf("alex removes: %v", err)
	}
	other := "Alex's trip"
	if _, err := alex.svc.Update(gid, ChatPatch{Name: &other}); fleet.KindOf(err) != fleet.KindForbidden {
		t.Fatalf("alex renames: %v", err)
	}
	if _, err := alex.svc.OfferAdmin(gid, AdminRequest{PeerID: cleo.node.ID()}); fleet.KindOf(err) != fleet.KindForbidden {
		t.Fatalf("alex hands over again: %v", err)
	}
	// Messages still go everywhere.
	if _, err := alex.svc.Send(owner, gid, "still here"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bo and cleo get alex's line", func() bool {
		return strings.Contains(bodies(messages(t, bo, gid)), "still here") && strings.Contains(bodies(messages(t, cleo, gid)), "still here")
	})
}

// Cleo is offline through the handover and Alex is gone afterwards: Bo's later states alone,
// with the proof they carry, bring her up to date.
func TestAdminHandoverOfflineMemberCatchesUp(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	cleo.node.SetOnline(false)
	handOver(t, alex, bo, gid, alex, bo)
	eventually(t, "alex has bo's state", func() bool { return version(alex, gid) == version(bo, gid) })
	alex.node.SetOnline(false)
	name := "Trip without Alex"
	if _, err := bo.svc.Update(gid, ChatPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if adminOf(cleo, gid) != alex.node.ID() {
		t.Fatal("cleo heard of the handover while offline")
	}
	cleo.node.SetOnline(true)
	eventually(t, "cleo accepts bo through the proof", func() bool {
		c, _ := cleo.svc.Chat(gid)
		return adminOf(cleo, gid) == bo.node.ID() && c.Name == name
	})
	if got := countLines(notices(t, cleo, gid), "alex made bo the admin."); got != 1 {
		t.Fatalf("cleo's lines: %q", notices(t, cleo, gid))
	}
	if p := cleo.svc.proofOf(gid); len(p) != 1 || p[0].From != alex.node.ID() {
		t.Fatalf("cleo keeps the proof %+v", p)
	}
}

func (s *Service) proofOf(gid string) []p2p.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proofLocked(gid)
}

// Bo's state at v+2 reaches Cleo before Alex's at v+1: it is accepted, and Alex's (from a
// member by then) is ignored as stale.
func TestAdminHandoverOutOfOrder(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	v := version(cleo, gid)
	var hold atomic.Bool
	hold.Store(true)
	alex.node.SetSendHook(func(peer string, env p2p.Envelope) error {
		if hold.Load() && peer == cleo.node.ID() && env.Type == TypeGroup {
			return p2p.ErrUnreachable
		}
		return nil
	})
	handOver(t, alex, bo, gid, alex, bo, cleo)
	if got := version(cleo, gid); got != v+2 {
		t.Fatalf("cleo is at %d, want bo's %d", got, v+2)
	}
	hold.Store(false)
	alex.node.SetSendHook(nil)
	alex.svc.kick(cleo.node.ID(), true)
	eventually(t, "alex's late state went out", func() bool {
		_, err := alex.st.NextOutbox(cleo.node.ID())
		return err != nil
	})
	time.Sleep(50 * time.Millisecond)
	if adminOf(cleo, gid) != bo.node.ID() || version(cleo, gid) != v+2 {
		t.Fatalf("after alex's late state: admin %s version %d", adminOf(cleo, gid), version(cleo, gid))
	}
	if got := countLines(notices(t, cleo, gid), "made bo the admin"); got != 1 {
		t.Fatalf("cleo's lines: %q", notices(t, cleo, gid))
	}
}

// Nobody but the admin, or a member it handed the group to, can change the member list.
func TestAdminForgeriesRejected(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	A, B, C := alex.node.ID(), bo.node.ID(), cleo.node.ID()
	v := version(bo, gid)
	reject := func(what string, env p2p.Envelope) {
		t.Helper()
		if err := bo.svc.onEnvelope(env); err == nil {
			t.Errorf("%s: accepted", what)
		}
		if adminOf(bo, gid) != A || version(bo, gid) != v {
			t.Fatalf("%s: bo now sees admin %s at %d", what, adminOf(bo, gid), version(bo, gid))
		}
	}
	// Cleo names herself admin: without a proof, with one she signed, with Alex's real
	// certificate for Bo changed to name her, with one Alex signed for another group.
	reject("no proof", stateFrom(t, C, gid, groupBody{V: WireVersion, Name: "Mine", Version: v + 5, Members: wireMembers(C, A, B, C)}))
	reject("a cert from a non-admin", stateFrom(t, C, gid, groupBody{V: WireVersion, Name: "Mine", Version: v + 5,
		Members: wireMembers(C, A, B, C), AdminProof: []p2p.Envelope{cert(t, cleo.node, gid, v+1, C)}}))
	real := cert(t, alex.node, gid, v+1, B)
	tampered := real
	tampered.Body, _ = json.Marshal(adminCertBody{V: WireVersion, Version: v + 1, Admin: C})
	reject("a tampered cert", stateFrom(t, C, gid, groupBody{V: WireVersion, Name: "Mine", Version: v + 5,
		Members: wireMembers(C, A, B, C), AdminProof: []p2p.Envelope{tampered}}))
	reject("another group's cert", stateFrom(t, C, gid, groupBody{V: WireVersion, Name: "Mine", Version: v + 5,
		Members: wireMembers(C, A, B, C), AdminProof: []p2p.Envelope{cert(t, alex.node, "g-other", v+1, C)}}))
	// A real certificate for Bo, sent by Cleo naming herself: the proof names someone else.
	reject("a proof for someone else", stateFrom(t, C, gid, groupBody{V: WireVersion, Name: "Mine", Version: v + 5,
		Members: wireMembers(C, A, B, C), AdminProof: []p2p.Envelope{real}}))
	// Two admins, or the sender not the admin it names.
	two := wireMembers(A, A, B, C)
	two[2].Role = model.RoleAdmin
	reject("two admins", stateFrom(t, A, gid, groupBody{V: WireVersion, Name: "x", Version: v + 1, Members: two}))

	// A hand-back: Alex → Bo → Alex. Bo, a member again, replays the state that made him
	// admin (or the whole chain) at a higher version: Cleo is past those certificates.
	handOver(t, alex, bo, gid, alex, bo, cleo)
	old := bo.svc.proofOf(gid)
	eventually(t, "bo's takeover everywhere", func() bool { return version(cleo, gid) == version(bo, gid) })
	handOver(t, bo, alex, gid, alex, bo, cleo)
	eventually(t, "alex's takeover everywhere", func() bool { return version(cleo, gid) == version(alex, gid) })
	vc := version(cleo, gid)
	for what, proof := range map[string][]p2p.Envelope{"the old proof": old, "the whole chain": cleo.svc.proofOf(gid)} {
		env := stateFrom(t, B, gid, groupBody{V: WireVersion, Name: "Bo's again", Version: vc + 10, Members: wireMembers(B, A, B, C), AdminProof: proof})
		if err := cleo.svc.onEnvelope(env); err == nil {
			t.Errorf("replayed %s: accepted", what)
		}
		if adminOf(cleo, gid) != A || version(cleo, gid) != vc {
			t.Fatalf("replayed %s: cleo sees admin %s at %d", what, adminOf(cleo, gid), version(cleo, gid))
		}
	}
}

// An older tmx (it acknowledges the offer and drops it) never takes the role: Alex waits, can
// cancel, and an offer still queued is cancelled before it goes out.
func TestAdminOfferToOldInstallAndCancel(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	alex.node.SetSendHook(func(peer string, env p2p.Envelope) error {
		if env.Type == TypeAdminOffer {
			return p2ptest.Dropped
		}
		return nil
	})
	c, err := alex.svc.OfferAdmin(gid, AdminRequest{PeerID: bo.node.ID(), ThenLeave: true})
	if err != nil {
		t.Fatal(err)
	}
	if o := c.AdminOffer; o == nil || o.PeerID != bo.node.ID() || o.Name != "bo" || !o.ThenLeave {
		t.Fatalf("offer: %+v", o)
	}
	eventually(t, "the offer reached bo's tmx", func() bool {
		c, _ := alex.svc.Chat(gid)
		return c.AdminOffer != nil && c.AdminOffer.Sent
	})
	time.Sleep(100 * time.Millisecond)
	if adminOf(alex, gid) != alex.node.ID() || adminOf(bo, gid) != alex.node.ID() {
		t.Fatal("the role moved to an old tmx")
	}
	if _, err := alex.svc.OfferAdmin(gid, AdminRequest{PeerID: cleo.node.ID()}); fleet.KindOf(err) != fleet.KindConflict {
		t.Fatalf("a second offer: %v", err)
	}
	if c, err := alex.svc.CancelAdminOffer(gid); err != nil || c.AdminOffer != nil {
		t.Fatalf("cancel: %+v %v", c.AdminOffer, err)
	}
	if _, err := alex.svc.CancelAdminOffer(gid); fleet.KindOf(err) != fleet.KindNotFound {
		t.Fatalf("cancel twice: %v", err)
	}
	alex.node.SetSendHook(nil)

	// Cleo is offline: the offer waits in the outbox, and a cancel takes it back.
	cleo.node.SetOnline(false)
	c, err = alex.svc.OfferAdmin(gid, AdminRequest{PeerID: cleo.node.ID()})
	if err != nil || c.AdminOffer == nil || c.AdminOffer.Sent {
		t.Fatalf("offer to offline cleo: %+v %v", c.AdminOffer, err)
	}
	if _, err := alex.svc.CancelAdminOffer(gid); err != nil {
		t.Fatal(err)
	}
	if _, err := alex.st.NextOutbox(cleo.node.ID()); err == nil {
		t.Fatal("the cancelled offer is still queued")
	}
	cleo.node.SetOnline(true)

	// Bo's answer is late (his tmx could not reach alex): after a cancel it changes nothing.
	var hold atomic.Bool
	hold.Store(true)
	bo.node.SetSendHook(func(peer string, env p2p.Envelope) error {
		if hold.Load() && env.Type == TypeAdminAccept {
			return p2p.ErrUnreachable
		}
		return nil
	})
	if _, err := alex.svc.OfferAdmin(gid, AdminRequest{PeerID: bo.node.ID()}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the offer reached bo", func() bool {
		c, _ := alex.svc.Chat(gid)
		return c.AdminOffer != nil && c.AdminOffer.Sent
	})
	if _, err := alex.svc.CancelAdminOffer(gid); err != nil {
		t.Fatal(err)
	}
	hold.Store(false)
	bo.svc.kick(alex.node.ID(), true)
	eventually(t, "bo's late answer went out", func() bool {
		_, err := bo.st.NextOutbox(alex.node.ID())
		return err != nil
	})
	time.Sleep(50 * time.Millisecond)
	if adminOf(alex, gid) != alex.node.ID() {
		t.Fatal("a late answer moved the role")
	}
	// A plain offer to cleo now goes through.
	handOver(t, alex, cleo, gid, alex, bo, cleo)
}

// "Make Bo admin and leave": Alex leaves once Bo's own state is back; Bo then runs the group.
func TestAdminThenLeave(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	if _, err := alex.svc.OfferAdmin(gid, AdminRequest{PeerID: bo.node.ID(), ThenLeave: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alex left", func() bool {
		c, _ := alex.svc.Chat(gid)
		return c.Status == model.ChatLeft
	})
	for _, s := range []*side{bo, cleo} {
		eventually(t, s.name+" sees bo run the group without alex", func() bool {
			c, _ := s.svc.Chat(gid)
			return adminOf(s, gid) == bo.node.ID() && len(current(c.Members)) == 2
		})
	}
	eventually(t, "cleo's lines", func() bool {
		l := notices(t, cleo, gid)
		return countLines(l, "alex made bo the admin.") == 1 && countLines(l, "alex left.") == 1
	})
	name := "Bo's trip"
	if _, err := bo.svc.Update(gid, ChatPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cleo sees bo's rename", func() bool {
		c, _ := cleo.svc.Chat(gid)
		return c.Name == name
	})
}

// Leaving right after the handover, before Bo's own state is back, waits for it (it would
// drop the handover still queued for Bo); leaving once more leaves at once.
func TestLeaveWaitsForTakeover(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	var hold atomic.Bool
	hold.Store(true)
	bo.node.SetSendHook(func(peer string, env p2p.Envelope) error {
		if hold.Load() && peer == alex.node.ID() && env.Type == TypeGroup {
			return p2p.ErrUnreachable
		}
		return nil
	})
	handOver(t, alex, bo, gid, alex, bo, cleo)
	c, err := alex.svc.Leave(gid)
	if err != nil || c.Status != model.ChatActive {
		t.Fatalf("leave right away: %s %v", c.Status, err)
	}
	if got := countLines(notices(t, alex, gid), "You'll leave the group once bo's tmx has taken it over"); got != 1 {
		t.Fatalf("alex's lines: %q", notices(t, alex, gid))
	}
	hold.Store(false)
	bo.svc.kick(alex.node.ID(), true)
	eventually(t, "alex leaves after bo's state", func() bool {
		c, _ := alex.svc.Chat(gid)
		return c.Status == model.ChatLeft
	})
	eventually(t, "bo and cleo see alex left", func() bool {
		b, _ := bo.svc.Chat(gid)
		c, _ := cleo.svc.Chat(gid)
		return len(current(b.Members)) == 2 && len(current(c.Members)) == 2
	})

	// Leaving twice while waiting leaves at once.
	alex2, bo2, cleo2, gid2 := group(t, p2ptest.NewNetwork())
	bo2.node.SetSendHook(func(peer string, env p2p.Envelope) error {
		if peer == alex2.node.ID() && env.Type == TypeGroup {
			return p2p.ErrUnreachable
		}
		return nil
	})
	handOver(t, alex2, bo2, gid2, alex2, bo2, cleo2)
	alex2.svc.Leave(gid2)
	if c, _ := alex2.svc.Leave(gid2); c.Status != model.ChatLeft {
		t.Fatalf("second leave: %s", c.Status)
	}
}

// Someone whose pairing with Alex completes after the handover hears where the group went.
func TestAdminMovedForLateJoiner(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	handOver(t, alex, bo, gid, alex, bo, cleo)
	// An invite pairing already under way when Alex revoked his invites.
	inv, err := alex.node.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteGroup, GroupID: gid, GroupName: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	dave := newSide(t, n, "dave")
	if _, err := dave.svc.Join(context.Background(), inv.Link); err != nil {
		t.Fatal(err)
	}
	eventually(t, "dave hears the group moved", func() bool {
		c, _ := dave.svc.Chat(gid)
		return c.Status == model.ChatRemoved && countLines(notices(t, dave, gid), "alex handed the group to bo; ask bo for an invite.") == 1
	})
	if c, _ := alex.svc.Chat(gid); len(current(c.Members)) != 3 {
		t.Fatalf("alex added dave: %+v", c.Members)
	}
}

// The chain is kept in the database: after a restart Bo hands over with the certificate that
// made him admin plus his own. It keeps the last 8 certificates.
func TestAdminChainSurvivesRestartAndIsTrimmed(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	handOver(t, alex, bo, gid, alex, bo, cleo)
	eventually(t, "bo's takeover everywhere", func() bool { return version(alex, gid) == version(bo, gid) && version(cleo, gid) == version(bo, gid) })
	bo.svc.Close()
	svc, err := New(Options{Node: bo.node, Store: bo.st, Agents: bo.ag, Name: func() string { return "bo" },
		RetryMin: 30 * time.Millisecond, RetryMax: 150 * time.Millisecond, SendWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	bo.svc = svc
	handOver(t, bo, cleo, gid, alex, bo, cleo)
	p := cleo.svc.proofOf(gid)
	if len(p) != 2 || p[0].From != alex.node.ID() || p[1].From != bo.node.ID() {
		t.Fatalf("cleo's proof after bo's restart: %d certs", len(p))
	}
	// Eight more handovers around the group: ten in all, the last 8 kept.
	sides := []*side{alex, bo, cleo}
	cur := 2 // cleo
	for range 8 {
		next := (cur + 1) % 3
		eventually(t, "the takeover is everywhere", func() bool {
			return version(sides[0], gid) == version(sides[1], gid) && version(sides[1], gid) == version(sides[2], gid)
		})
		handOver(t, sides[cur], sides[next], gid, sides...)
		cur = next
	}
	for _, s := range sides {
		if p := s.svc.proofOf(gid); len(p) != maxAdminProof {
			t.Fatalf("%s keeps %d certificates", s.name, len(p))
		}
	}
	name := "Still going"
	if _, err := sides[cur].svc.Update(gid, ChatPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	for _, s := range sides {
		eventually(t, s.name+" sees the rename", func() bool {
			c, _ := s.svc.Chat(gid)
			return c.Name == name
		})
	}
}

// A member that missed more handovers than the chain keeps cannot check the new admin: it
// refuses the state and tells its owner once.
func TestAdminTooFarBehind(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, _, cleo, gid := group(t, n)
	v := version(cleo, gid)
	signers := []*p2ptest.Fake{alex.node}
	for i := 1; i <= 9; i++ {
		signers = append(signers, n.NewNode(fmt.Sprintf("x%d", i)))
	}
	var chain []p2p.Envelope
	for i := 1; i < len(signers); i++ {
		chain = append(chain, cert(t, signers[i-1], gid, v+int64(i), signers[i].ID()))
	}
	last := signers[len(signers)-1].ID()
	state := func(proof []p2p.Envelope) p2p.Envelope {
		return stateFrom(t, last, gid, groupBody{V: WireVersion, Name: "Elsewhere", Version: v + 20,
			Members: wireMembers(last, last, cleo.node.ID()), AdminProof: proof})
	}
	// Nine certificates: too long. The last eight start after cleo's version, without alex.
	if err := cleo.svc.onEnvelope(state(chain)); err == nil {
		t.Fatal("accepted a chain of 9")
	}
	for range 2 {
		if err := cleo.svc.onEnvelope(state(chain[1:])); err == nil {
			t.Fatal("accepted a chain without the admin cleo knows")
		}
	}
	if got := countLines(notices(t, cleo, gid), "Ask the admin to invite you again."); got != 1 {
		t.Fatalf("cleo's lines: %q", notices(t, cleo, gid))
	}
	if adminOf(cleo, gid) != alex.node.ID() || version(cleo, gid) != v {
		t.Fatal("cleo's member list changed")
	}
}

// Receipts tell who understands handovers (kept across a restart). A member's tmx that missed
// a handover (it was an older tmx then) gets the current state as soon as it shows that it
// understands them, and catches up.
func TestAdminCapsAndCatchUp(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	handover := func(s *side, peer string) bool {
		c, _ := s.svc.Chat(gid)
		for _, m := range c.Members {
			if m.PeerID == peer {
				return m.Handover
			}
		}
		return false
	}
	if handover(alex, bo.node.ID()) || !handover(alex, alex.node.ID()) {
		t.Fatal("handover support known before any receipt")
	}
	// Cleo's tmx drops group states (as an older one would refuse them) during the handover.
	drop := func(peer string, env p2p.Envelope) error {
		if peer == cleo.node.ID() && env.Type == TypeGroup {
			return p2ptest.Dropped
		}
		return nil
	}
	alex.node.SetSendHook(drop)
	bo.node.SetSendHook(drop)
	handOver(t, alex, bo, gid, alex, bo)
	eventually(t, "bo's takeover sent", func() bool { return version(alex, gid) == version(bo, gid) })
	alex.node.SetSendHook(nil)
	bo.node.SetSendHook(nil)
	if adminOf(cleo, gid) != alex.node.ID() {
		t.Fatal("cleo learned of the handover")
	}
	if _, err := bo.svc.Send(owner, gid, "bo here, running it now"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bo knows both understand handovers", func() bool {
		return handover(bo, alex.node.ID()) && handover(bo, cleo.node.ID())
	})
	eventually(t, "cleo catches up from bo's state", func() bool { return adminOf(cleo, gid) == bo.node.ID() })
	// Kept across a restart.
	bo.svc.Close()
	svc, err := New(Options{Node: bo.node, Store: bo.st, Agents: bo.ag, Name: func() string { return "bo" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	bo.svc = svc
	if !handover(bo, cleo.node.ID()) {
		t.Fatal("forgot cleo's capabilities after a restart")
	}
	// Receipts carry the capability on the wire.
	found := false
	for _, x := range cleo.node.SentLog() {
		var b receiptBody
		if x.Env.Type == TypeReceipt && json.Unmarshal(x.Env.Body, &b) == nil && len(b.Caps) > 0 && b.Caps[0] == capAdmin {
			found = true
		}
	}
	if !found {
		t.Fatal("no receipt carried caps")
	}
}

// API-level errors of OfferAdmin: not the admin, not a member, a DM, yourself.
func TestOfferAdminErrors(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	dm := pairDM(t, bo, cleo)
	for _, tc := range []struct {
		s    *side
		chat string
		peer string
		kind fleet.Kind
	}{
		{bo, gid, cleo.node.ID(), fleet.KindForbidden},
		{alex, gid, "peer-nobody", fleet.KindNotFound},
		{alex, "g-unknown", bo.node.ID(), fleet.KindNotFound},
		{bo, dm, cleo.node.ID(), fleet.KindInvalid},
		{alex, gid, alex.node.ID(), fleet.KindInvalid},
	} {
		if _, err := tc.s.svc.OfferAdmin(tc.chat, AdminRequest{PeerID: tc.peer}); fleet.KindOf(err) != tc.kind {
			t.Errorf("%s offers %s in %s: %v, want %v", tc.s.name, tc.peer, tc.chat, err, tc.kind)
		}
	}
	if g, name, err := alex.svc.AdminTarget(gid, bo.node.ID()); err != nil || g != "Trip" || name != "bo" {
		t.Fatalf("target: %q %q %v", g, name, err)
	}
	if !errors.Is(func() error { _, err := (&Service{}).OfferAdmin(gid, AdminRequest{}); return err }(), ErrOff) {
		t.Fatal("offer without a node")
	}
}

// Cleo left while Alex ran the group and rejoins through Bo's invite after the handover: her
// old rows still name Alex as admin, the state from Bo (her inviter) is taken all the same.
func TestAdminRejoinThroughNewAdmin(t *testing.T) {
	n := p2ptest.NewNetwork()
	alex, bo, cleo, gid := group(t, n)
	if _, err := cleo.svc.Leave(gid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cleo gone", func() bool {
		c, _ := alex.svc.Chat(gid)
		return len(current(c.Members)) == 2
	})
	handOver(t, alex, bo, gid, alex, bo)
	inv, err := bo.svc.CreateInvite(InviteRequest{Kind: p2p.InviteGroup, GroupID: gid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cleo.svc.Join(context.Background(), inv.Link); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cleo back with bo as admin", func() bool {
		c, _ := cleo.svc.Chat(gid)
		return c.Status == model.ChatActive && adminOf(cleo, gid) == bo.node.ID() && len(current(c.Members)) == 3
	})
}

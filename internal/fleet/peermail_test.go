package fleet

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

// fakePeers is a PeerSender: hosts it knows, and a scripted result per send.
type fakePeers struct {
	mu    sync.Mutex
	hosts map[string]bool
	sent  []PeerOutbound
	errs  []error // returned in order; nil when exhausted
}

func (p *fakePeers) Known(host string) bool { return p.hosts[host] }

func (p *fakePeers) Send(host string, m PeerOutbound) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, m)
	if len(p.errs) == 0 {
		return nil
	}
	err := p.errs[0]
	p.errs = p.errs[1:]
	return err
}

func (p *fakePeers) Sent() []PeerOutbound {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PeerOutbound(nil), p.sent...)
}

func fastPeerRetry(t *testing.T) {
	b, m, pmin := peerBackoffBase, peerBackoffMax, peerPollMin
	peerBackoffBase, peerBackoffMax, peerPollMin = 20*time.Millisecond, 40*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { peerBackoffBase, peerBackoffMax, peerPollMin = b, m, pmin })
}

// peerEnv: a started fleet ("testbox") with a lead, its worker, and the peer "hostc".
func peerEnv(t *testing.T) (*env, *Fleet, *fakePeers, model.Agent, Caller) {
	t.Helper()
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	peers := &fakePeers{hosts: map[string]bool{"hostc": true}}
	f.SetPeerSender(peers, "testbox")
	return e, f, peers, lead, callerFor(t, f, e.chat, lead.ID)
}

func inbound(id, body string) PeerInbound {
	return PeerInbound{Peer: "hostc", ID: id, Sender: "bob", SenderHost: "hostc", To: "alpha-lead", Body: body}
}

func TestPeerInboundDeliveredAndIdempotent(t *testing.T) {
	e, f, _, lead, _ := peerEnv(t)
	lp := e.chat.Last(lead.ID)
	lp.State(model.StateIdle)
	waitState(t, f, "alpha-lead", model.StateIdle)
	m, dup, err := f.DeliverPeer(inbound("0123456789ab", "hello from hostc"))
	if err != nil || dup {
		t.Fatalf("deliver: %v %v", dup, err)
	}
	if m.From != PeerParty("bob", "hostc") || m.To != lead.ID {
		t.Fatalf("message: %+v", m)
	}
	eventually(t, "delivered", func() bool {
		s := lp.Sent()
		return len(s) >= 2 && s[len(s)-1] == "[message from bob@hostc (other machine)] hello from hostc"
	})
	entries, _ := f.Timeline("alpha-lead", 0, 0)
	labelled := false
	for _, en := range entries {
		labelled = labelled || en.Kind == model.EntryMessageIn && en.Meta["from_name"] == "bob@hostc"
	}
	if !labelled {
		t.Fatal("timeline does not name the sender bob@hostc")
	}
	again, dup, err := f.DeliverPeer(inbound("0123456789ab", "hello from hostc"))
	if err != nil || !dup || again.ID != m.ID {
		t.Fatalf("duplicate: %+v %v %v", again, dup, err)
	}
	if _, _, err := f.DeliverPeer(inbound("0123456789ab", "something else")); KindOf(err) != KindConflict {
		t.Fatalf("changed body: %v", err)
	}
	// name@<this machine> is local.
	in := inbound("0123456789ac", "second")
	in.To = "alpha-lead@testbox"
	if _, _, err := f.DeliverPeer(in); err != nil {
		t.Fatal(err)
	}
}

func TestPeerInboundRefusals(t *testing.T) {
	_, f, _, _, _ := peerEnv(t)
	spoof := inbound("0123456789ab", "hi")
	spoof.SenderHost = "hosta" // hostc vouching for an agent of hosta
	if _, _, err := f.DeliverPeer(spoof); KindOf(err) != KindForbidden {
		t.Fatalf("spoofed host: %v", err)
	}
	for _, id := range []string{"short", "0123456789AB", "0123456789abz", strings.Repeat("a", 33)} {
		if _, _, err := f.DeliverPeer(inbound(id, "hi")); KindOf(err) != KindInvalid {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	bad := inbound("0123456789ab", "hi")
	bad.Sender = "Bob Smith"
	if _, _, err := f.DeliverPeer(bad); KindOf(err) != KindInvalid {
		t.Fatalf("sender: %v", err)
	}
	other := inbound("0123456789ab", "hi")
	other.To = "alpha-lead@hosta"
	if _, _, err := f.DeliverPeer(other); KindOf(err) != KindNotFound {
		t.Fatalf("other machine's agent: %v", err)
	}
	if _, _, err := f.DeliverPeer(inbound("0123456789ab", strings.Repeat("x", MaxBodyBytes+2048))); KindOf(err) != KindInvalid {
		t.Fatalf("long: %v", err)
	}
}

func TestPeerInboundQueueLimit(t *testing.T) {
	e, f, _, lead, _ := peerEnv(t)
	e.chat.Last(lead.ID).State(model.StateWorking) // busy: mail stays queued
	waitState(t, f, "alpha-lead", model.StateWorking)
	for i := 0; i < MaxQueued; i++ {
		id := strings.Repeat("0", 10) + string("0123456789abcdef"[i/16]) + string("0123456789abcdef"[i%16])
		if _, _, err := f.DeliverPeer(inbound(id, "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.DeliverPeer(inbound("ffffffffffff", "x")); KindOf(err) != KindTooMany {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestPeerReplyRoundTrip(t *testing.T) {
	_, f, peers, lead, lc := peerEnv(t)
	in, _, err := f.DeliverPeer(inbound("0123456789ab", "can you check?"))
	if err != nil {
		t.Fatal(err)
	}
	// A reply to peer mail goes back to that agent with the wire reply_to.
	out, err := f.SendMessage(lc, SendRequest{ReplyTo: in.ID, Body: "checked"})
	if err != nil {
		t.Fatal(err)
	}
	if out.To != PeerParty("bob", "hostc") || out.State != model.MsgDelivered || out.Receipt != "accepted" {
		t.Fatalf("reply: %+v", out)
	}
	s := peers.Sent()
	if len(s) != 1 || s[0].Sender != "alpha-lead" || s[0].SenderHost != "testbox" || s[0].To != "bob" ||
		s[0].ReplyTo == nil || *s[0].ReplyTo != "0123456789ab" || s[0].Body != "checked" ||
		s[0].SHA256 != bodySHA("checked") || !peerWireRx.MatchString(s[0].ID) || len(s[0].ID) != 12 {
		t.Fatalf("wire: %+v", s)
	}
	// Their answer to our message links to our local ID.
	ans := inbound("aaaaaaaaaaaa", "thanks")
	ans.ReplyTo = s[0].ID
	back, _, err := f.DeliverPeer(ans)
	if err != nil || back.ReplyTo != out.ID {
		t.Fatalf("answer: %+v %v", back, err)
	}
	// Our own wire ID coming back as a new message is a conflict, not a delivery.
	if _, _, err := f.DeliverPeer(inbound(s[0].ID, "checked")); KindOf(err) != KindConflict {
		t.Fatalf("reused outbound id: %v", err)
	}
	// The sending agent's mailbox lists the outbound copy.
	msgs, _ := f.Messages(OwnerCaller, lead.ID, 0)
	found := false
	for _, m := range msgs {
		found = found || m.ID == out.ID
	}
	if !found {
		t.Fatal("outbound mail missing from the sender's messages")
	}
}

func TestPeerSendAddresses(t *testing.T) {
	e, f, peers, lead, lc := peerEnv(t)
	w, _ := f.LaunchAgent(lc, LaunchRequest{Name: "w1"})
	wc := callerFor(t, f, e.chat, w.ID)
	// name@<this machine> is local mail.
	m, err := f.SendMessage(wc, SendRequest{To: "alpha-lead@testbox", Body: "local"})
	if err != nil || m.To != lead.ID {
		t.Fatalf("local: %+v %v", m, err)
	}
	if _, err := f.SendMessage(wc, SendRequest{To: "bob@nowhere", Body: "x"}); KindOf(err) != KindNotFound {
		t.Fatalf("unknown host: %v", err)
	}
	if len(peers.Sent()) != 0 {
		t.Fatal("sent to an unknown host")
	}
	// The owner's mail goes out as "operator".
	if _, err := f.SendMessage(OwnerCaller, SendRequest{To: "bob@hostc", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if s := peers.Sent(); len(s) != 1 || s[0].Sender != PeerOperator || s[0].ReplyTo != nil {
		t.Fatalf("owner: %+v", s)
	}
	// The per-window rate applies to a peer party.
	for i := 1; i < MaxPerWindow; i++ {
		if _, err := f.SendMessage(wc, SendRequest{To: "bob@hostc", Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.SendMessage(wc, SendRequest{To: "bob@hostc", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendMessage(wc, SendRequest{To: "bob@hostc", Body: "x"}); KindOf(err) != KindTooMany {
		t.Fatalf("rate: %v", err)
	}
}

func TestPeerSendRetriedWithSameID(t *testing.T) {
	fastPeerRetry(t)
	_, f, peers, _, lc := peerEnv(t)
	peers.errs = []error{ErrPeerUnreachable, ErrPeerUnreachable}
	m, err := f.SendMessage(lc, SendRequest{To: "bob@hostc", Body: "are you there?"})
	if err != nil {
		t.Fatal(err)
	}
	if m.State != model.MsgQueued {
		t.Fatalf("unreachable: %+v", m)
	}
	eventually(t, "accepted on retry", func() bool {
		got, _ := f.message(m.ID)
		return got.State == model.MsgDelivered && got.Receipt == "accepted"
	})
	s := peers.Sent()
	if len(s) != 3 || s[0].ID != s[1].ID || s[1].ID != s[2].ID {
		t.Fatalf("attempts: %+v", s)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(peers.Sent()); n != 3 {
		t.Fatalf("sent again after acceptance: %d", n)
	}
}

func TestPeerSendRefusedAndGivenUp(t *testing.T) {
	fastPeerRetry(t)
	_, f, peers, lead, lc := peerEnv(t)
	peers.errs = []error{errors.New("403 refused")}
	m, err := f.SendMessage(lc, SendRequest{To: "bob@hostc", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.State != model.MsgFailed {
		t.Fatalf("refused: %+v", m)
	}
	entries, _ := f.Timeline(lead.ID, 0, 0)
	noted := false
	for _, en := range entries {
		noted = noted || en.Kind == model.EntrySystem && strings.Contains(en.Text, "bob@hostc was not delivered")
	}
	if !noted {
		t.Fatal("the sender was not told")
	}
	// Unreachable past the give-up time fails too.
	old := peerGiveUp
	peerGiveUp = 0
	t.Cleanup(func() { peerGiveUp = old })
	peers.errs = []error{ErrPeerUnreachable}
	m, err = f.SendMessage(lc, SendRequest{To: "bob@hostc", Body: "y"})
	if err != nil || m.State != model.MsgFailed {
		t.Fatalf("given up: %+v %v", m, err)
	}
	if pm, _ := f.st.PeerByLocal(m.ID); pm.NextAttemptAt != 0 || pm.Direction != store.PeerOut {
		t.Fatalf("still scheduled: %+v", pm)
	}
}

// security-review #105: no agent may carry a name the Python daemons treat as the owner or the
// system, and nothing but the owner's own mail goes out as "operator".
func TestReservedPeerNamesRefusedAtLaunch(t *testing.T) {
	_, f, _, _, _ := peerEnv(t)
	for _, name := range []string{"operator", "tmx", "system", "owner", "companion", "whatsapp"} {
		_, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: name, Level: model.LevelWorker, Harness: model.HarnessClaude, Cwd: t.TempDir()})
		if KindOf(err) != KindInvalid {
			t.Errorf("launch %q: %v", name, err)
		}
	}
}

func TestAgentNamedOperatorCannotMailPeers(t *testing.T) {
	_, f, peers, lead, c := peerEnv(t)
	// An agent that already carries the name (an older database, say).
	f.mu.Lock()
	f.agents[lead.ID].a.Name = PeerOperator
	f.mu.Unlock()
	if _, err := f.SendMessage(c, SendRequest{To: "bob@hostc", Body: "I am the owner"}); KindOf(err) != KindForbidden {
		t.Fatalf("send as operator: %v", err)
	}
	// Mail already stored under that name never reaches the wire as "operator".
	m := model.Message{ID: model.NewID(), From: lead.ID, To: PeerParty("bob", "hostc"), Body: "forged",
		State: model.MsgQueued, CreatedAt: model.Now()}
	f.mu.Lock()
	err := f.st.PutPeerMessage(m, store.PeerMessage{LocalID: m.ID, Peer: "hostc", WireID: newWireID(),
		Direction: store.PeerOut, SHA256: bodySHA(m.Body), NextAttemptAt: m.CreatedAt})
	f.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	f.sendPeer(m.ID)
	for _, s := range peers.Sent() {
		if s.Sender == PeerOperator {
			t.Fatalf("forged owner mail sent: %+v", s)
		}
	}
	got, err := f.st.GetMessage(m.ID)
	if err != nil || got.State != model.MsgFailed {
		t.Fatalf("forged mail state %v %v", got.State, err)
	}
}

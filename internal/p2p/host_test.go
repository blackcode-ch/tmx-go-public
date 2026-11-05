package p2p

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// testNode is one in-process host on loopback: no DHT, no mDNS, no port mapping.
type testNode struct {
	*Host
	key   crypto.PrivKey
	state string
	name  string
}

func startNode(t *testing.T, name string, key crypto.PrivKey, stateFile string, relays ...string) *testNode {
	t.Helper()
	if key == nil {
		key, _ = testKey(t)
	}
	if stateFile == "" {
		stateFile = filepath.Join(t.TempDir(), "p2p-state.json")
	}
	h, err := New(Options{
		Key: key, StateFile: stateFile, Name: name, Version: "test",
		Relays:        relays,
		ListenAddrs:   []string{"/ip4/127.0.0.1/udp/0/quic-v1", "/ip4/127.0.0.1/tcp/0"},
		AllowLoopback: true, RetryInterval: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Central guard: every test host, however it's built, must only ever be listening on
	// loopback - binding 0.0.0.0/:: triggers the Windows Firewall prompt.
	assertLoopbackMultiaddrs(t, h.h.Network().ListenAddresses())
	assertLoopbackMultiaddrs(t, h.h.Addrs())
	n := &testNode{Host: h, key: key, state: stateFile, name: name}
	t.Cleanup(func() { n.Close() })
	return n
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// waitPeerEvent waits for a PeerEvent about peerID with the wanted reachability (others are skipped).
func waitPeerEvent(t *testing.T, n *testNode, peerID string, online bool) PeerEvent {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev := <-n.PeerEvents():
			if ev.PeerID == peerID && (ev.Reachability != Offline) == online {
				return ev
			}
		case <-deadline:
			t.Fatalf("%s: no PeerEvent online=%v for %s", n.name, online, peerID)
		}
	}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func send(t *testing.T, from, to *testNode, chat, text string) Envelope {
	t.Helper()
	id := fmt.Sprintf("%s-%d", from.name, time.Now().UnixNano())
	if err := from.Send(ctxT(t), to.PeerID(), Envelope{ID: id, Type: "msg", ChatID: chat, Body: []byte(`"` + text + `"`)}); err != nil {
		t.Fatalf("%s → %s: %v", from.name, to.name, err)
	}
	env := recv(t, to.Incoming(), to.name+" incoming")
	if env.ID != id || env.From != from.PeerID() || env.ChatID != chat || string(env.Body) != `"`+text+`"` {
		t.Fatalf("%s got %+v", to.name, env)
	}
	return env
}

func TestPairAndMessage(t *testing.T) {
	alice := startNode(t, "alice", nil, "")
	bob := startNode(t, "bob", nil, "")
	carol := startNode(t, "carol", nil, "")

	// DM invite: single use.
	inv, err := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	if inv.MaxUses != 1 || inv.ExpiresAt-time.Now().UnixMilli() < int64(23*time.Hour/time.Millisecond) {
		t.Fatalf("DM defaults: %+v", inv)
	}
	t.Logf("live-style DM link (%d chars): %s", len(inv.Link), inv.Link)
	pv, err := bob.Preview(inv.Link)
	if err != nil || pv.Name != "alice" || pv.PeerID != alice.PeerID() || pv.Kind != InviteDM {
		t.Fatalf("preview %+v %v", pv, err)
	}

	// Before pairing, Send is refused locally and alice refuses bob's envelopes.
	if err := bob.Send(ctxT(t), alice.PeerID(), Envelope{ID: "x", Type: "msg"}); !errors.Is(err, ErrRejected) {
		t.Fatalf("unpaired send: %v", err)
	}

	got, err := bob.Join(ctxT(t), inv.Link)
	if err != nil {
		t.Fatal(err)
	}
	onAlice := recv(t, alice.Paired(), "alice Paired")
	if got.PeerID != alice.PeerID() || got.Name != "alice" || onAlice.PeerID != bob.PeerID() || onAlice.Name != "bob" ||
		onAlice.InviteID != inv.ID || got.SafetyCode != onAlice.SafetyCode || got.SafetyCode == "" {
		t.Fatalf("paired: bob saw %+v, alice saw %+v", got, onAlice)
	}

	send(t, bob, alice, "dm", "hello alice")
	send(t, alice, bob, "dm", "hi bob")

	// Used up: carol cannot use it, nor can bob be paired twice for a second use. alice has
	// no invite open now, so her connection gater refuses carol (a stranger) outright; Join
	// tells carol why that probably is rather than "unreachable".
	if _, err := carol.Join(ctxT(t), inv.Link); err == nil || errors.Is(err, ErrUnreachable) ||
		!strings.Contains(err.Error(), "used, revoked or has expired") {
		t.Fatalf("second use: %v", err)
	}
	if again, err := bob.Join(ctxT(t), inv.Link); err != nil || again.PeerID != alice.PeerID() {
		t.Fatalf("a retry by the same joiner is free: %v", err)
	}
	if invs := alice.Invites(); len(invs) != 1 || invs[0].Uses != 1 {
		t.Fatalf("invites after use: %+v", invs)
	}

	// Revoked invites stop working. While another invite is open, strangers get past the
	// gater to try it, and handlePair names the reason for a used or revoked one.
	inv2, _ := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if err := alice.RevokeInvite(inv2.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if _, err := carol.Join(ctxT(t), inv2.Link); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := carol.Join(ctxT(t), inv.Link); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("second use, another invite open: %v", err)
	}
	_ = alice.RevokeInvite(other.ID)

	// Group invite: multi-use, carries the group, both joiners pair.
	g, err := alice.CreateInvite(InviteOptions{Kind: InviteGroup, GroupID: "grp1", GroupName: "Design", MaxUses: 2})
	if err != nil {
		t.Fatal(err)
	}
	if g.MaxUses != 2 || g.ExpiresAt-time.Now().UnixMilli() < int64(23*time.Hour/time.Millisecond) {
		t.Fatalf("group defaults: %+v", g)
	}
	for _, j := range []*testNode{bob, carol} {
		p, err := j.Join(ctxT(t), g.Link)
		if err != nil {
			t.Fatalf("%s joins group: %v", j.name, err)
		}
		if p.Kind != InviteGroup || p.GroupID != "grp1" || p.GroupName != "Design" {
			t.Fatalf("%s: %+v", j.name, p)
		}
		a := recv(t, alice.Paired(), "alice Paired (group)")
		if a.PeerID != j.PeerID() || a.GroupID != "grp1" || a.InviteID != g.ID {
			t.Fatalf("alice: %+v", a)
		}
	}
	send(t, carol, alice, "grp1", "carol here")
	send(t, alice, carol, "grp1", "welcome carol")

	// bob and carol never paired with each other: refused until they trust each other
	// (vouched for by the group's admin through the chat layer), then they talk directly.
	// Until bob trusts carol, his connection gater refuses her connection: to carol that is
	// "unreachable" (the chat layer queues and retries), no longer bob's ErrRejected.
	if err := carol.Trust(bob.PeerID(), "bob", alice.PeerAddrs(bob.PeerID())); err != nil {
		t.Fatal(err)
	}
	if err := carol.Send(ctxT(t), bob.PeerID(), Envelope{ID: "c1", Type: "msg", ChatID: "grp1"}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("bob has not allowed carol yet: %v", err)
	}
	if err := bob.Trust(carol.PeerID(), "carol", alice.PeerAddrs(carol.PeerID())); err != nil {
		t.Fatal(err)
	}
	send(t, carol, bob, "grp1", "hi bob, from carol")
	send(t, bob, carol, "grp1", "hi carol")

	// Forget: alice drops bob and closes his connections before returning. No invite is
	// open, so her gater refuses him from then on: to bob that is "unreachable", like carol
	// above, and nothing he sends reaches alice. (Forget used to leave the connection open
	// until sweepStrangers' next tick, so bob's Send got ErrRejected or a mid-stream close,
	// whichever came first.)
	alice.Forget(bob.PeerID())
	if err := bob.Send(ctxT(t), alice.PeerID(), Envelope{ID: "f1", Type: "msg"}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("forgotten peer: %v", err)
	}
	select {
	case env := <-alice.Incoming():
		t.Fatalf("alice received from a forgotten peer: %+v", env)
	default:
	}
}

// TestJoinThenMessageRejectsOutsider is the real two-host invite→join path end to end on
// loopback only: two libp2p hosts (QUIC + TCP on 127.0.0.1), mDNS/DHT/relay all off (the
// zero value of startNode's Options). A creates an invite, B joins it, a signed envelope
// A→B is delivered and accepted; a third host that was never invited and never joined gets
// ErrRejected, both from its own unpaired Send and from a raw stream straight to B's
// /tmx/msg/1.0.0 handler (so the rejection is B's, not just outsider's local isPeer check).
func TestJoinThenMessageRejectsOutsider(t *testing.T) {
	a := startNode(t, "a", nil, "")
	b := startNode(t, "b", nil, "")
	outsider := startNode(t, "outsider", nil, "")

	inv, err := a.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Join(ctxT(t), inv.Link); err != nil {
		t.Fatalf("b joins a: %v", err)
	}
	recv(t, a.Paired(), "a Paired")

	send(t, a, b, "dm", "hello b")
	send(t, b, a, "dm", "hi a")

	// outsider never saw the invite: its own Send refuses locally (not a paired peer).
	if err := outsider.Send(ctxT(t), b.PeerID(), Envelope{ID: "o1", Type: "msg"}); !errors.Is(err, ErrRejected) {
		t.Fatalf("outsider → b: %v", err)
	}

	// Even a hand-crafted, correctly signed envelope over a raw stream to b is refused: b
	// does not know outsider as a peer, regardless of what outsider claims.
	env, err := outsider.Sign(Envelope{ID: "o2", Type: "msg", Body: []byte(`"forged"`)})
	if err != nil {
		t.Fatal(err)
	}
	bPeer, err := peer.Decode(b.PeerID())
	if err != nil {
		t.Fatal(err)
	}
	// With no invite open, b's connection gater refuses the outsider before any protocol
	// runs: it cannot even open a stream.
	_ = outsider.h.Connect(ctxT(t), peer.AddrInfo{ID: bPeer, Addrs: b.h.Addrs()})
	if s, err := outsider.h.NewStream(ctxT(t), bPeer, protoID(ProtoMsg)); err == nil {
		s.Reset()
		t.Fatal("b's gater let a stranger open a stream")
	}
	if b.h.Network().Connectedness(outsider.h.ID()) == network.Connected {
		t.Fatal("b kept a connection from a stranger")
	}
	// While b has an invite open, anyone may connect (to try it): then b's /tmx/msg handler
	// itself refuses the outsider. outsider needs b's address to even open a connection (it
	// was never paired or trusted, so it never learned one through tmx; here it just happens
	// to know it, e.g. from the network - the point is what b does once a stream is open).
	if _, err := b.CreateInvite(InviteOptions{Kind: InviteDM}); err != nil {
		t.Fatal(err)
	}
	if err := outsider.h.Connect(ctxT(t), peer.AddrInfo{ID: bPeer, Addrs: b.h.Addrs()}); err != nil {
		t.Fatalf("outsider connects to b (bare libp2p connection, not paired): %v", err)
	}
	s, err := outsider.h.NewStream(ctxT(t), bPeer, protoID(ProtoMsg))
	if err != nil {
		t.Fatalf("outsider opens stream to b: %v", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeMsg(s, env); err != nil {
		t.Fatalf("outsider writes envelope: %v", err)
	}
	var a2 ack
	if err := readMsg(s, maxPairMsg, &a2); err != nil {
		t.Fatalf("outsider reads ack: %v", err)
	}
	if a2.OK {
		t.Fatal("b accepted an envelope from a never-invited, never-joined outsider")
	}
	if a2.Error != "not paired with this tmx" || a2.Retry {
		t.Fatalf("b's rejection of outsider: got error=%q retry=%v, want the not-paired error with no retry (busy/shutdown must not pass this test)", a2.Error, a2.Retry)
	}
}

func TestUnreachableAndReconnect(t *testing.T) {
	alice := startNode(t, "alice", nil, "")
	bobKey, _ := testKey(t)
	bobState := filepath.Join(t.TempDir(), "bob-state.json")
	bob := startNode(t, "bob", bobKey, bobState)

	inv, _ := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if _, err := bob.Join(ctxT(t), inv.Link); err != nil {
		t.Fatal(err)
	}
	recv(t, alice.Paired(), "alice Paired")
	if r := alice.Reachable(bob.PeerID()); r != Direct {
		t.Fatalf("reachability after pairing: %s", r)
	}

	// bob goes away: Send fails fast with ErrUnreachable (the chat layer queues it).
	bobID := bob.PeerID()
	if err := bob.Close(); err != nil {
		t.Fatal(err)
	}
	waitPeerEvent(t, alice, bobID, false)
	if r := alice.Reachable(bobID); r != Offline {
		t.Fatalf("reachability after close: %s", r)
	}
	start := time.Now()
	err := alice.Send(ctxT(t), bobID, Envelope{ID: "q1", Type: "msg", Body: []byte(`"queued"`)})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("offline send: %v", err)
	}
	t.Logf("offline send failed after %s: %v", time.Since(start).Round(time.Millisecond), err)

	// bob restarts with the same key and state but new ports: it redials alice from its
	// saved addresses, and alice sees bob online (and can send again).
	bob2 := startNode(t, "bob", bobKey, bobState)
	if bob2.PeerID() != bobID {
		t.Fatal("peer ID must survive a restart")
	}
	if len(bob2.Peers()) != 1 || bob2.Peers()[0] != alice.PeerID() {
		t.Fatalf("paired peers are persisted: %v", bob2.Peers())
	}
	ev := waitPeerEvent(t, alice, bobID, true)
	if ev.Reachability != Direct {
		t.Fatalf("event %+v", ev)
	}
	send(t, alice, bob2, "dm", "you're back")
	send(t, bob2, alice, "dm", "I am")
}

func TestStatusAndInviteLimits(t *testing.T) {
	n := startNode(t, "solo", nil, "")
	st := n.Status()
	if !st.Enabled || st.PeerID != n.PeerID() || len(st.Addrs) == 0 || st.Reachability != Direct {
		t.Fatalf("status %+v", st)
	}
	n.SetDisplayName("  New Name  ")
	inv, err := n.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	if pv, _ := n.Preview(inv.Link); pv.Name != "New Name" {
		t.Fatalf("name in invite: %q", pv.Name)
	}
	if _, err := n.Join(ctxT(t), inv.Link); err == nil || !strings.Contains(err.Error(), "your own") {
		t.Fatalf("joining own invite: %v", err)
	}
	for _, o := range []InviteOptions{
		{Kind: "nope"},
		{Kind: InviteGroup},               // no group ID
		{Kind: InviteDM, TTLMillis: 1000}, // < 1 min
		{Kind: InviteDM, TTLMillis: int64(100 * 24 * time.Hour / time.Millisecond)}, // > 90 days
	} {
		if _, err := n.CreateInvite(o); err == nil {
			t.Errorf("CreateInvite(%+v) accepted", o)
		}
	}
	if err := n.Send(ctxT(t), n.PeerID(), Envelope{ID: "a", Type: "b"}); err == nil {
		t.Error("send to self accepted")
	}
	big := make([]byte, MaxBody+1)
	if err := n.Send(ctxT(t), n.PeerID()+"x", Envelope{ID: "a", Type: "b", Body: big}); err == nil {
		t.Error("bad peer ID accepted")
	}
}

// TestTrust: A pairs with B and with C through invites; B and C trust each other (as the
// chat layer does for co-members vouched for by A) and exchange envelopes directly, even
// while A is gone; an untrusted D is still refused; Forget undoes trust.
func TestTrust(t *testing.T) {
	a := startNode(t, "a", nil, "")
	b := startNode(t, "b", nil, "")
	c := startNode(t, "c", nil, "")
	d := startNode(t, "d", nil, "")
	for _, j := range []*testNode{b, c, d} {
		inv, err := a.CreateInvite(InviteOptions{Kind: InviteDM})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Join(ctxT(t), inv.Link); err != nil {
			t.Fatalf("%s joins a: %v", j.name, err)
		}
		recv(t, a.Paired(), "a Paired")
	}
	if err := b.Trust("not-a-peer", "x", nil); err == nil {
		t.Fatal("Trust accepted a bad peer ID")
	}
	// The admin passes on the addresses it knows; a bad address is ignored, not fatal.
	if err := b.Trust(c.PeerID(), "c", append(a.PeerAddrs(c.PeerID()), "garbage")); err != nil {
		t.Fatal(err)
	}
	if err := c.Trust(b.PeerID(), "c's name for b", a.PeerAddrs(b.PeerID())); err != nil {
		t.Fatal(err)
	}
	a.Close() // the admin goes offline: b and c do not need it
	send(t, b, c, "grp", "b to c, directly")
	send(t, c, b, "grp", "c to b, directly")
	if r := b.Reachable(c.PeerID()); r != Direct {
		t.Fatalf("b→c reachability %s", r)
	}

	// d trusts b on its side, but b never trusted d: b's connection gater refuses d's
	// connection (b has no invite open and does not know d), so d sees b as unreachable.
	if err := d.Trust(b.PeerID(), "b", c.PeerAddrs(b.PeerID())); err != nil {
		t.Fatal(err)
	}
	if err := d.Send(ctxT(t), b.PeerID(), Envelope{ID: "d1", Type: "msg"}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("untrusted d → b: %v", err)
	}

	// Trust is persisted with the paired peers; Forget undoes it.
	found := false
	for _, id := range b.Peers() {
		found = found || id == c.PeerID()
	}
	if !found {
		t.Fatalf("b's peers %v lack c", b.Peers())
	}
	// b closes c's connection as it forgets c, and its gater refuses c from then on.
	b.Forget(c.PeerID())
	if err := c.Send(ctxT(t), b.PeerID(), Envelope{ID: "c9", Type: "msg"}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("after Forget: %v", err)
	}
}

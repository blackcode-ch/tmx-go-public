package p2p

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

// fakeNet is a gateNet with fixed connections and peerstore addresses.
type fakeNet struct {
	cs []gateConn
	as map[peer.ID][]ma.Multiaddr
}

func (f fakeNet) conns() []gateConn              { return f.cs }
func (f fakeNet) addrs(p peer.ID) []ma.Multiaddr { return f.as[p] }

type connAddrs struct{ local, remote ma.Multiaddr }

func (c connAddrs) LocalMultiaddr() ma.Multiaddr  { return c.local }
func (c connAddrs) RemoteMultiaddr() ma.Multiaddr { return c.remote }

// bareHost is a Host without libp2p: enough for the gater's decisions.
func bareHost() *Host {
	return &Host{st: &state{Peers: map[string]*peerRec{}}, log: slog.New(slog.DiscardHandler)}
}

func newPeerID(t *testing.T) peer.ID {
	t.Helper()
	_, id := testKey(t)
	return id
}

func TestGaterInvitesAndPeers(t *testing.T) {
	n := bareHost()
	stranger, friend := newPeerID(t), newPeerID(t)
	n.st.Peers[friend.String()] = &peerRec{PeerID: friend.String()}
	from := ma.StringCast("/ip4/192.168.1.20/udp/4001/quic-v1")
	now := nowMs()
	future, past := now+int64(time.Hour/time.Millisecond), now-1000

	if !n.admit(friend, from, nil) {
		t.Fatal("a paired peer was refused")
	}
	if n.admit(stranger, from, nil) {
		t.Fatal("a stranger was let in with no invite open")
	}
	for _, c := range []struct {
		name string
		in   inviteRec
		ok   bool
	}{
		{"open DM", inviteRec{Invite: Invite{ExpiresAt: future, MaxUses: 1}}, true},
		{"open group, unlimited", inviteRec{Invite: Invite{ExpiresAt: future, Uses: 9}}, true},
		{"expired", inviteRec{Invite: Invite{ExpiresAt: past, MaxUses: 1}}, false},
		{"used up", inviteRec{Invite: Invite{ExpiresAt: future, MaxUses: 1, Uses: 1}, EndedAt: now}, false},
		{"revoked", inviteRec{Invite: Invite{ExpiresAt: future, Revoked: true}, EndedAt: now}, false},
	} {
		in := c.in
		in.CreatedAt = now
		n.st.Invites = []*inviteRec{&in}
		if got := n.admit(stranger, from, nil); got != c.ok {
			t.Errorf("%s invite: stranger admitted = %v, want %v", c.name, got, c.ok)
		}
	}

	// A pending pairing admits only the peer that already supplied its proof.
	n.st.Invites = nil
	n.pairing = map[peer.ID]int{friend: 1}
	if n.admit(stranger, from, nil) {
		t.Fatal("a stranger was admitted while another peer's pairing is completing")
	}
	n.pairing = nil

	// The libp2p hook: outbound is never gated; inbound goes through admit (the host is not
	// ready here, so only the peer/invite rules apply).
	g := connGater{n}
	ca := connAddrs{local: ma.StringCast("/ip4/192.168.1.2/udp/4001/quic-v1"), remote: from}
	if !g.InterceptSecured(network.DirOutbound, stranger, ca) {
		t.Fatal("an outbound connection was gated")
	}
	if !g.InterceptPeerDial(stranger) || !g.InterceptAddrDial(stranger, from) || !g.InterceptAccept(ca) {
		t.Fatal("dials and accepts are not gated (the peer is not known yet)")
	}
	if g.InterceptSecured(network.DirInbound, stranger, ca) {
		t.Fatal("an inbound stranger got through InterceptSecured")
	}
	if !g.InterceptSecured(network.DirInbound, friend, ca) {
		t.Fatal("an inbound paired peer was refused by InterceptSecured")
	}
}

// TestGaterDialledPeer: a peer this node dialled itself (e.g. the inviter it is joining) may
// connect back, also when the outbound connection is relayed.
func TestGaterDialledPeer(t *testing.T) {
	n := bareHost()
	inviter, other := newPeerID(t), newPeerID(t)
	relayID := newPeerID(t)
	circuit := ma.StringCast("/ip4/8.8.8.8/udp/4001/quic-v1/p2p/" + relayID.String() + "/p2p-circuit")
	v := fakeNet{cs: []gateConn{{peer: inviter, remote: circuit, outbound: true}, {peer: other, remote: ma.StringCast("/ip4/9.9.9.9/tcp/1"), outbound: false}}}
	from := ma.StringCast("/ip4/192.168.1.30/udp/5000/quic-v1")
	if !n.admit(inviter, from, v) {
		t.Fatal("the peer we dialled could not connect back")
	}
	if n.admit(other, from, v) {
		t.Fatal("a peer that only connected to us (unpaired) was let in again")
	}
}

// TestGaterAutoNAT: AutoNAT servers dial back from a random peer ID at their own IP
// (go-libp2p v0.50: a throwaway dialer host). Those get in; nothing else does by IP.
func TestGaterAutoNAT(t *testing.T) {
	n := bareHost()
	n.opts.DHT = true
	server, friend, stranger, dialBack := newPeerID(t), newPeerID(t), newPeerID(t), newPeerID(t)
	relayID := newPeerID(t)
	n.st.Peers[friend.String()] = &peerRec{PeerID: friend.String()}
	v := fakeNet{
		cs: []gateConn{
			{peer: server, remote: ma.StringCast("/ip4/8.8.8.8/udp/4001/quic-v1"), outbound: true},     // a DHT peer we dialled
			{peer: relayID, remote: ma.StringCast("/ip4/1.1.1.1/tcp/4001"), outbound: true},            // a relay we dialled
			{peer: friend, remote: ma.StringCast("/ip4/2.2.2.2/udp/30000/quic-v1"), outbound: false},   // a contact that dialled us
			{peer: stranger, remote: ma.StringCast("/ip4/3.3.3.3/udp/30000/quic-v1"), outbound: false}, // let in by an invite
			{peer: newPeerID(t), remote: ma.StringCast("/ip4/192.168.1.7/tcp/4001"), outbound: true},   // LAN peer we dialled
			{peer: newPeerID(t), remote: ma.StringCast("/ip4/127.0.0.1/tcp/4001"), outbound: true},     // loopback peer we dialled
			{peer: newPeerID(t), remote: ma.StringCast("/ip4/4.4.4.4/tcp/1/p2p/" + relayID.String() + "/p2p-circuit"), outbound: true},
		},
		as: map[peer.ID][]ma.Multiaddr{
			server: {ma.StringCast("/ip4/8.8.8.8/tcp/4001"), ma.StringCast("/ip6/2001:4860:4860::8888/udp/4001/quic-v1")},
		},
	}
	for _, c := range []struct {
		name, from string
		ok         bool
	}{
		{"dial-back from the server's IP (v1, v2 same family)", "/ip4/8.8.8.8/tcp/53000", true},
		{"dial-back over QUIC from another port", "/ip4/8.8.8.8/udp/53001/quic-v1", true},
		{"v2 dial-back from the server's other family", "/ip6/2001:4860:4860::8888/tcp/53002", true},
		{"from a paired peer's IP", "/ip4/2.2.2.2/tcp/53003", true},
		{"from a relay we use, directly", "/ip4/1.1.1.1/tcp/53004", true},
		{"through the relay we use", "/ip4/1.1.1.1/tcp/4001/p2p/" + relayID.String() + "/p2p-circuit", false},
		{"through a relay at the server's IP", "/ip4/8.8.8.8/udp/4001/quic-v1/p2p/" + relayID.String() + "/p2p-circuit", false},
		{"from an IP nobody vouches for", "/ip4/9.9.9.9/tcp/53005", false},
		{"from the IP of an unpaired peer that dialled us", "/ip4/3.3.3.3/tcp/53006", false},
		{"from a LAN IP we dialled", "/ip4/192.168.1.7/tcp/53007", false},
		{"from loopback", "/ip4/127.0.0.1/tcp/53008", false},
		{"from the IP behind an outbound relayed connection", "/ip4/4.4.4.4/tcp/53009", false},
	} {
		if got := n.admit(dialBack, ma.StringCast(c.from), v); got != c.ok {
			t.Errorf("%s (%s): admitted = %v, want %v", c.name, c.from, got, c.ok)
		}
	}
}

// relayNode is a circuit relay v2 on loopback (a stand-in for a public IPFS relay).
func relayNode(t *testing.T) host.Host {
	t.Helper()
	rh, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rh.Close() })
	assertLoopbackMultiaddrs(t, rh.Network().ListenAddresses())
	r, err := relay.New(rh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return rh
}

// circuitLink is inv's link with only a circuit address through rh (no direct address).
func circuitLink(t *testing.T, inv Invite, rh host.Host) (string, ma.Multiaddr) {
	t.Helper()
	p, err := parseLink(inv.Link)
	if err != nil {
		t.Fatal(err)
	}
	circ := ma.StringCast(rh.Addrs()[0].String() + "/p2p/" + rh.ID().String() + "/p2p-circuit")
	p.Addrs = []ma.Multiaddr{circ}
	link, err := p.encode()
	if err != nil {
		t.Fatal(err)
	}
	return link, circ
}

// connected reports whether a holds any connection to b, limited (relayed) ones included.
func connected(a *testNode, b *testNode) bool {
	return len(a.h.Network().ConnsToPeer(b.h.ID())) > 0
}

// disconnect closes a's and b's connections to each other (again, if a redial loop was
// quicker than the check).
func disconnect(t *testing.T, a, b *testNode) {
	t.Helper()
	waitFor(t, "disconnected", func() bool {
		_ = a.h.Network().ClosePeer(b.h.ID())
		_ = b.h.Network().ClosePeer(a.h.ID())
		return !connected(a, b) && !connected(b, a)
	})
}

// TestGaterLoopback: real hosts. A stranger cannot connect while no invite is open; an
// invite lets a joiner in; a failed attempt is disconnected; a paired peer reconnects.
func TestGaterLoopback(t *testing.T) {
	alice := startNode(t, "alice", nil, "")
	bob := startNode(t, "bob", nil, "")
	mallory := startNode(t, "mallory", nil, "")
	aliceInfo := peer.AddrInfo{ID: alice.h.ID(), Addrs: alice.h.Addrs()}

	refused := func(who *testNode) {
		t.Helper()
		_ = who.h.Connect(ctxT(t), aliceInfo)
		if s, err := who.h.NewStream(ctxT(t), alice.h.ID(), protoID(ProtoPair)); err == nil {
			s.Reset()
			t.Fatalf("%s opened a stream to alice", who.name)
		}
		if alice.h.Network().Connectedness(who.h.ID()) == network.Connected {
			t.Fatalf("alice kept a connection from %s", who.name)
		}
	}
	refused(mallory)

	// An open invite: the joiner (a stranger until it pairs) gets in and pairs.
	inv, err := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Join(ctxT(t), inv.Link); err != nil {
		t.Fatalf("join with an open invite: %v", err)
	}
	recv(t, alice.Paired(), "alice Paired")
	send(t, bob, alice, "dm", "hi")

	// The DM invite is used up: strangers are refused again.
	refused(mallory)

	// While an invite is open a stranger may connect, but one whose attempt fails (here a
	// revoked invite) is disconnected after reading the answer.
	revoked, _ := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	_ = alice.RevokeInvite(revoked.ID)
	open, _ := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if _, err := mallory.Join(ctxT(t), revoked.Link); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked invite: %v", err)
	}
	waitFor(t, "alice drops mallory", func() bool { return !connected(alice, mallory) })
	// Rate limited (5 attempts a minute) before its request is read: the answer still
	// arrives before the disconnect.
	for i := 0; ; i++ {
		_, err := mallory.Join(ctxT(t), revoked.Link)
		if err != nil && strings.Contains(err.Error(), "too many attempts") {
			break
		}
		if i > 5 || err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("attempt %d: %v", i+2, err)
		}
	}
	waitFor(t, "alice drops mallory", func() bool { return !connected(alice, mallory) })
	_ = alice.RevokeInvite(open.ID)
	refused(mallory)

	// Paired peers reconnect in both directions with no invite open.
	disconnect(t, alice, bob)
	if err := bob.h.Connect(ctxT(t), aliceInfo); err != nil {
		t.Fatalf("bob reconnects to alice: %v", err)
	}
	send(t, bob, alice, "dm", "back")
	disconnect(t, alice, bob)
	send(t, alice, bob, "dm", "and back")

	// Forgotten: bob is a stranger to alice again (his current connection is cut first).
	alice.Forget(bob.PeerID())
	disconnect(t, alice, bob)
	refused(bob)
}

// TestGaterRelay: through a real circuit relay on loopback. An inbound circuit connection
// carries the remote's own peer ID, so a joiner gets in while an invite is open, a paired
// peer reconnects through the relay, and a stranger is refused.
func TestGaterRelay(t *testing.T) {
	rh := relayNode(t)
	relayAddr := rh.Addrs()[0].String() + "/p2p/" + rh.ID().String()
	alice := startNode(t, "alice", nil, "", relayAddr)
	bob := startNode(t, "bob", nil, "", relayAddr)
	mallory := startNode(t, "mallory", nil, "", relayAddr)
	if _, err := client.Reserve(ctxT(t), alice.h, peer.AddrInfo{ID: rh.ID(), Addrs: rh.Addrs()}); err != nil {
		t.Fatalf("reservation: %v", err)
	}
	inv, err := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	link, circ := circuitLink(t, inv, rh)
	viaRelay := peer.AddrInfo{ID: alice.h.ID(), Addrs: []ma.Multiaddr{circ}}

	// A stranger through the relay while an invite is open: let in. Close it again.
	if err := mallory.h.Connect(ctxT(t), viaRelay); err != nil {
		t.Fatalf("stranger through the relay, invite open: %v", err)
	}
	disconnect(t, alice, mallory)

	if _, err := bob.Join(ctxT(t), link); err != nil {
		t.Fatalf("join through the relay: %v", err)
	}
	recv(t, alice.Paired(), "alice Paired")
	if r := alice.Reachable(bob.PeerID()); r != Relayed {
		t.Fatalf("bob joined through the relay, alice sees %s", r)
	}

	// No invite open now: a stranger through the relay is refused; bob gets back in.
	// Judged on alice's side: mallory's Connect and NewStream can both return nil, because
	// mallory finishes its half of the upgrade before alice's gater refuses the connection,
	// and NewStream selects the pairing protocol lazily (mallory's peerstore learned it on
	// the first connection) without waiting for alice. Only an answer proves alice let it in.
	var strangerConns atomic.Int32
	alice.h.Network().Notify(&network.NotifyBundle{ConnectedF: func(_ network.Network, c network.Conn) {
		if c.RemotePeer() == mallory.h.ID() {
			strangerConns.Add(1)
		}
	}})
	if alice.admit(mallory.h.ID(), circ, alice.gateView()) {
		t.Fatal("the gater admits a stranger through the relay with no invite open")
	}
	_ = mallory.h.Connect(ctxT(t), viaRelay)
	sctx := network.WithAllowLimitedConn(ctxT(t), "test")
	if s, err := mallory.h.NewStream(sctx, alice.h.ID(), protoID(ProtoPair)); err == nil {
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		var resp pairResponse
		werr := writeMsg(s, pairRequest{V: 1, Name: "mallory"})
		rerr := readMsg(s, maxPairMsg, &resp)
		s.Reset()
		if werr == nil && rerr == nil {
			t.Fatalf("a stranger got an answer through the relay: %+v", resp)
		}
	}
	if n := strangerConns.Load(); n != 0 {
		t.Fatalf("alice accepted %d relayed connection(s) from a stranger", n)
	}
	if connected(alice, mallory) {
		t.Fatal("alice kept a relayed connection from a stranger")
	}

	// A known peer gets in through the relay with no invite open: the inbound circuit
	// connection carries its own peer ID. (dave, trusted as a group co-member, knows alice
	// only through the relay and alice has no address for dave, so no redial races this.)
	dave := startNode(t, "dave", nil, "", relayAddr)
	if err := alice.Trust(dave.PeerID(), "dave", nil); err != nil {
		t.Fatal(err)
	}
	if err := dave.h.Connect(ctxT(t), viaRelay); err != nil {
		t.Fatalf("trusted dave through the relay: %v", err)
	}
	if r := alice.Reachable(dave.PeerID()); r != Relayed {
		t.Fatalf("dave through the relay: alice sees %s", r)
	}
	if err := dave.Trust(alice.PeerID(), "alice", nil); err != nil {
		t.Fatal(err)
	}
	send(t, dave, alice, "grp", "relayed")
}

// TestGaterSweepsIdleStranger: a stranger let in through the relay while an invite is open,
// that never opens a pair stream, loses its connection once no invite admits strangers:
// when the invite is used by someone else, and when it is revoked (#90).
func TestGaterSweepsIdleStranger(t *testing.T) {
	rh := relayNode(t)
	relayAddr := rh.Addrs()[0].String() + "/p2p/" + rh.ID().String()
	alice := startNode(t, "alice", nil, "", relayAddr)
	bob := startNode(t, "bob", nil, "", relayAddr)
	mallory := startNode(t, "mallory", nil, "", relayAddr)
	if _, err := client.Reserve(ctxT(t), alice.h, peer.AddrInfo{ID: rh.ID(), Addrs: rh.Addrs()}); err != nil {
		t.Fatalf("reservation: %v", err)
	}
	// Both sides count: until mallory sees alice's close (it travels through the relay),
	// its next Connect reuses the dying circuit instead of dialling a new one.
	strangerIn := func(inv Invite) string {
		t.Helper()
		link, circ := circuitLink(t, inv, rh)
		if err := mallory.h.Connect(ctxT(t), peer.AddrInfo{ID: alice.h.ID(), Addrs: []ma.Multiaddr{circ}}); err != nil {
			t.Fatalf("stranger through the relay, invite open: %v", err)
		}
		waitFor(t, "alice holds mallory's connection", func() bool { return connected(alice, mallory) })
		return link
	}

	// Used by bob: mallory, idle, is cut; bob stays.
	inv, err := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	link := strangerIn(inv)
	if _, err := bob.Join(ctxT(t), link); err != nil {
		t.Fatalf("join through the relay: %v", err)
	}
	recv(t, alice.Paired(), "alice Paired")
	waitFor(t, "alice drops the idle stranger after the invite is used", func() bool { return !connected(alice, mallory) && !connected(mallory, alice) })
	if !connected(alice, bob) {
		t.Fatal("the sweep cut the peer that paired")
	}

	// Revoked: mallory, idle, is cut.
	inv2, err := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	strangerIn(inv2)
	if err := alice.RevokeInvite(inv2.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alice drops the idle stranger after a revoke", func() bool { return !connected(alice, mallory) && !connected(mallory, alice) })
}

// fakeMDNS records starts and closes instead of announcing on the LAN.
type fakeMDNS struct {
	mu             sync.Mutex
	starts, closes int
}

func (f *fakeMDNS) Start() error { f.mu.Lock(); f.starts++; f.mu.Unlock(); return nil }
func (f *fakeMDNS) Close() error { f.mu.Lock(); f.closes++; f.mu.Unlock(); return nil }
func (f *fakeMDNS) running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts > f.closes
}

func (f *fakeMDNS) started() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func startMDNSNode(t *testing.T, name string, md bool, key crypto.PrivKey, stateFile string) (*testNode, *fakeMDNS) {
	t.Helper()
	if key == nil {
		key, _ = testKey(t)
	}
	if stateFile == "" {
		stateFile = filepath.Join(t.TempDir(), "p2p-state.json")
	}
	f := &fakeMDNS{}
	h, err := New(Options{
		Key: key, StateFile: stateFile, Name: name, Version: "test", MDNS: md,
		ListenAddrs:   []string{"/ip4/127.0.0.1/udp/0/quic-v1", "/ip4/127.0.0.1/tcp/0"},
		AllowLoopback: true, RetryInterval: 400 * time.Millisecond,
		newMDNS: func(host.Host, string, mdns.Notifee) mdns.Service { return f },
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLoopbackMultiaddrs(t, h.h.Network().ListenAddresses())
	n := &testNode{Host: h, key: key, state: stateFile, name: name}
	t.Cleanup(func() { n.Close() })
	return n, f
}

// TestMDNSLifecycle: mDNS runs only while there is a paired or trusted peer.
func TestMDNSLifecycle(t *testing.T) {
	alice, amd := startMDNSNode(t, "alice", true, nil, "")
	bobKey, _ := testKey(t)
	bobState := filepath.Join(t.TempDir(), "bob.json")
	bob, bmd := startMDNSNode(t, "bob", true, bobKey, bobState)
	if amd.running() || bmd.running() {
		t.Fatal("mDNS runs with no paired peer")
	}
	inv, _ := alice.CreateInvite(InviteOptions{Kind: InviteDM})
	if _, err := bob.Join(ctxT(t), inv.Link); err != nil {
		t.Fatal(err)
	}
	recv(t, alice.Paired(), "alice Paired")
	waitFor(t, "alice records bob", func() bool { return len(alice.Peers()) == 1 })
	if !amd.running() || !bmd.running() {
		t.Fatalf("mDNS after the first pairing: inviter %v, joiner %v", amd.running(), bmd.running())
	}
	// A trusted co-member counts too; mDNS stops only with the last peer.
	_, carol := testKey(t)
	if err := alice.Trust(carol.String(), "carol", nil); err != nil {
		t.Fatal(err)
	}
	alice.Forget(bob.PeerID())
	if !amd.running() {
		t.Fatal("mDNS stopped while a trusted peer is left")
	}
	alice.Forget(carol.String())
	if amd.running() || amd.started() != 1 {
		t.Fatalf("mDNS after the last peer is forgotten: running %v, starts %d", amd.running(), amd.started())
	}
	if err := alice.Trust(carol.String(), "carol", nil); err != nil {
		t.Fatal(err)
	}
	if !amd.running() || amd.started() != 2 {
		t.Fatal("mDNS did not start again for a new peer")
	}

	// A restart with paired peers on file starts it at once; Close stops it.
	bob.Close()
	if bmd.running() {
		t.Fatal("mDNS still running after Close")
	}
	_, bmd2 := startMDNSNode(t, "bob", true, bobKey, bobState)
	if !bmd2.running() {
		t.Fatal("mDNS not started for the paired peers on file")
	}

	// p2p.mdns false: never.
	off, omd := startMDNSNode(t, "off", false, nil, "")
	if err := off.Trust(carol.String(), "carol", nil); err != nil {
		t.Fatal(err)
	}
	if omd.started() != 0 {
		t.Fatal("mDNS started although it is off")
	}
}

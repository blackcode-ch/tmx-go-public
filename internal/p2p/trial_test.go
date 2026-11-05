package p2p

import (
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func TestDefaultListenersNotWildcard(t *testing.T) {
	n := &Host{}
	for _, a := range n.listenAddrs(1234) {
		if strings.Contains(a, "0.0.0.0") || strings.Contains(a, "/::/") {
			t.Errorf("wildcard listener: %s", a)
		}
	}
}

func TestGaterNoDHTIPAdmissionWhenDHTOff(t *testing.T) {
	n := bareHost()
	remote := ma.StringCast("/ip4/8.8.8.8/tcp/4001")
	v := fakeNet{cs: []gateConn{{peer: newPeerID(t), remote: remote, outbound: true}}}
	if n.admit(newPeerID(t), remote, v) {
		t.Fatal("private host admitted an unknown peer by IP")
	}
}

// No state file, credentials or public sockets: inspect the running local protocol set.
func TestNoBootstrapRelayHolePunchWithoutOptIn(t *testing.T) {
	key, _ := testKey(t)
	n, err := New(Options{Key: key, AllowLoopback: true,
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:   []string{"invalid-unused-bootstrap"}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if n.kad != nil || len(n.h.Network().Conns()) != 0 {
		t.Fatal("private host started discovery or dialled a peer")
	}
	for _, p := range n.h.Mux().Protocols() {
		if strings.Contains(string(p), "circuit") || strings.Contains(string(p), "dcutr") {
			t.Errorf("unexpected relay/hole-punch protocol %s", p)
		}
	}
	before := nowMs()
	inv, err := n.CreateInvite(InviteOptions{Kind: InviteGroup, GroupID: "test-group"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.MaxUses != 1 || inv.ExpiresAt < before || inv.ExpiresAt > nowMs()+int64(24*time.Hour/time.Millisecond) {
		t.Fatalf("unbounded group invite: %+v", inv)
	}
}
func TestInviteAdmissionWindowBounded(t *testing.T) {
	now := nowMs()
	in := &inviteRec{CreatedAt: now - int64(3*time.Minute/time.Millisecond), Invite: Invite{ExpiresAt: now + int64(time.Hour/time.Millisecond), MaxUses: 1}}
	s := &state{Invites: []*inviteRec{in}}
	if s.inviteOpen(now) {
		t.Error("invite admits strangers after two minutes")
	}
	in.CreatedAt = now
	if !s.inviteOpen(now) {
		t.Error("fresh invite refused")
	}
	for _, created := range []int64{0, now + 1, now - (2 * time.Minute).Milliseconds()} {
		in.CreatedAt = created
		if s.inviteOpen(now) {
			t.Errorf("invalid or elapsed admission window: created=%d", created)
		}
	}
}

func TestPendingPairingAdmitsOnlyProvenPeer(t *testing.T) {
	n := bareHost()
	proven, stranger := newPeerID(t), newPeerID(t)
	n.pairing = map[peer.ID]int{proven: 1}
	if !n.admit(proven, nil, nil) || n.admit(stranger, nil, nil) {
		t.Fatal("pending pairing admission is not scoped to the proof-checked peer")
	}
}

package p2p

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
)

// TestLiveRelayJoin pairs over the public internet through a circuit relay: a fresh node on
// the public DHT joins the invite in TMXG_P2P_LIVE_LINK (made by a running daemon, e.g.
// `tmx p2p invite`) after removing every non-relay address from it, then sends an
// envelope. Skipped unless the variable is set; needs internet access.
func TestLiveRelayJoin(t *testing.T) {
	link := os.Getenv("TMXG_P2P_LIVE_LINK")
	if link == "" {
		t.Skip("set TMXG_P2P_LIVE_LINK to an invite from a running daemon")
	}
	p, err := parseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	var relayed []ma.Multiaddr
	for _, a := range p.Addrs {
		if isRelayAddr(a) {
			relayed = append(relayed, a)
		}
	}
	if len(relayed) == 0 {
		t.Fatalf("the invite has no relay address (is the inviter behind NAT with a reservation yet?): %v", p.Addrs)
	}
	p.Addrs = relayed
	link, err = p.encode()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("relay-only link (%d chars), addresses %v", len(link), relayed)

	key, _ := testKey(t)
	n, err := New(Options{Key: key, StateFile: filepath.Join(t.TempDir(), "s.json"), Name: "live-test", Version: "test",
		ListenAddrs: []string{"/ip4/127.0.0.1/udp/0/quic-v1", "/ip4/127.0.0.1/tcp/0"}, DHT: true})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	assertLoopbackMultiaddrs(t, n.h.Network().ListenAddresses())
	// Keep the inviter's other addresses out of reach: only the relay path from the link.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	got, err := n.Join(ctx, link)
	if err != nil {
		t.Fatalf("join over the relay: %v", err)
	}
	t.Logf("paired with %s in %s over %s; safety code %s", got.Name, time.Since(start).Round(time.Millisecond), n.Reachable(got.PeerID), got.SafetyCode)
	for _, c := range n.h.Network().ConnsToPeer(p.PeerID) {
		t.Logf("connection: %s limited=%v", c.RemoteMultiaddr(), c.Stat().Limited)
	}
	if err := n.Send(ctx, got.PeerID, Envelope{ID: "live-" + time.Now().Format("150405"), Type: "ping", Body: []byte(`{"text":"over the relay"}`)}); err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("envelope delivered; reachability now %s", n.Reachable(got.PeerID))
	// DCUtR may upgrade the relayed connection to a direct one (hole punching); give it a moment.
	time.Sleep(10 * time.Second)
	t.Logf("after 10 s: %s", n.Reachable(got.PeerID))
	for _, c := range n.h.Network().ConnsToPeer(p.PeerID) {
		t.Logf("connection: %s limited=%v", c.RemoteMultiaddr(), c.Stat().Limited)
	}
}

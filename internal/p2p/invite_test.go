package p2p

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

func testKey(t *testing.T) (crypto.PrivKey, peer.ID) {
	t.Helper()
	k, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := peer.IDFromPrivateKey(k)
	return k, id
}

// assertLoopbackAddr fails the test if addr is not a loopback IP. Binding 0.0.0.0/:: in a
// test triggers the Windows Firewall prompt; every test socket must stay on 127.0.0.1/::1.
// Shared by every loopback guard in this package (listeners, libp2p hosts, pickPort probes)
// so there is one place that defines what "loopback" means for tests.
func assertLoopbackAddr(t *testing.T, addr net.Addr) {
	t.Helper()
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatalf("test socket address %v: %v", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("test socket bound to non-loopback address %v (must be 127.0.0.1/::1)", addr)
	}
}

// assertLoopbackListener fails the test if l is not bound to a loopback address.
func assertLoopbackListener(t *testing.T, l net.Listener) {
	t.Helper()
	assertLoopbackAddr(t, l.Addr())
}

// assertLoopbackMultiaddrs fails the test if any of addrs is a real socket not bound to
// loopback (e.g. a libp2p host's h.Network().ListenAddresses() or h.Addrs()). Relay circuit
// addresses (/p2p-circuit) are skipped: they have no IP and bind no socket of their own,
// relayed traffic rides the host's own outbound connection to the relay.
func assertLoopbackMultiaddrs(t *testing.T, addrs []ma.Multiaddr) {
	t.Helper()
	for _, a := range addrs {
		if isRelayAddr(a) {
			continue
		}
		if !manet.IsIPLoopback(a) {
			t.Fatalf("host listener bound to non-loopback multiaddr %s (must be /ip4/127.0.0.1 or /ip6/::1)", a)
		}
	}
}

func mustAddrs(t *testing.T, ss ...string) []ma.Multiaddr {
	var out []ma.Multiaddr
	for _, s := range ss {
		a, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestInviteRoundTrip(t *testing.T) {
	_, id := testKey(t)
	_, relay := testKey(t)
	secret := bytes.Repeat([]byte{7}, secretLen)
	exp := time.Now().Add(24 * time.Hour).Truncate(time.Second).UnixMilli()
	addrs := mustAddrs(t,
		"/ip4/203.0.113.7/udp/41234/quic-v1",
		"/ip4/203.0.113.7/tcp/41234",
		"/ip4/198.51.100.2/udp/4001/quic-v1/p2p/"+relay.String()+"/p2p-circuit",
		"/ip4/192.168.1.20/udp/41234/quic-v1",
	)
	for _, p := range []payload{
		{Kind: InviteDM, PeerID: id, Addrs: addrs, Secret: secret, ExpiresAt: exp, Name: "Steve"},
		{Kind: InviteGroup, PeerID: id, Addrs: addrs[:1], Secret: secret, ExpiresAt: exp, Name: "Zoë",
			GroupID: "g7k2m9q4xa", GroupName: "Design crew"},
		{Kind: InviteDM, PeerID: id, Secret: secret, ExpiresAt: exp}, // no addresses: DHT lookup
	} {
		link, err := p.encode()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(link, LinkPrefix) {
			t.Fatalf("link %q", link)
		}
		got, err := parseLink("  Join me: " + link + "\n")
		if err != nil {
			t.Fatalf("parse %q: %v", link, err)
		}
		if got.Kind != p.Kind || got.PeerID != p.PeerID || !bytes.Equal(got.Secret, p.Secret) || got.ExpiresAt != p.ExpiresAt ||
			got.Name != p.Name || got.GroupID != p.GroupID || got.GroupName != p.GroupName || len(got.Addrs) != len(p.Addrs) {
			t.Fatalf("round trip:\n got %+v\nwant %+v", got, p)
		}
		for i := range p.Addrs {
			if !got.Addrs[i].Equal(p.Addrs[i]) {
				t.Fatalf("addr %d: %s != %s", i, got.Addrs[i], p.Addrs[i])
			}
		}
	}
}

// TestInviteLinkLength keeps links small enough for an easily scanned QR code.
func TestInviteLinkLength(t *testing.T) {
	_, id := testKey(t)
	_, relay := testKey(t)
	p := payload{Kind: InviteDM, PeerID: id, Secret: make([]byte, secretLen), ExpiresAt: time.Now().UnixMilli(), Name: "Steve Example",
		Addrs: mustAddrs(t,
			"/ip4/203.0.113.7/udp/41234/quic-v1",
			"/ip4/198.51.100.2/udp/4001/quic-v1/p2p/"+relay.String()+"/p2p-circuit",
			"/ip4/192.168.1.20/udp/41234/quic-v1",
			"/ip4/192.168.1.20/tcp/41234",
		)}
	link, err := p.encode()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DM link with 4 addresses (one relayed): %d chars", len(link))
	if len(link) > 300 {
		t.Fatalf("link is %d chars; keep it under 300 for the QR", len(link))
	}
	p.Addrs = append(p.Addrs, mustAddrs(t, "/ip6/2001:db8::1/udp/41234/quic-v1", "/ip6/2001:db8::1/tcp/41234", "/ip4/10.0.0.2/tcp/1")...)
	link, _ = p.encode()
	got, err := parseLink(link)
	if err != nil || len(got.Addrs) != maxInviteAddrs {
		t.Fatalf("addresses are capped at %d: got %d (%v)", maxInviteAddrs, len(got.Addrs), err)
	}
}

func TestInviteNameTruncation(t *testing.T) {
	_, id := testKey(t)
	long := strings.Repeat("ä", 50) // 100 bytes
	link, err := payload{Kind: InviteDM, PeerID: id, Secret: make([]byte, secretLen), Name: long}.encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Name) > maxNameBytes || !strings.HasPrefix(long, got.Name) {
		t.Fatalf("name %q (%d bytes)", got.Name, len(got.Name))
	}
}

func TestParseLinkRejects(t *testing.T) {
	_, id := testKey(t)
	good, _ := payload{Kind: InviteDM, PeerID: id, Secret: make([]byte, secretLen), Name: "x"}.encode()
	raw := strings.TrimPrefix(good, LinkPrefix)
	for _, s := range []string{
		"", "hello", "https://example.com/join/abc", LinkPrefix, LinkPrefix + "!!!!",
		LinkPrefix + raw[:len(raw)/2], // truncated
		LinkPrefix + "AgA",            // version 2
		LinkPrefix + strings.Repeat("A", 3000),
	} {
		if _, err := parseLink(s); err == nil {
			t.Errorf("parseLink(%q) accepted", s)
		}
	}
	if _, err := parseLink(LinkPrefix + "AgA"); err == nil || !strings.Contains(err.Error(), "newer tmx") {
		t.Errorf("a newer format should say so: %v", err)
	}
}

func TestPairProofBinding(t *testing.T) {
	_, a := testKey(t)
	_, b := testKey(t)
	_, c := testKey(t)
	s := bytes.Repeat([]byte{1}, secretLen)
	p := pairProof(s, a, b)
	if bytes.Equal(p, pairProof(s, a, c)) || bytes.Equal(p, pairProof(s, c, b)) || bytes.Equal(p, pairProof(bytes.Repeat([]byte{2}, secretLen), a, b)) {
		t.Fatal("the proof must depend on the secret and both peer IDs")
	}
}

func TestSafetyCode(t *testing.T) {
	_, a := testKey(t)
	_, b := testKey(t)
	x, y := SafetyCode(a.String(), b.String()), SafetyCode(b.String(), a.String())
	if x != y {
		t.Fatalf("order-dependent: %s vs %s", x, y)
	}
	if len(x) != 7 || x[3] != ' ' {
		t.Fatalf("format %q", x)
	}
	_, c := testKey(t)
	if SafetyCode(a.String(), c.String()) == x && SafetyCode(b.String(), c.String()) == x {
		t.Fatal("codes should differ between pairs")
	}
}

func TestEnvelopeSignature(t *testing.T) {
	k, id := testKey(t)
	e, err := SignEnvelope(k, Envelope{ID: "m1", Type: "msg", ChatID: "c1", Body: []byte(`{"text":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if e.From != id.String() || e.At == 0 || len(e.Sig) == 0 {
		t.Fatalf("not filled: %+v", e)
	}
	if err := VerifyEnvelope(e); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Envelope){
		"body":    func(e *Envelope) { e.Body = []byte(`{"text":"hI"}`) },
		"chat":    func(e *Envelope) { e.ChatID = "c2" },
		"type":    func(e *Envelope) { e.Type = "group" },
		"at":      func(e *Envelope) { e.At++ },
		"id":      func(e *Envelope) { e.ID = "m2" },
		"shift":   func(e *Envelope) { e.ID, e.Type = "m1m", "sg" }, // field boundaries are length-prefixed
		"sig":     func(e *Envelope) { e.Sig[0] ^= 1 },
		"from":    func(e *Envelope) { _, other := testKey(t); e.From = other.String() },
		"garbage": func(e *Envelope) { e.From = "nope" },
	} {
		c := e
		c.Sig = append([]byte(nil), e.Sig...)
		mut(&c)
		if VerifyEnvelope(c) == nil {
			t.Errorf("tampered %s still verifies", name)
		}
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "p2p-key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Equals(k2) {
		t.Fatal("the key must be stable across loads")
	}
	if fi, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("key file mode: %v %v", fi, err)
	}
}

func TestRateLimiter(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 3; i++ {
		if !r.allow("k", 3, time.Minute) {
			t.Fatalf("hit %d refused", i)
		}
	}
	if r.allow("k", 3, time.Minute) {
		t.Fatal("4th hit allowed")
	}
	if !r.allow("other", 3, time.Minute) {
		t.Fatal("keys are independent")
	}
	if !r.allow("short", 1, time.Millisecond) {
		t.Fatal()
	}
	time.Sleep(3 * time.Millisecond)
	if !r.allow("short", 1, time.Millisecond) {
		t.Fatal("the window should have passed")
	}
}

func TestInviteEnded(t *testing.T) {
	now := time.Now().UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	s := &state{Invites: []*inviteRec{
		{Invite: Invite{ID: "open", ExpiresAt: now + day}},
		{Invite: Invite{ID: "expired-long-ago", ExpiresAt: now - 2*day}},
		{Invite: Invite{ID: "expired-recently", ExpiresAt: now - day/2}},
		{Invite: Invite{ID: "revoked-long-ago", ExpiresAt: now + day, Revoked: true}, EndedAt: now - 2*day},
		{Invite: Invite{ID: "used", ExpiresAt: now + day, MaxUses: 1, Uses: 1}, EndedAt: now - 10},
	}}
	if !s.Invites[0].open(now) || s.Invites[4].open(now) {
		t.Fatal("open/used")
	}
	s.pruneInvites(now)
	var ids []string
	for _, in := range s.Invites {
		ids = append(ids, in.ID)
	}
	if strings.Join(ids, ",") != "open,expired-recently,used" {
		t.Fatalf("kept %v", ids)
	}
}

func TestPickPort(t *testing.T) {
	pickPortProbeCheck = func(a net.Addr) { assertLoopbackAddr(t, a) }
	t.Cleanup(func() { pickPortProbeCheck = nil })

	p := pickPortOn("127.0.0.1", 0)
	if p < 20000 || p >= 60000 {
		t.Fatalf("random port %d", p)
	}
	if got := pickPortOn("127.0.0.1", p); got != p {
		t.Fatalf("a free remembered port is kept: %d != %d", got, p)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	assertLoopbackListener(t, l)
	if got := pickPortOn("127.0.0.1", p); got == p {
		t.Fatal("a busy remembered port must be replaced")
	}
}

func TestDialable(t *testing.T) {
	_, relay := testKey(t)
	for s, want := range map[string]bool{
		"/ip4/1.2.3.4/udp/4001/quic-v1":                                     true,
		"/ip4/1.2.3.4/tcp/4001":                                             true,
		"/ip4/1.2.3.4/tcp/4001/p2p/" + relay.String() + "/p2p-circuit":      true,
		"/ip4/1.2.3.4/udp/4001/quic/p2p/" + relay.String() + "/p2p-circuit": false,
		"/ip4/1.2.3.4/tcp/4001/ws":                                          false,
		"/ip4/1.2.3.4/udp/4001/quic-v1/webtransport":                        false,
	} {
		if got := dialable(mustAddrs(t, s)[0]); got != want {
			t.Errorf("dialable(%s) = %v", s, got)
		}
	}
}

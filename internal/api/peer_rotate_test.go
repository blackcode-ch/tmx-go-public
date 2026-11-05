package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/peers"
)

// fakeVoyager answers /api/peers/pair-token the way a real peer would: stores the posted
// token, answers its own fingerprint of exactly what it stored.
type fakeVoyager struct {
	pairTok string
}

func (p *fakeVoyager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/api/peers/pair-token" {
		w.WriteHeader(404)
		return
	}
	var b struct{ Token string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	p.pairTok = b.Token
	_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": peers.Fingerprint(b.Token)})
}

// rotateSetup is "hostb" paired with a real "hostc" peer (sorts after hostb, so hostb
// may rotate toward it - DESIGN.md's rule).
func rotateSetup(t *testing.T) (*apitest.Daemon, *fakeVoyager, string) {
	t.Helper()
	d := apitest.New(t)
	voy := &fakeVoyager{}
	srv := httptest.NewTLSServer(voy)
	t.Cleanup(srv.Close)
	paths := config.Paths{Home: d.Home}
	cfg := config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hostc", URL: srv.URL}}}
	pc := peers.New(cfg, paths, nil)
	pc.SetTransport(srv.Client().Transport)
	pair := peers.NewPairToken()
	if _, err := pc.Tokens().Store("hostc", pair, "test"); err != nil {
		t.Fatal(err)
	}
	voy.pairTok = pair
	url := d.WithPeers(t, pc)
	return d, voy, url
}

// The owner can rotate a pair token toward a peer sorting after this machine; the new
// fingerprint matches what the peer actually stored.
func TestPeerRotateOwnerOnlyAndPINGated(t *testing.T) {
	d, voy, url := rotateSetup(t)
	owner := tc.New(url, d.OwnerToken)
	before := voy.pairTok

	var got map[string]string
	if s, _ := call(t, owner, "POST", "/api/peers/hostc/rotate", nil, &got); s != 0 {
		t.Fatalf("rotate: %d", s)
	}
	if got["fingerprint"] != peers.Fingerprint(voy.pairTok) {
		t.Fatalf("returned fingerprint does not match what hostc stored: %v (hostc has %s)", got, peers.Fingerprint(voy.pairTok))
	}
	if voy.pairTok == before {
		t.Fatal("hostc's token was not actually rotated")
	}

	// Owner-only: an agent is refused.
	agent := tc.New(url, d.AgentToken(t, "head"))
	if s, _ := call(t, agent, "POST", "/api/peers/hostc/rotate", nil, nil); s != http.StatusForbidden {
		t.Fatalf("agent rotate: %d, want 403", s)
	}

	// With the gate on, rotating asks for a fresh proof, same as any other config.json change.
	gateOn(t, owner)
	s, conf := call(t, owner, "POST", "/api/peers/hostc/rotate", nil, nil)
	if s != http.StatusPreconditionRequired || conf.Reason == "" {
		t.Fatalf("without proof: %d %q", s, conf.Reason)
	}
	stillBefore := voy.pairTok
	proof := confirmed(t, owner, conf)
	if s, _ := call(t, proof, "POST", "/api/peers/hostc/rotate", nil, nil); s != 0 {
		t.Fatalf("with proof: %d", s)
	}
	if voy.pairTok == stillBefore {
		t.Fatal("confirmed rotation did not change the peer's token")
	}
}

// Rotating toward a peer that sorts BEFORE this machine (it generates OUR token, not the other
// way round) is refused without any network call.
func TestPeerRotateRefusesAPeerThatSortsFirst(t *testing.T) {
	d := apitest.New(t)
	paths := config.Paths{Home: d.Home}
	cfg := config.Config{Profile: config.ProfileProduction, Machine: "hostb", Peers: []config.Peer{{Name: "hosta", URL: "https://hosta.example.ts.net"}}}
	pc := peers.New(cfg, paths, nil)
	pair := peers.NewPairToken()
	if _, err := pc.Tokens().Store("hosta", pair, "test"); err != nil {
		t.Fatal(err)
	}
	url := d.WithPeers(t, pc)
	owner := tc.New(url, d.OwnerToken)
	if s, _ := call(t, owner, "POST", "/api/peers/hosta/rotate", nil, nil); s != http.StatusBadGateway {
		t.Fatalf("rotate toward hosta: %d, want 502 (refused by RotateWith, not a route error)", s)
	}
}

// Rotating toward an unknown or unconfigured peer is a clean 404, not a panic or a 500.
func TestPeerRotateUnknownPeer(t *testing.T) {
	d, _, url := rotateSetup(t)
	owner := tc.New(url, d.OwnerToken)
	if s, _ := call(t, owner, "POST", "/api/peers/mallory/rotate", nil, nil); s != http.StatusNotFound {
		t.Fatalf("unknown peer: %d, want 404", s)
	}
}

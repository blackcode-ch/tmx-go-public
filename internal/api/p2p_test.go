package api_test

import (
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

func TestP2PRoutesOff(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	var st api.P2PStatus
	if s := owner.do("GET", "/api/p2p", nil, &st); s != 200 || st.Enabled || st.Reachability != p2p.Offline {
		t.Fatalf("off: %d %+v", s, st)
	}
	if s := owner.do("POST", "/api/p2p/net/invites", `{}`, nil); s != 503 {
		t.Fatalf("net route while off: %d", s)
	}
	if s := agent.do("GET", "/api/p2p", nil, nil); s != 403 {
		t.Fatalf("agent: %d", s)
	}
}

func TestP2PRoutesOn(t *testing.T) {
	d := apitest.New(t)
	key, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	node, err := p2p.New(p2p.Options{Key: key, Name: "Owner", ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"}, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { node.Close() })
	p2p.AssertLoopbackAddrs(t, node.Status().Addrs)
	srv := httptest.NewServer(api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, P2P: node}).Handler())
	t.Cleanup(srv.Close)
	owner := client{t: t, base: srv.URL, token: d.OwnerToken}

	var st api.P2PStatus
	if s := owner.do("GET", "/api/p2p", nil, &st); s != 200 || !st.Enabled || st.PeerID != node.PeerID() || st.Name != "Owner" || len(st.Addrs) == 0 {
		t.Fatalf("status: %d %+v", s, st)
	}
	var inv p2p.Invite
	if s := owner.do("POST", "/api/p2p/net/invites", map[string]any{"kind": "group", "group_id": "g1", "group_name": "Crew"}, &inv); s != 201 ||
		!strings.HasPrefix(inv.Link, p2p.LinkPrefix) || inv.Kind != p2p.InviteGroup {
		t.Fatalf("invite: %d %+v", s, inv)
	}
	var pv p2p.InvitePreview
	if s := owner.do("POST", "/api/p2p/net/preview", map[string]string{"link": inv.Link}, &pv); s != 200 || pv.GroupName != "Crew" || pv.Name != "Owner" {
		t.Fatalf("preview: %d %+v", s, pv)
	}
	if s := owner.do("POST", "/api/p2p/net/preview", map[string]string{"link": "tmx://join/zz"}, nil); s != 400 {
		t.Fatalf("bad link: %d", s)
	}
	if s := owner.do("POST", "/api/p2p/net/join", map[string]string{"link": inv.Link}, nil); s != 400 {
		t.Fatalf("joining own invite: %d", s)
	}
	if s := owner.do("DELETE", "/api/p2p/net/invites/"+inv.ID, nil, nil); s != 200 {
		t.Fatalf("revoke: %d", s)
	}
	var list []p2p.Invite
	if s := owner.do("GET", "/api/p2p/net/invites", nil, &list); s != 200 || len(list) != 1 || !list[0].Revoked {
		t.Fatalf("list: %d %+v", s, list)
	}
	if s := owner.do("GET", "/api/p2p/net/log", nil, nil); s != 404 {
		t.Fatalf("log without drain: %d", s)
	}
}

package api_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/gate"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

// Handing a group's admin role over: only the admin may, only to a member, and always with the
// owner's confirmation on this machine, also while the owner gate is off (28 Oct 2026).
func TestChatAdminHandoverRoutes(t *testing.T) {
	net := p2ptest.NewNetwork()
	alexNode, boNode := net.NewNode("alex"), net.NewNode("bo")
	a, b := apitest.NewWithNode(t, alexNode), apitest.NewWithNode(t, boNode)
	alex := client{t: t, base: a.URL, token: a.OwnerToken}
	bo := client{t: t, base: b.URL, token: b.OwnerToken}
	alexTC, boTC := tc.New(a.URL, a.OwnerToken), tc.New(b.URL, b.OwnerToken)

	var g model.Chat
	alex.do("POST", "/api/chats", map[string]string{"name": "Trip"}, &g)
	var inv p2p.Invite
	alex.do("POST", "/api/invites", map[string]string{"kind": "group", "group_id": g.ID}, &inv)
	if s := bo.do("POST", "/api/invites/join", map[string]string{"link": inv.Link}, nil); s != 200 {
		t.Fatalf("join: %d", s)
	}
	waitFor(t, "bo in the group", func() bool {
		var c model.Chat
		bo.do("GET", "/api/chats/"+g.ID, nil, &c)
		return c.Status == model.ChatActive && len(c.Members) == 2
	})
	path := "/api/chats/" + g.ID + "/admin"
	toBo := map[string]any{"peer_id": boNode.ID(), "then_leave": false}

	// Who may: not a member (bo, not the admin), not an agent, only to a member.
	if s := bo.do("POST", path, map[string]any{"peer_id": alexNode.ID()}, nil); s != http.StatusForbidden {
		t.Fatalf("a member hands over: %d", s)
	}
	head := client{t: t, base: a.URL, token: a.AgentToken(t, "head")}
	if s := head.do("POST", path, toBo, nil); s != http.StatusForbidden {
		t.Fatalf("an agent hands over: %d", s)
	}
	if s := alex.do("POST", path, map[string]any{"peer_id": "peer-nobody"}, nil); s != http.StatusNotFound {
		t.Fatalf("to a non-member: %d", s)
	}
	if s := alex.do("POST", "/api/chats/g-unknown/admin", toBo, nil); s != http.StatusNotFound {
		t.Fatalf("unknown chat: %d", s)
	}
	if len(a.GateAuth.Prompts()) != 0 {
		t.Fatalf("refused requests prompted: %q", a.GateAuth.Prompts())
	}

	// The gate is off, and the handover still asks.
	var st gate.Status
	if call(t, alexTC, "GET", "/api/gate", nil, &st); st.Enabled {
		t.Fatal("gate on")
	}
	s, conf := call(t, alexTC, "POST", path, toBo, nil)
	if s != http.StatusPreconditionRequired || conf.Reason != "make bo the admin of Trip" {
		t.Fatalf("handover without proof: %d %+v", s, conf)
	}
	var done gate.Confirmation
	if err := alexTC.Get(context.Background(), "/api/gate/confirmations/"+conf.ID+"?wait=3000", &done); err != nil || done.State != gate.StateConfirmed {
		t.Fatalf("confirmation: %+v %v", done, err)
	}
	// A different body needs its own confirmation (a different gate.Action.Key), which this
	// call leaves pending with an ID the test never otherwise learns. waitIdle's probe cannot
	// cancel it directly (gate.Require only hands back a *NeedConfirmation for a MATCHING key;
	// for any other pending one it just returns ErrBusy, so waitIdle can only poll and wait for
	// it to resolve by itself - a real CI flake, 3 Nov, run 41692174665: on a loaded runner the
	// fake auth's confirm goroutine can lose the race against waitIdle's 3s deadline, and the
	// next ask below then hits gate.ErrBusy -> 409 instead of a fresh 428). Cancel it by its own
	// ID instead of waiting it out, so the next ask is deterministic.
	s, wrongBodyConf := call(t, alexTC, "POST", path+"?"+api.ConfirmParam+"="+done.ID, map[string]any{"peer_id": boNode.ID(), "then_leave": true}, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("a proof for another body: %d", s)
	}
	a.Gate.Cancel(wrongBodyConf.ID)
	s, conf = call(t, alexTC, "POST", path, toBo, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("second ask: %d", s)
	}
	alexTC.Get(context.Background(), "/api/gate/confirmations/"+conf.ID+"?wait=3000", &done)
	var out model.Chat
	if s := alex.do("POST", path+"?"+api.ConfirmParam+"="+done.ID, toBo, &out); s != http.StatusAccepted || out.ID != g.ID {
		t.Fatalf("handover with proof: %d %+v", s, out)
	}
	waitFor(t, "bo runs the group", func() bool {
		var c model.Chat
		bo.do("GET", "/api/chats/"+g.ID, nil, &c)
		return c.Admin
	})
	var ac model.Chat
	if alex.do("GET", "/api/chats/"+g.ID, nil, &ac); ac.Admin || ac.AdminOffer != nil {
		t.Fatalf("alex after the handover: %+v", ac)
	}
	if s := alex.do("POST", path, map[string]any{"peer_id": boNode.ID()}, nil); s != http.StatusForbidden {
		t.Fatalf("alex hands over again: %d", s)
	}

	// Bo offers it back while alex is offline: it waits (admin_offer), a second offer is a
	// conflict (no prompt), and DELETE withdraws it.
	alexNode.SetOnline(false)
	back := map[string]any{"peer_id": alexNode.ID()}
	s, conf = call(t, boTC, "POST", path, back, nil)
	if s != http.StatusPreconditionRequired {
		t.Fatalf("bo's offer: %d", s)
	}
	boTC.Get(context.Background(), "/api/gate/confirmations/"+conf.ID+"?wait=3000", &done)
	if s := bo.do("POST", path+"?"+api.ConfirmParam+"="+done.ID, back, &out); s != http.StatusAccepted || out.AdminOffer == nil ||
		out.AdminOffer.PeerID != alexNode.ID() || out.AdminOffer.Sent {
		t.Fatalf("offer to offline alex: %d %+v", s, out.AdminOffer)
	}
	prompts := len(b.GateAuth.Prompts())
	if s := bo.do("POST", path, back, nil); s != http.StatusConflict {
		t.Fatalf("second offer: %d", s)
	}
	if len(b.GateAuth.Prompts()) != prompts {
		t.Fatal("a conflicting offer prompted")
	}
	var cancelled model.Chat
	if s := bo.do("DELETE", path, nil, &cancelled); s != http.StatusOK || cancelled.AdminOffer != nil || !cancelled.Admin {
		t.Fatalf("cancel: %d %+v", s, cancelled.AdminOffer)
	}
	if s := bo.do("DELETE", path, nil, nil); s != http.StatusNotFound {
		t.Fatalf("cancel again: %d", s)
	}
}

// Without an owner gate there is no way to ask for the owner's biometrics: the handover is
// refused (501), never let through.
func TestChatAdminNeedsAGate(t *testing.T) {
	net0 := p2ptest.NewNetwork()
	node := net0.NewNode("alex")
	d := apitest.NewWithNode(t, node)
	srv := httptest.NewUnstartedServer(nil)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	srv.Config.Handler = api.New(api.Options{Fleet: d.Fleet, OwnerToken: d.OwnerToken, Port: port, Chats: d.Chats, P2P: node}).Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	alex := client{t: t, base: srv.URL, token: d.OwnerToken}
	var g model.Chat
	alex.do("POST", "/api/chats", map[string]string{"name": "Solo"}, &g)
	other := net0.NewNode("bo")
	inv, _ := d.Chats.CreateInvite(chat.InviteRequest{Kind: p2p.InviteGroup, GroupID: g.ID})
	if _, err := other.Join(context.Background(), inv.Link); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bo admitted", func() bool {
		var c model.Chat
		alex.do("GET", "/api/chats/"+g.ID, nil, &c)
		return len(c.Members) == 2
	})
	if s := alex.do("POST", "/api/chats/"+g.ID+"/admin", map[string]any{"peer_id": other.ID()}, nil); s != http.StatusNotImplemented {
		t.Fatalf("without a gate: %d", s)
	}
}

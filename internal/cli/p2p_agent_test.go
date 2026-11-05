package cli

import (
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
	"strings"
	"testing"
)

func TestP2PAgentCLI(t *testing.T) {
	net := p2ptest.NewNetwork()
	node := net.NewNode("Alice")
	other := net.NewNode("Bob")
	d := apitest.NewWithNode(t, node)
	t.Setenv(fleet.EnvURL, d.URL)
	t.Setenv(fleet.EnvToken, d.AgentToken(t, "head"))
	out, errs, code := tmx(t, "p2p", "invite-create", "--label", "Bob")
	var pending api.AgentP2PResult
	if code != 0 || json.Unmarshal([]byte(out), &pending) != nil || pending.ApprovalID == "" || len(node.Invites()) != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if _, err := d.Fleet.Decide(fleet.OwnerCaller, pending.ApprovalID, fleet.DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	out, errs, code = tmx(t, "p2p", "invite-create", "--approval", pending.ApprovalID)
	if code != 0 || !strings.Contains(out, "tmx://join/") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	inv, err := other.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	out, errs, code = tmx(t, "p2p", "join", inv.Link)
	if code != 0 || !strings.Contains(out, "Join Bob's tmx; peer "+other.ID()) || !strings.Contains(out, "pending") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	t.Setenv(fleet.EnvToken, "")
	if _, _, code = tmx(t, "p2p", "invite-create", "--label", "Bob"); code == 0 {
		t.Fatal("fell back to owner credentials")
	}
}

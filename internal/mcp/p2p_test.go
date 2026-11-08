package mcp

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
	"strings"
	"testing"
)

func TestP2PToolsApprovalResume(t *testing.T) {
	node := p2ptest.NewNetwork().NewNode("Alice")
	d := apitest.NewWithNode(t, node)
	cs := session(t, d, "head")
	out := mustOK(t, cs, "p2p_invite_create", map[string]any{"label": "Bob"})
	if !strings.Contains(out, "pending") || !strings.Contains(out, "approval_id") || len(node.Invites()) != 0 {
		t.Fatal(out)
	}
	aps, _ := d.Fleet.Approvals(model.ApprovalPending)
	if len(aps) != 1 {
		t.Fatal("expected one approval")
	}
	if _, err := d.Fleet.Decide(fleet.OwnerCaller, aps[0].ID, fleet.DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	out = mustOK(t, cs, "p2p_invite_create", map[string]any{"approval_id": aps[0].ID})
	if !strings.Contains(out, "tmx://join/") || !strings.Contains(out, "expires_at") {
		t.Fatal(out)
	}
	if _, bad := call(t, cs, "p2p_join", map[string]any{"link": "invalid"}); !bad {
		t.Fatal("invalid link accepted")
	}
}

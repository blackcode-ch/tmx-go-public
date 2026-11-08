package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api"
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func approvePairing(t *testing.T, owner *tc.Client, id, decision string) {
	t.Helper()
	body := map[string]string{"decision": decision, "scope": "any"}
	status, conf := call(t, owner, "POST", "/api/approvals/"+id, body, nil)
	if status != http.StatusPreconditionRequired {
		t.Fatalf("approval without fresh owner proof: %d", status)
	}
	if status, _ = call(t, confirmed(t, owner, conf), "POST", "/api/approvals/"+id, body, nil); status != 0 {
		t.Fatalf("approval with proof: %d", status)
	}
}

func TestAgentP2PApprovalAndPairing(t *testing.T) {
	net := p2ptest.NewNetwork()
	alice, bob := net.NewNode("Alice"), net.NewNode("Bob")
	a, b := apitest.NewWithNode(t, alice), apitest.NewWithNode(t, bob)
	head := tc.New(a.URL, a.AgentToken(t, "head"))
	owner := tc.New(a.URL, a.OwnerToken)
	var pending api.AgentP2PResult
	req := api.AgentP2PInviteRequest{Label: "Bob"}
	if code, _ := call(t, head, "POST", "/api/p2p/agent/invite", req, &pending); code != 0 || pending.State != model.ApprovalPending || pending.Invite != nil {
		t.Fatalf("request: %d %+v", code, pending)
	}
	if pending.Preview != "Create an invite for Bob, valid 24 h, 1 use" {
		t.Fatal(pending.Preview)
	}
	if len(alice.Invites()) != 0 {
		t.Fatal("invite created before approval")
	}
	resume := map[string]string{"approval_id": pending.ApprovalID}
	for range 2 {
		call(t, head, "POST", "/api/p2p/agent/invite", resume, nil)
	}
	aps, _ := a.Fleet.Approvals(model.ApprovalPending)
	if len(aps) != 1 {
		t.Fatalf("duplicate cards: %d", len(aps))
	}
	if code, _ := call(t, head, "POST", "/api/approvals/"+pending.ApprovalID, map[string]string{"decision": "allow"}, nil); code != 403 {
		t.Fatal("agent self-approved", code)
	}
	if code, _ := call(t, head, "POST", "/api/p2p/agent/invite", map[string]string{"approval_id": pending.ApprovalID, "label": "Changed"}, nil); code != 400 {
		t.Fatal("changed held payload", code)
	}
	approvePairing(t, owner, pending.ApprovalID, "allow")
	var done api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/invite", resume, &done)
	if done.Invite == nil || done.Invite.MaxUses != 1 || done.Invite.ExpiresAt < time.Now().Add(23*time.Hour).UnixMilli() {
		t.Fatalf("invite: %+v", done)
	}
	call(t, head, "POST", "/api/p2p/agent/invite", resume, nil)
	if len(alice.Invites()) != 1 {
		t.Fatal("resume executed twice")
	}
	// Allow once does not create a standing grant, even for identical arguments.
	var again api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/invite", req, &again)
	if again.State != model.ApprovalPending {
		t.Fatal("allow once became always")
	}
	// Join returns a preview before contacting the inviter or consuming the link.
	bh := tc.New(b.URL, b.AgentToken(t, "head"))
	bo := tc.New(b.URL, b.OwnerToken)
	var join api.AgentP2PResult
	call(t, bh, "POST", "/api/p2p/agent/join", api.AgentP2PJoinRequest{Link: done.Invite.Link}, &join)
	if join.State != model.ApprovalPending || join.Preview != "Join Alice's tmx; peer "+alice.ID() || join.Joined != nil || alice.Invites()[0].Uses != 0 {
		t.Fatalf("join preview: %+v", join)
	}
	card, err := b.Fleet.ApprovalByID(join.ApprovalID)
	if err != nil || card.Description != join.Preview {
		t.Fatalf("phone and agent previews differ: %+v %v", card, err)
	}
	approvePairing(t, bo, join.ApprovalID, "allow")
	var joined api.AgentP2PResult
	call(t, bh, "POST", "/api/p2p/agent/join", map[string]string{"approval_id": join.ApprovalID}, &joined)
	if joined.Joined == nil || joined.Joined.Contact.Name != "Alice" || alice.Invites()[0].Uses != 1 {
		t.Fatalf("join: %+v", joined)
	}
}

func TestAgentP2PDeniedExpiredAndWorker(t *testing.T) {
	net := p2ptest.NewNetwork()
	node := net.NewNode("Alice")
	d := apitest.NewWithNode(t, node)
	head := tc.New(d.URL, d.AgentToken(t, "head"))
	owner := tc.New(d.URL, d.OwnerToken)
	for _, decision := range []string{"deny", "expire"} {
		var out api.AgentP2PResult
		call(t, head, "POST", "/api/p2p/agent/invite", api.AgentP2PInviteRequest{Label: "Team", Kind: "group"}, &out)
		if decision == "deny" {
			call(t, owner, "POST", "/api/approvals/"+out.ApprovalID, map[string]string{"decision": "deny"}, nil)
		} else {
			agent, _ := d.Fleet.Agent("head")
			if _, err := d.Fleet.CancelHold(fleet.AgentCaller(agent.ID), out.ApprovalID); err != nil {
				t.Fatal(err)
			}
		}
		var result api.AgentP2PResult
		call(t, head, "POST", "/api/p2p/agent/invite", map[string]string{"approval_id": out.ApprovalID}, &result)
		if result.Invite != nil || result.State == model.ApprovalAllowed || len(node.Invites()) != 0 {
			t.Fatalf("ran after %s: %+v", decision, result)
		}
	}

	other := net.NewNode("Bob")
	inv, err := other.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"deny", "expire"} {
		var pending api.AgentP2PResult
		call(t, head, "POST", "/api/p2p/agent/join", map[string]string{"link": inv.Link}, &pending)
		if decision == "deny" {
			call(t, owner, "POST", "/api/approvals/"+pending.ApprovalID, map[string]string{"decision": "deny"}, nil)
		} else {
			agent, _ := d.Fleet.Agent("head")
			if _, err := d.Fleet.CancelHold(fleet.AgentCaller(agent.ID), pending.ApprovalID); err != nil {
				t.Fatal(err)
			}
		}
		var result api.AgentP2PResult
		call(t, head, "POST", "/api/p2p/agent/join", map[string]string{"approval_id": pending.ApprovalID}, &result)
		if result.Joined != nil || other.Invites()[0].Uses != 0 {
			t.Fatalf("joined after %s", decision)
		}
	}
	var chats []model.Chat
	call(t, owner, "GET", "/api/chats", nil, &chats)
	if len(chats) != 0 {
		t.Fatal("group created before approval")
	}
	oc := client{t: t, base: d.URL, token: d.OwnerToken}
	var worker model.Agent
	if code := oc.do("POST", "/api/agents", map[string]any{"name": "worker", "project_id": workerProject(t, d)}, &worker); code != 201 {
		t.Fatal("worker launch", code)
	}
	wc := tc.New(d.URL, d.AgentToken(t, "worker"))
	for _, path := range []string{"invite", "join"} {
		if code, _ := call(t, wc, "POST", "/api/p2p/agent/"+path, map[string]string{"label": "Bob", "link": "invalid"}, nil); code != 403 {
			t.Fatal("worker", path, code)
		}
	}
	aps, _ := d.Fleet.Approvals(model.ApprovalPending)
	if len(aps) != 0 {
		t.Fatal("worker created approval")
	}
}

func TestAgentP2PAlwaysAndRevocation(t *testing.T) {
	net := p2ptest.NewNetwork()
	node := net.NewNode("Alice")
	d := apitest.NewWithNode(t, node)
	head := tc.New(d.URL, d.AgentToken(t, "head"))
	owner := tc.New(d.URL, d.OwnerToken)
	var out api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/invite", api.AgentP2PInviteRequest{Label: "First"}, &out)
	approvePairing(t, owner, out.ApprovalID, fleet.DecisionAllowAlways)
	var next api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/invite", api.AgentP2PInviteRequest{Label: "Different group", Kind: "group", MaxUses: 3, TTL: "48h"}, &next)
	if next.State != model.ApprovalAllowed || next.Invite == nil || next.Invite.MaxUses != 3 || next.Invite.GroupName != "Different group" {
		t.Fatalf("always: %+v", next)
	}
	// Group invite really lands in the group through the chat service.
	other := net.NewNode("Bob")
	b := apitest.NewWithNode(t, other)
	joined, err := b.Chats.Join(context.Background(), next.Invite.Link)
	if err != nil || joined.Chat.Kind != model.ChatGroup {
		t.Fatalf("group join: %+v %v", joined, err)
	}
	// Creating invites never grants the join tool.
	inv, err := other.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteDM})
	if err != nil {
		t.Fatal(err)
	}
	var join api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/join", map[string]string{"link": inv.Link}, &join)
	if join.State != model.ApprovalPending {
		t.Fatal("invite grant leaked to join")
	}
	grants := d.Fleet.GuardSettings().AlwaysAllow
	if len(grants) != 1 || !strings.Contains(grants[0].Tool, "p2p_invite_create") {
		t.Fatalf("grants: %+v", grants)
	}
	path := "/api/safety/guard/always/" + grants[0].ID
	code, conf := call(t, owner, "DELETE", path, nil, nil)
	if code != 428 {
		t.Fatal("revoke proof", code)
	}
	if code, _ := call(t, confirmed(t, owner, conf), "DELETE", path, nil, nil); code != 0 {
		t.Fatal("revoke", code)
	}
	var revoked api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/invite", api.AgentP2PInviteRequest{Label: "Again"}, &revoked)
	if revoked.State != model.ApprovalPending || revoked.Invite != nil {
		t.Fatal("revocation did not take effect")
	}
}

func TestAgentP2PLeadIsolationAndConcurrentResume(t *testing.T) {
	node := p2ptest.NewNetwork().NewNode("Alice")
	d := apitest.NewWithNode(t, node)
	project, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "Pairing"})
	if err != nil {
		t.Fatal(err)
	}
	lead := tc.New(d.URL, d.AgentToken(t, project.LeadID))
	head := tc.New(d.URL, d.AgentToken(t, "head"))
	var pending api.AgentP2PResult
	call(t, lead, "POST", "/api/p2p/agent/invite", api.AgentP2PInviteRequest{Label: "Bob"}, &pending)
	if pending.State != model.ApprovalPending {
		t.Fatal("lead cannot request approval")
	}
	resume := map[string]string{"approval_id": pending.ApprovalID}
	if code, _ := call(t, head, "POST", "/api/p2p/agent/invite", resume, nil); code != 404 {
		t.Fatal("another agent used the approval", code)
	}
	if code, _ := call(t, lead, "POST", "/api/p2p/agent/join", resume, nil); code != 404 {
		t.Fatal("another tool used the approval", code)
	}
	approvePairing(t, tc.New(d.URL, d.OwnerToken), pending.ApprovalID, "allow")
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			var out api.AgentP2PResult
			errs <- lead.Post(context.Background(), "/api/p2p/agent/invite", resume, &out)
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if len(node.Invites()) != 1 {
		t.Fatal("concurrent resumes created multiple invitations")
	}
	// Even an allowed, unexecuted request is bound to the original process token.
	var old api.AgentP2PResult
	call(t, lead, "POST", "/api/p2p/agent/invite", api.AgentP2PInviteRequest{Label: "Old process"}, &old)
	approvePairing(t, tc.New(d.URL, d.OwnerToken), old.ApprovalID, "allow")
	if _, err := d.Fleet.StopAgent(fleet.OwnerCaller, project.LeadID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Fleet.StartAgent(fleet.OwnerCaller, project.LeadID); err != nil {
		t.Fatal(err)
	}
	fresh := tc.New(d.URL, d.AgentToken(t, project.LeadID))
	if code, _ := call(t, fresh, "POST", "/api/p2p/agent/invite", map[string]string{"approval_id": old.ApprovalID}, nil); code != 404 {
		t.Fatal("new process redeemed old approval", code)
	}
	if len(node.Invites()) != 1 {
		t.Fatal("old approval executed")
	}
}

func TestAgentP2PJoinAlwaysAndWorkerStillRefused(t *testing.T) {
	net := p2ptest.NewNetwork()
	d := apitest.NewWithNode(t, net.NewNode("Alice"))
	head := tc.New(d.URL, d.AgentToken(t, "head"))
	owner := tc.New(d.URL, d.OwnerToken)
	makeLink := func(name string) string {
		n := net.NewNode(name)
		apitest.NewWithNode(t, n)
		inv, err := n.CreateInvite(p2p.InviteOptions{Kind: p2p.InviteDM})
		if err != nil {
			t.Fatal(err)
		}
		return inv.Link
	}
	var pending api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/join", map[string]string{"link": makeLink("Bob")}, &pending)
	approvePairing(t, owner, pending.ApprovalID, fleet.DecisionAllowAlways)
	var joined api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/join", map[string]string{"link": makeLink("Carol")}, &joined)
	if joined.State != model.ApprovalAllowed || joined.Joined == nil || joined.Joined.Contact.Name != "Carol" {
		t.Fatalf("join grant: %+v", joined)
	}
	oc := client{t: t, base: d.URL, token: d.OwnerToken}
	if code := oc.do("POST", "/api/agents", map[string]any{"name": "worker", "project_id": workerProject(t, d)}, nil); code != 201 {
		t.Fatal(code)
	}
	wc := tc.New(d.URL, d.AgentToken(t, "worker"))
	if code, _ := call(t, wc, "POST", "/api/p2p/agent/join", map[string]string{"link": makeLink("Dave")}, nil); code != 403 {
		t.Fatal("standing grant admitted worker", code)
	}
	grants := d.Fleet.GuardSettings().AlwaysAllow
	path := "/api/safety/guard/always/" + grants[0].ID
	code, conf := call(t, owner, "DELETE", path, nil, nil)
	if code != 428 {
		t.Fatal(code)
	}
	call(t, confirmed(t, owner, conf), "DELETE", path, nil, nil)
	var next api.AgentP2PResult
	call(t, head, "POST", "/api/p2p/agent/join", map[string]string{"link": makeLink("Eve")}, &next)
	if next.State != model.ApprovalPending || next.Joined != nil {
		t.Fatal("revoked join grant still used")
	}
}

package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestProcessAPIBoundaries(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	anon := client{t: t, base: d.URL}
	path := "/api/agents/head/processes"
	for _, req := range []struct{ method, path string }{
		{"GET", "/api/processes"},
		{"POST", path}, {"POST", "/api/processes/missing/promote"}, {"GET", "/api/system/tools"},
	} {
		if status := agent.do(req.method, req.path, map[string]any{}, nil); status != 403 {
			t.Fatalf("agent %s %s: %d", req.method, req.path, status)
		}
	}
	if status := anon.do("GET", path, nil, nil); status != 401 {
		t.Fatalf("anonymous processes: %d", status)
	}
	if status := agent.do("GET", path, nil, nil); status != 200 {
		t.Fatalf("own processes: %d", status)
	}
	var system []model.ProcessInfo
	if status := owner.do("GET", "/api/system/tools", nil, &system); status != 200 {
		t.Fatal(status)
	}
	found := false
	for _, p := range system {
		if p.ID == "whatsapp" && p.Category == model.CategorySystemTool {
			found = true
		}
	}
	if !found {
		t.Fatal("WhatsApp missing from system tools")
	}
	if err := d.Gate.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	var result fleet.ProcessResult
	if status := agent.do("POST", "/api/processes/requests", map[string]any{"purpose": "test request", "command": "sudo", "args": []string{"true"}}, &result); status != 202 {
		t.Fatalf("agent request: %d", status)
	}
	if status := owner.do("POST", "/api/approvals/"+result.Approval.ID, map[string]any{"decision": "allow"}, nil); status != 428 {
		t.Fatalf("approval bypassed gate: %d", status)
	}
	if status := owner.do("POST", "/api/approvals/"+result.Approval.ID, map[string]any{"decision": "deny"}, nil); status != 200 {
		t.Fatalf("deny: %d", status)
	}
	if status := owner.do("POST", path, map[string]any{"purpose": "test", "command": "must-not-run"}, nil); status != 428 {
		t.Fatalf("command bypassed owner gate: %d", status)
	}
}

package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func workerProject(t *testing.T, d *apitest.Daemon) string {
	t.Helper()
	p, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "Test workers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Fleet.StopAgent(fleet.OwnerCaller, p.LeadID); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestDoneAndProtectionAPIBoundaries(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	var a model.Agent
	if s := head.do("POST", "/api/agents", map[string]any{"name": "head-helper"}, &a); s != 201 || a.ProjectID == "" || a.ParentID != d.Fleet.HeadID() {
		t.Fatalf("head helper: %d %+v", s, a)
	}
	if s := owner.do("POST", "/api/agents", map[string]any{"name": "worker", "project_id": workerProject(t, d)}, &a); s != 201 {
		t.Fatal(s)
	}
	worker := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	if s := head.do("POST", "/api/agents/worker/done", nil, nil); s != 403 {
		t.Fatalf("cross-agent done: %d", s)
	}
	if s := worker.do("PATCH", "/api/agents/worker", map[string]bool{"protected": true}, nil); s != 403 {
		t.Fatalf("self protect: %d", s)
	}
	var update fleet.AgentUpdate
	if s := owner.do("PATCH", "/api/agents/worker", map[string]bool{"protected": true}, &update); s != 200 || !update.Agent.Protected || update.RestartNeeded {
		t.Fatalf("owner protect: %d %+v", s, update)
	}
	if s := worker.do("POST", "/api/agents/worker/done", nil, &a); s != 200 || a.DoneAt == 0 {
		t.Fatalf("done: %d %+v", s, a)
	}
}

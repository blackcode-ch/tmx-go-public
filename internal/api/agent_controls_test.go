package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestAgentControlRoutes(t *testing.T) {
	d := apitest.New(t)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	var a model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "controls", "project_id": workerProject(t, d)}, &a); code != 201 {
		t.Fatal(code)
	}
	w := client{t: t, base: d.URL, token: d.AgentToken(t, a.ID)}
	for _, op := range []string{"shelve", "unshelve", "rename", "answer"} {
		if code := w.do("POST", "/api/agents/controls/"+op, map[string]any{"name": "renamed", "choice": "approve"}, nil); code != 403 {
			t.Fatalf("worker %s: %d", op, code)
		}
	}
	if code := o.do("POST", "/api/agents/controls/rename", map[string]string{"name": "renamed"}, nil); code != 409 {
		t.Fatalf("running rename: %d", code)
	}
	if code := o.do("POST", "/api/sessions/controls/shelve", map[string]string{"note": "Later"}, &a); code != 200 || a.ShelvedAt == 0 || a.Desired != "stopped" {
		t.Fatalf("shelve: %d %+v", code, a)
	}
	id := a.ID
	if code := o.do("POST", "/api/agents/controls/rename", map[string]string{"name": "renamed"}, &a); code != 200 || a.ID != id || a.Name != "renamed" {
		t.Fatalf("rename: %d %+v", code, a)
	}
	if code := o.do("POST", "/api/agents/renamed/rename", map[string]string{"name": "head"}, nil); code != 409 {
		t.Fatalf("reserved name: %d", code)
	}
	a = model.Agent{}
	if code := o.do("POST", "/api/agents/renamed/unshelve", map[string]bool{"start": false}, &a); code != 200 || a.ShelvedAt != 0 || a.Desired != "stopped" {
		t.Fatalf("unshelve: %d %+v", code, a)
	}
	if code := o.do("POST", "/api/agents/renamed/unshelve", map[string]bool{"start": true}, &a); code != 200 || a.Desired != "running" {
		t.Fatalf("resume: %d %+v", code, a)
	}
	if code := o.do("POST", "/api/agents/renamed/answer", map[string]string{"choice": "approve"}, nil); code != 409 {
		t.Fatalf("no prompt: %d", code)
	}
}

func TestAgentRenameRequiresBoundOwnerProof(t *testing.T) {
	d := apitest.New(t)
	o := client{t: t, base: d.URL, token: d.OwnerToken}
	var a model.Agent
	if code := o.do("POST", "/api/agents", map[string]any{"name": "before", "project_id": workerProject(t, d)}, &a); code != 201 {
		t.Fatal(code)
	}
	if _, err := d.Fleet.StopAgent(fleet.OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Gate.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	owner := tc.New(d.URL, d.OwnerToken)
	body := map[string]string{"name": "after"}
	code, conf := call(t, owner, "POST", "/api/agents/before/rename", body, nil)
	if code != 428 {
		t.Fatalf("rename without proof: %d", code)
	}
	proof := confirmed(t, owner, conf)
	if code, _ := call(t, proof, "POST", "/api/agents/before/rename", map[string]string{"name": "different"}, nil); code != 428 {
		t.Fatalf("changed payload: %d", code)
	}
	if code, _ := call(t, proof, "POST", "/api/agents/before/rename", body, &a); code != 0 || a.Name != "after" {
		t.Fatalf("confirmed rename: %d %+v", code, a)
	}
}

package mcp

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"testing"
)

func TestReportCompletionIsExplicit(t *testing.T) {
	d := apitest.New(t)
	p, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "Reports"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := d.Fleet.LaunchAgent(fleet.OwnerCaller, fleet.LaunchRequest{Name: "reporter", ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := d.Fleet.UpdateAgent(fleet.OwnerCaller, w.ID, fleet.AgentPatch{Protected: &on}); err != nil {
		t.Fatal(err)
	}
	cs := session(t, d, w.ID)
	mustOK(t, cs, "report", map[string]any{"body": "done is just text here"})
	a, _ := d.Fleet.Agent(w.ID)
	if a.DoneAt != 0 {
		t.Fatal("report text was interpreted as completion")
	}
	mustOK(t, cs, "report", map[string]any{"body": "work complete; tests pass", "done": true})
	a, _ = d.Fleet.Agent(w.ID)
	if a.DoneAt == 0 {
		t.Fatal("explicit completion was not recorded")
	}
}

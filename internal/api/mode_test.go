package api_test

import (
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestModeRoute(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var w model.Agent
	if s := owner.do("POST", "/api/agents", map[string]string{"name": "w1", "project_id": workerProject(t, d)}, &w); s != 201 {
		t.Fatalf("launch: %d", s)
	}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	if s := head.do("POST", "/api/agents/w1/mode", map[string]string{"mode": "terminal"}, nil); s != 403 {
		t.Fatalf("an agent switched a mode: %d", s)
	}
	if s := owner.do("POST", "/api/agents/w1/mode", map[string]string{"mode": "screen"}, nil); s != 400 {
		t.Fatalf("unknown mode: %d", s)
	}
	// Mid-turn: it waits for the end of the turn.
	p := d.Chat.Last(w.ID)
	p.State(model.StateWorking)
	deadline := time.Now().Add(3 * time.Second)
	for a, _ := d.Fleet.Agent("w1"); a.State != model.StateWorking; a, _ = d.Fleet.Agent("w1") {
		if time.Now().After(deadline) {
			t.Fatal("not working")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var out fleet.ModeChange
	if s := owner.do("POST", "/api/agents/w1/mode", map[string]string{"mode": "terminal"}, &out); s != 200 || !out.Pending ||
		out.Agent.PendingMode != model.ModeTerminal || out.Note == "" {
		t.Fatalf("pending: %d %+v", s, out)
	}
	out = fleet.ModeChange{}
	if s := owner.do("POST", "/api/agents/w1/mode", map[string]string{"mode": "chat"}, &out); s != 200 || out.Agent.PendingMode != "" {
		t.Fatalf("cancel: %d %+v", s, out)
	}
}

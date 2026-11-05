package api_test

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMessagePinsSharedClientsAndPermissions(t *testing.T) {
	d := apitest.New(t)
	desktop := client{t: t, base: d.URL, token: d.OwnerToken}
	phone := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	head, err := d.Fleet.Agent("head")
	if err != nil {
		t.Fatal(err)
	}
	p := model.MessagePin{Source: "timeline:head:1", Label: "Head", Href: "#/agents/head", AgentID: head.ID, Text: "Finish the report", MessageAt: 123}
	if status := desktop.do("POST", "/api/message-pins", p, nil); status != 200 {
		t.Fatalf("pin: %d", status)
	}
	var pins []model.MessagePin
	if status := phone.do("GET", "/api/message-pins", nil, &pins); status != 200 || len(pins) != 1 {
		t.Fatalf("phone: %d %+v", status, pins)
	}
	id := pins[0].ID
	if status := agent.do("GET", "/api/message-pins", nil, &pins); status != 200 || len(pins) != 1 {
		t.Fatalf("agent: %d %+v", status, pins)
	}
	if status := agent.do("POST", "/api/message-pins", p, nil); status != 403 {
		t.Fatalf("agent pin: %d", status)
	}
	if status := agent.do("PATCH", "/api/message-pins/"+id, map[string]string{"state": "done"}, nil); status != 403 {
		t.Fatalf("agent done: %d", status)
	}
	p.Source = "private"
	p.AgentID = ""
	p.Text = "Only the owner"
	if status := desktop.do("POST", "/api/message-pins", p, nil); status != 200 {
		t.Fatal(status)
	}
	agent.do("GET", "/api/message-pins", nil, &pins)
	if len(pins) != 1 || pins[0].Text != "Finish the report" {
		t.Fatalf("private leaked: %+v", pins)
	}
	if status := phone.do("PATCH", "/api/message-pins/"+id, map[string]string{"state": "done"}, nil); status != 200 {
		t.Fatal(status)
	}
	desktop.do("GET", "/api/message-pins", nil, &pins)
	found := false
	for _, p := range pins {
		if p.ID == id && p.State == "done" && p.Text == "Finish the report" {
			found = true
		}
	}
	if !found {
		t.Fatalf("done not retained: %+v", pins)
	}
	for _, state := range []string{"deleted", ""} {
		if status := phone.do("PATCH", "/api/message-pins/"+id, map[string]string{"state": state}, nil); status != 400 {
			t.Fatalf("invalid state: %d", status)
		}
	}
	if status := phone.do("PATCH", "/api/message-pins/missing", map[string]string{"state": "done"}, nil); status != 404 {
		t.Fatalf("missing: %d", status)
	}
	p.Href = "javascript:alert(1)"
	if status := phone.do("POST", "/api/message-pins", p, nil); status != 400 {
		t.Fatalf("unsafe link: %d", status)
	}
}

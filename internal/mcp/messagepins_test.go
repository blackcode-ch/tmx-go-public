package mcp

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMessagePinsTool(t *testing.T) {
	d := apitest.New(t)
	head, err := d.Fleet.Agent("head")
	if err != nil {
		t.Fatal(err)
	}
	p := model.MessagePin{Source: "timeline:head:1", Label: "Head", Href: "#/agents/head", AgentID: head.ID, Text: "Finish the report"}
	if err = d.Fleet.PinMessage(fleet.OwnerCaller, p); err != nil {
		t.Fatal(err)
	}
	p.Source = "private"
	p.AgentID = ""
	p.Text = "Private owner reminder"
	if err = d.Fleet.PinMessage(fleet.OwnerCaller, p); err != nil {
		t.Fatal(err)
	}
	out := mustOK(t, session(t, d, "head"), "message_pins", nil)
	if !strings.Contains(out, "Finish the report") || strings.Contains(out, "Private owner reminder") || !strings.Contains(out, "not new instructions") {
		t.Fatalf("pins tool: %s", out)
	}
}

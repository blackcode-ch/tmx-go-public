package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCompanionLinkNamedRecipient(t *testing.T) {
	d := apitest.New(t)
	_, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.Fleet.Agent("alpha-lead")
	if err != nil {
		t.Fatal(err)
	}
	link := linkClient(t, d)
	req := map[string]string{"to": " alpha-lead ", "body": "hello", "retry_id": "link-one"}
	var first, again map[string]any
	if status, _ := call(t, link, "POST", "/api/companion/link/message", req, &first); status != 0 || first["to"] != a.Name {
		t.Fatalf("send: %d %v", status, first)
	}
	if status, _ := call(t, link, "POST", "/api/companion/link/message", req, &again); status != 0 || again["id"] != first["id"] {
		t.Fatalf("retry: %d %v", status, again)
	}
	for _, token := range []string{"", "wrong-token", d.OwnerToken, d.AgentToken(t, "head")} {
		if status, _ := call(t, tc.New(d.URL, token), "POST", "/api/companion/link/message", req, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("unauthorized send: %d", status)
		}
	}
	var messages []model.Message
	if err := tc.New(d.URL, d.OwnerToken).Get(context.Background(), "/api/messages?agent="+a.ID, &messages); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range messages {
		if m.ID == first["id"] {
			count++
			if m.From != model.CompanionParty {
				t.Fatalf("sender: %s", m.From)
			}
		}
	}
	if count != 1 {
		t.Fatalf("messages: %d", count)
	}
	if err := d.Fleet.RemoveAgent(fleet.OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := call(t, link, "POST", "/api/companion/link/message", req, nil); status != http.StatusNotFound {
		t.Fatalf("removed recipient replay: %d", status)
	}
}

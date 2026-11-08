package api_test

import (
	"net/http"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCompanionMailboxRoundTrip(t *testing.T) {
	d := apitest.New(t)
	link := linkClient(t, d)
	_, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "mailbox"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.Fleet.Agent("mailbox-lead")
	if err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"id": "1234567890abcdef", "to": a.Name, "from": "companion", "body": "question", "urgent": false}
	var first, again map[string]any
	if status, _ := call(t, link, "POST", "/api/messages", req, &first); status != 0 || first["id"] != req["id"] {
		t.Fatalf("send %d %v", status, first)
	}
	reply, err := d.Fleet.SendMessage(fleet.AgentCaller(a.ID), fleet.SendRequest{Body: "answer", ReplyTo: first["id"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	if reply.To != model.CompanionParty || reply.ReplyTo != first["id"] {
		t.Fatalf("reply %+v", reply)
	}
	if _, err = d.Fleet.SendMessage(fleet.AgentCaller("wrong-agent"), fleet.SendRequest{Body: "stolen", ReplyTo: first["id"].(string)}); err == nil {
		t.Fatal("unrelated agent replied")
	}
	var page struct {
		Agent    string
		Total    int
		Messages []map[string]any
	}
	if status, _ := call(t, link, "GET", "/api/inbox?agent=companion&box=in&unread=1", nil, &page); status != 0 || page.Agent != "companion" || page.Total != 1 || len(page.Messages) != 1 {
		t.Fatalf("page %d %+v", status, page)
	}
	var detail map[string]any
	if status, _ := call(t, link, "GET", "/api/messages/"+reply.ID, nil, &detail); status != 0 || detail["sender"] != a.Name || detail["reply_to"] != first["id"] || detail["body"] != "answer" {
		t.Fatalf("detail %d %v", status, detail)
	}
	var response map[string]any
	if status, _ := call(t, link, "POST", "/api/messages/"+reply.ID+"/reply", map[string]string{"from": "companion", "body": "thanks"}, &response); status != 0 || response["reply_to"] != reply.ID {
		t.Fatalf("reply route %d %v", status, response)
	}
	if status, _ := call(t, link, "POST", "/api/messages/"+reply.ID+"/read", nil, nil); status != 0 {
		t.Fatalf("read %d", status)
	}
	if status, _ := call(t, link, "GET", "/api/inbox?agent=companion&unread=1", nil, &page); status != 0 || page.Total != 0 {
		t.Fatalf("unread %d %+v", status, page)
	}
	if err := d.Fleet.RemoveAgent(fleet.OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := call(t, link, "POST", "/api/messages", req, &again); status != 0 || again["id"] != first["id"] {
		t.Fatalf("replay after removal %d %v", status, again)
	}
	req["urgent"] = true
	if status, _ := call(t, link, "POST", "/api/messages", req, nil); status != http.StatusConflict {
		t.Fatalf("different urgent %d", status)
	}
	req["id"] = "abcdef1234567890"
	if status, _ := call(t, link, "POST", "/api/messages", req, nil); status != http.StatusNotFound {
		t.Fatalf("new stale send %d", status)
	}
}

func TestCompanionMailboxBoundaries(t *testing.T) {
	d := apitest.New(t)
	link := linkClient(t, d)
	for _, token := range []string{"", "invalid", d.OwnerToken, d.AgentToken(t, "head")} {
		if status, _ := call(t, tc.New(d.URL, token), "GET", "/api/inbox?agent=companion", nil, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("mailbox authorization %d", status)
		}
	}
	if status, _ := call(t, link, "GET", "/api/inbox?agent=head", nil, nil); status != http.StatusForbidden {
		t.Fatalf("other inbox %d", status)
	}
	if status, _ := call(t, link, "POST", "/api/messages", map[string]string{"id": "1234567890abcdef", "to": "head", "from": "owner", "body": "spoof"}, nil); status != http.StatusForbidden {
		t.Fatalf("spoof %d", status)
	}
	m, err := d.Fleet.SendMessage(fleet.OwnerCaller, fleet.SendRequest{To: "head", Body: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := call(t, link, "GET", "/api/messages/"+m.ID, nil, nil); status != http.StatusNotFound {
		t.Fatalf("private mail %d", status)
	}
}

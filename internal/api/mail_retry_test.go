package api_test

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMailRetryOwnerRouteAndVisibleReceipt(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	request := map[string]string{"text": "retry safely", "retry_id": "owner-request"}
	var first, again model.Message
	if status := owner.do("POST", "/api/agents/head/send", request, &first); status != 200 {
		t.Fatalf("first %d", status)
	}
	if status := owner.do("POST", "/api/agents/head/send", request, &again); status != 200 || again.ID != first.ID {
		t.Fatalf("retry %d %+v", status, again)
	}
	request["text"] = "different"
	if status := owner.do("POST", "/api/agents/head/send", request, nil); status != 409 {
		t.Fatalf("changed request %d", status)
	}
	var messages []model.Message
	if status := owner.do("GET", "/api/messages?agent=head", nil, &messages); status != 200 {
		t.Fatal(status)
	}
	found := false
	for _, m := range messages {
		if m.ID == first.ID {
			found = m.Receipt == "accepted" && m.ReceiptAt > 0
		}
	}
	if !found {
		t.Fatal("owner cannot see delivery receipt")
	}
}

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	tc "github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestRoomAgentAddressAPI(t *testing.T) {
	d := apitest.New(t)
	snap, err := d.Fleet.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	owner := tc.New(d.URL, d.OwnerToken)
	for _, address := range []string{"head@" + snap.Machine, "tmx:" + snap.Machine + ":head", "HEAD@" + strings.ToUpper(snap.Machine), "TMX:" + strings.ToUpper(snap.Machine) + ":HEAD"} {
		var a model.Agent
		if err := owner.Get(context.Background(), "/api/agents/"+tc.Seg(address), &a); err != nil || a.Name != "head" {
			t.Fatalf("get %s: %+v %v", address, a, err)
		}
		var m model.Message
		if err := owner.Post(context.Background(), "/api/messages", map[string]string{"to": address, "body": "hello"}, &m); err != nil || m.To != a.ID {
			t.Fatalf("send %s: %+v %v", address, m, err)
		}
		if status, _ := call(t, tc.New(d.URL, ""), "GET", "/api/agents/"+tc.Seg(address), nil, nil); status != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", status)
		}
	}
	if status, _ := call(t, owner, "GET", "/api/agents/"+tc.Seg("tmx:another-host:head"), nil, nil); status != http.StatusNotFound {
		t.Fatalf("remote get: %d", status)
	}
	if status, _ := call(t, owner, "GET", "/api/agents/"+tc.Seg("tmx::head"), nil, nil); status != http.StatusBadRequest {
		t.Fatalf("malformed get: %d", status)
	}
}

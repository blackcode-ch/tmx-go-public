package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

// tmx chat admin against a stub: the person is found by name, the daemon's 428 is answered
// by waiting for the owner's confirmation and retrying with it, and --cancel withdraws.
func TestChatAdminAgainstStub(t *testing.T) {
	var mu sync.Mutex
	var offers []map[string]any
	var headers []string
	cancels := 0
	group := model.Chat{ID: "g1", Kind: model.ChatGroup, Name: "Ops", Status: model.ChatActive, Admin: true, Members: []model.ChatMember{
		{PeerID: "p-me", Name: "Sam", Role: model.RoleAdmin, Me: true, Handover: true},
		{PeerID: "p-bo", Name: "Bo", Role: model.RoleMember, Handover: true},
		{PeerID: "p-cleo", Name: "Cleo", Role: model.RoleMember},
		{PeerID: "p-dana", Name: "Dana", Role: model.RoleMember, LeftAt: 5},
	}}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/health":
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == "GET" && r.URL.Path == "/api/chats":
			json.NewEncoder(w).Encode([]model.Chat{group, {ID: "dm-1", Kind: model.ChatDM, Name: "Bo", Status: model.ChatActive}})
		case r.Method == "POST" && r.URL.Path == "/api/chats/g1/admin":
			headers = append(headers, r.Header.Get("X-Tmx-Confirmation"))
			if r.Header.Get("X-Tmx-Confirmation") != "cf1" {
				w.WriteHeader(http.StatusPreconditionRequired)
				w.Write([]byte(`{"error":"Owner confirmation needed","confirmation":{"id":"cf1","reason":"make Bo the admin of Ops","state":"pending","method_label":"Touch ID"}}`))
				return
			}
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			offers = append(offers, b)
			c := group
			c.AdminOffer = &model.ChatAdminOffer{PeerID: "p-bo", Name: "Bo"}
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(c)
		case r.Method == "GET" && r.URL.Path == "/api/gate/confirmations/cf1":
			w.Write([]byte(`{"id":"cf1","reason":"make Bo the admin of Ops","state":"confirmed"}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/chats/g1/admin":
			cancels++
			json.NewEncoder(w).Encode(group)
		default:
			http.Error(w, `{"error":"no such route"}`, 404)
		}
	}))
	defer stub.Close()
	useHome(t, t.TempDir(), urlPort(t, stub.URL))

	out, errs, code := tmx(t, "chat", "admin", "ops", "bo", "--leave")
	if code != 0 || !strings.Contains(out, "Ops: waiting for Bo's tmx") || !strings.Contains(out, "You leave the group once") {
		t.Fatalf("admin: %d %q %q", code, out, errs)
	}
	if !strings.Contains(errs, "Confirm on this machine with Touch ID: tmx is trying to make Bo the admin of Ops.") ||
		!strings.Contains(errs, "1 member may need to update tmx first") {
		t.Fatalf("stderr: %q", errs)
	}
	mu.Lock()
	if len(headers) != 2 || headers[0] != "" || headers[1] != "cf1" || len(offers) != 1 || offers[0]["peer_id"] != "p-bo" || offers[0]["then_leave"] != true {
		t.Fatalf("requests: %q %+v", headers, offers)
	}
	mu.Unlock()
	if out, errs, code := tmx(t, "chat", "admin", "g1", "--cancel"); code != 0 || !strings.Contains(out, "offer withdrawn") || cancels != 1 {
		t.Fatalf("cancel: %d %q %q", code, out, errs)
	}
	for _, bad := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"ops"}, 2, "usage: tmx chat admin"},
		{[]string{"ops", "bo", "cleo"}, 2, "usage: tmx chat admin"},
		{[]string{"ops", "dana"}, 1, `no other member "dana"`},
		{[]string{"ops", "sam"}, 1, `no other member "sam"`},
		{[]string{"dm-1", "bo"}, 1, "only a group has an admin"},
	} {
		if _, errs, code := tmx(t, append([]string{"chat", "admin"}, bad.args...)...); code != bad.code || !strings.Contains(errs, bad.want) {
			t.Errorf("%v: %d %q", bad.args, code, errs)
		}
	}
}

// The same between two real installs (the test gate answers the prompt): bo runs the group.
func TestChatAdminBetweenTwoInstalls(t *testing.T) {
	nw := p2ptest.NewNetwork()
	a := apitest.NewWithNode(t, nw.NewNode("alex"))
	b := apitest.NewWithNode(t, nw.NewNode("bo"))
	g, err := a.Chats.Create(chat.CreateRequest{Name: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := a.Chats.CreateInvite(chat.InviteRequest{Kind: p2p.InviteGroup, GroupID: g.ID})
	if _, err := b.Chats.Join(context.Background(), inv.Link); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bo in the group", func() bool {
		c, err := b.Chats.Chat(g.ID)
		return err == nil && c.Status == model.ChatActive
	})
	useDaemon(t, a)
	out, errs, code := tmx(t, "chat", "admin", "trip", "bo")
	if code != 0 || !strings.Contains(errs, "tmx is trying to make bo the admin of Trip") {
		t.Fatalf("admin: %d %q %q", code, out, errs)
	}
	eventually(t, "bo is the admin", func() bool {
		c, err := b.Chats.ChatFor(fleet.OwnerCaller, g.ID)
		return err == nil && c.Admin
	})
	if _, errs, code := tmx(t, "chat", "admin", "trip", "bo"); code != 1 || !strings.Contains(errs, "only the group's admin") {
		t.Fatalf("again: %d %q", code, errs)
	}
}

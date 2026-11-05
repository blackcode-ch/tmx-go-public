package api_test

import (
	"encoding/base64"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func TestChatFilesAndMentionsOverAPI(t *testing.T) {
	net := p2ptest.NewNetwork()
	a, b := apitest.NewWithNode(t, net.NewNode("alex")), apitest.NewWithNode(t, net.NewNode("bo"))
	alex := client{t: t, base: a.URL, token: a.OwnerToken}
	bo := client{t: t, base: b.URL, token: b.OwnerToken}
	var inv struct{ Link string }
	alex.do("POST", "/api/invites", map[string]string{"kind": "dm"}, &inv)
	var joined chat.JoinResult
	if s := bo.do("POST", "/api/invites/join", map[string]string{"link": inv.Link}, &joined); s != 200 {
		t.Fatalf("join: %d", s)
	}
	dm := joined.Chat.ID
	waitFor(t, "alex's DM", func() bool {
		var c model.Chat
		return alex.do("GET", "/api/chats/"+dm, nil, &c) == 200 && c.Status == model.ChatActive
	})

	// Who can be mentioned: bo sees alex.
	var mm struct{ Mentionable []chat.Mentionable }
	if s := bo.do("GET", "/api/chats/"+dm+"/mentionable", nil, &mm); s != 200 || len(mm.Mentionable) == 0 || mm.Mentionable[0].Label != "alex" {
		t.Fatalf("mentionable: %d %+v", s, mm)
	}

	data := []byte("\x89PNG\r\n\x1a\n" + "pixels")
	var f model.ChatFile
	if s := alex.do("POST", "/api/chats/"+dm+"/files", map[string]string{"name": "shot.png", "data": base64.StdEncoding.EncodeToString(data)}, &f); s != 201 ||
		f.Type != "image/png" || !f.Image {
		t.Fatalf("upload: %d %+v", s, f)
	}
	agent := client{t: t, base: a.URL, token: a.AgentToken(t, "head")}
	if s := agent.do("POST", "/api/chats/"+dm+"/files", map[string]string{"name": "x", "data": "eA=="}, nil); s != 403 {
		t.Fatalf("agent upload: %d", s)
	}
	var m model.ChatMessage
	if s := alex.do("POST", "/api/chats/"+dm+"/messages", map[string]any{"body": "@bo see this", "files": []string{f.ID}}, &m); s != 201 ||
		len(m.Files) != 1 || len(m.Mentions) != 1 {
		t.Fatalf("send: %d %+v", s, m)
	}
	var got struct {
		model.ChatFile
		Data string
	}
	waitFor(t, "bo has the file", func() bool {
		return bo.do("GET", "/api/chats/"+dm+"/files/"+f.ID+"?data=1", nil, &got) == 200
	})
	if got.Data != base64.StdEncoding.EncodeToString(data) || got.State != model.ChatFileReady {
		t.Fatalf("bo's file: %+v", got.ChatFile)
	}
	var u chat.Unread
	if bo.do("GET", "/api/chats/unread", nil, &u); u.Mentions != 1 {
		t.Fatalf("unread: %+v", u)
	}
}

func TestSetP2PName(t *testing.T) {
	net := p2ptest.NewNetwork()
	node := net.NewNode("alex")
	d := apitest.NewWithNode(t, node)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var st struct{ Name string }
	if s := owner.do("PUT", "/api/p2p/name", map[string]string{"name": "  Alex   W "}, &st); s != 200 || st.Name != "Alex W" || node.DisplayName() != "Alex W" {
		t.Fatalf("set: %d %+v", s, st)
	}
	if s := owner.do("PUT", "/api/p2p/name", map[string]string{"name": " "}, nil); s != 400 {
		t.Fatalf("empty: %d", s)
	}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	if s := agent.do("PUT", "/api/p2p/name", map[string]string{"name": "x"}, nil); s != 403 {
		t.Fatalf("agent: %d", s)
	}
}

func TestUpdateRoutesOwnerOnly(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	agent := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	for _, r := range [][2]string{{"GET", "/api/update"}, {"POST", "/api/update/check"}, {"POST", "/api/update/install"}, {"PUT", "/api/update"}, {"PUT", "/api/update/token"}, {"GET", "/api/update/keys"}, {"PUT", "/api/update/keys"}, {"PUT", "/api/update/drill"}} {
		if s := agent.do(r[0], r[1], map[string]any{}, nil); s != 403 {
			t.Errorf("agent %s %s: %d", r[0], r[1], s)
		}
		if s := owner.do(r[0], r[1], map[string]any{}, nil); s != 501 { // this test daemon has no updater
			t.Errorf("owner %s %s: %d", r[0], r[1], s)
		}
	}
}

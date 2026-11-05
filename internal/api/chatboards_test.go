package api_test

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/chat"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
	"github.com/blackcode-ch/tmx-go/internal/p2p/p2ptest"
)

func TestChatBoardRoutes(t *testing.T) {
	net := p2ptest.NewNetwork()
	a, b := apitest.NewWithNode(t, net.NewNode("alex")), apitest.NewWithNode(t, net.NewNode("bo"))
	alex := client{t: t, base: a.URL, token: a.OwnerToken}
	bo := client{t: t, base: b.URL, token: b.OwnerToken}
	boHead := client{t: t, base: b.URL, token: b.AgentToken(t, "head")}

	var g model.Chat
	if s := alex.do("POST", "/api/chats", map[string]string{"name": "Trip"}, &g); s != 201 {
		t.Fatalf("group: %d", s)
	}
	var inv p2p.Invite
	alex.do("POST", "/api/invites", map[string]string{"kind": "group", "group_id": g.ID}, &inv)
	var joined chat.JoinResult
	if s := bo.do("POST", "/api/invites/join", map[string]string{"link": inv.Link}, &joined); s != 200 {
		t.Fatalf("join: %d", s)
	}
	waitFor(t, "bo is in", func() bool {
		var c model.Chat
		bo.do("GET", "/api/chats/"+g.ID, nil, &c)
		return c.Status == model.ChatActive
	})

	// A topic, a message in it, a log line, a move, an order.
	var tp model.ChatTopic
	if s := alex.do("POST", "/api/chats/"+g.ID+"/topics", map[string]string{"title": "Hotels", "color": "teal"}, &tp); s != 201 || tp.ID == "" {
		t.Fatalf("topic: %d %+v", s, tp)
	}
	if s := alex.do("POST", "/api/chats/"+g.ID+"/topics", map[string]string{"title": "HOTELS"}, nil); s != 409 {
		t.Fatalf("the same title: %d", s)
	}
	if s := alex.do("POST", "/api/chats/"+g.ID+"/topics", map[string]string{"title": "x", "color": "plaid"}, nil); s != 400 {
		t.Fatalf("a bad colour: %d", s)
	}
	var m model.ChatMessage
	if s := alex.do("POST", "/api/chats/"+g.ID+"/messages", map[string]string{"body": "Casa Mulin?", "topic": tp.ID}, &m); s != 201 || m.Topic != tp.ID {
		t.Fatalf("send in topic: %d %+v", s, m)
	}
	var e model.ChatTopicEntry
	if s := alex.do("POST", "/api/chats/"+g.ID+"/board/log", map[string]string{"topic": tp.ID, "kind": "decision", "text": "Casa Mulin."}, &e); s != 201 {
		t.Fatalf("log: %d", s)
	}
	if s := alex.do("POST", "/api/chats/"+g.ID+"/board/log", map[string]string{"topic": tp.ID, "kind": "decision", "text": "casa mulin."}, nil); s != 200 {
		t.Fatalf("the same line: %d", s)
	}
	if s := alex.do("POST", "/api/chats/"+g.ID+"/board/log", map[string]string{"topic": tp.ID, "kind": "info", "text": "token: abcdef123456"}, nil); s != 400 {
		t.Fatalf("a secret: %d", s)
	}
	if s := alex.do("PUT", "/api/chats/"+g.ID+"/board/order", map[string][]string{"ids": {tp.ID, "general"}}, nil); s != 200 {
		t.Fatalf("order: %d", s)
	}
	var ab model.ChatBoard
	if alex.do("GET", "/api/chats/"+g.ID+"/board", nil, &ab); !ab.Ordered || ab.Topics[0].ID != tp.ID || len(ab.Messages) != 1 || len(ab.Entries) != 1 {
		t.Fatalf("alex's board: %+v", ab)
	}

	// bo's board has the same topics, message and log (in bo's own order: none).
	var bb model.ChatBoard
	waitFor(t, "bo's board", func() bool {
		bo.do("GET", "/api/chats/"+g.ID+"/board", nil, &bb)
		return len(bb.Topics) == 2 && len(bb.Messages) == 1 && bb.Messages[0].Topic == tp.ID && len(bb.Entries) == 1
	})
	if bb.Ordered || bb.Topics[0].ID != "general" {
		t.Fatalf("bo's order: %+v", bb.Topics)
	}
	if s := bo.do("POST", "/api/chats/"+g.ID+"/board/move", map[string]any{"messages": []string{m.ID}, "topic": "general"}, nil); s != 200 {
		t.Fatalf("move: %d", s)
	}
	arch := true
	if s := bo.do("PATCH", "/api/chats/"+g.ID+"/topics/"+tp.ID, map[string]any{"archived": arch}, nil); s != 200 {
		t.Fatalf("archive: %d", s)
	}
	if s := bo.do("PATCH", "/api/chats/"+g.ID+"/topics/general", map[string]any{"title": "Misc"}, nil); s != 400 {
		t.Fatalf("rename General: %d", s)
	}
	if s := bo.do("PATCH", "/api/chats/"+g.ID+"/topics/tnope", map[string]any{"title": "Misc"}, nil); s != 404 {
		t.Fatalf("unknown topic: %d", s)
	}
	waitFor(t, "alex sees the move and the archive", func() bool {
		var x model.ChatBoard // fresh: "topic" is left out when it is General
		alex.do("GET", "/api/chats/"+g.ID+"/board?per_topic=5", nil, &x)
		return len(x.Messages) == 1 && x.Messages[0].Topic == "" && x.Topics[0].Archived && x.Topics[1].Messages == 1
	})

	// The participating agent reads the board and writes in a topic; it changes nothing else.
	var hb model.ChatBoard
	if s := boHead.do("GET", "/api/chats/"+g.ID+"/board?per_topic=0", nil, &hb); s != 200 || len(hb.Messages) != 0 || len(hb.Topics) != 2 {
		t.Fatalf("head board: %d %+v", s, hb)
	}
	var hm model.ChatMessage
	if s := boHead.do("POST", "/api/chats/"+g.ID+"/messages", map[string]string{"body": "found trains", "topic": "Trains"}, &hm); s != 201 || hm.Topic == "" {
		t.Fatalf("head writes in a new topic: %d %+v", s, hm)
	}
	// It organises the board as well: opens and changes topics, files messages under them.
	var ht model.ChatTopic
	if s := boHead.do("POST", "/api/chats/"+g.ID+"/topics", map[string]string{"title": "Hotels"}, &ht); s != 201 || ht.Title != "Hotels" {
		t.Fatalf("agent opens a topic: %d %+v", s, ht)
	}
	if s := boHead.do("PATCH", "/api/chats/"+g.ID+"/topics/"+ht.ID, map[string]any{"color": "teal"}, &ht); s != 200 || ht.Color != "teal" {
		t.Fatalf("agent recolours its topic: %d %+v", s, ht)
	}
	if s := boHead.do("POST", "/api/chats/"+g.ID+"/board/move", map[string]any{"messages": []string{hm.ID}, "topic": ht.ID}, nil); s != 200 {
		t.Fatalf("agent moves a message: %d", s)
	}
	// Log lines and the order of the strings stay the owner's.
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/chats/" + g.ID + "/board/log"}, {"PUT", "/api/chats/" + g.ID + "/board/order"},
	} {
		if s := boHead.do(r.method, r.path, map[string]string{"title": "x"}, nil); s != 403 {
			t.Errorf("agent %s %s: %d", r.method, r.path, s)
		}
	}
	waitFor(t, "alex sees the head's topic and the move", func() bool {
		var ab model.ChatBoard
		alex.do("GET", "/api/chats/"+g.ID+"/board?per_topic=5", nil, &ab)
		for _, x := range ab.Topics {
			if x.Title == "Hotels" && x.Color == "teal" && x.Messages == 1 {
				return true
			}
		}
		return false
	})
	if s := bo.do("GET", "/api/chats/nochat/board", nil, nil); s != 404 {
		t.Fatalf("unknown chat: %d", s)
	}
	waitFor(t, "alex has the head's topic", func() bool {
		var ab model.ChatBoard
		alex.do("GET", "/api/chats/"+g.ID+"/board", nil, &ab)
		for _, x := range ab.Topics {
			if x.Title == "Trains" && strings.HasPrefix(x.ID, "t") {
				return true
			}
		}
		return false
	})

	// Off: 503.
	d := apitest.New(t)
	off := client{t: t, base: d.URL, token: d.OwnerToken}
	if s := off.do("GET", "/api/chats/x/board", nil, nil); s != 503 {
		t.Fatalf("p2p off: %d", s)
	}
}

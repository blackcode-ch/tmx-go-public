package api_test

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestBoardRoutes(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	head := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	for _, r := range [][2]string{{"GET", "/api/board"}, {"POST", "/api/board/move"}, {"POST", "/api/threads"}} {
		if s := head.do(r[0], r[1], map[string]any{}, nil); s != 403 {
			t.Errorf("agent %s %s: %d", r[0], r[1], s)
		}
	}
	var th model.Thread
	if s := owner.do("POST", "/api/threads", map[string]any{"title": "Hiring", "color": "amber"}, &th); s != 201 || th.Kind != model.ThreadTopic {
		t.Fatalf("create topic: %d %+v", s, th)
	}
	if s := owner.do("POST", "/api/threads", map[string]any{"title": "hiring"}, nil); s != 409 {
		t.Fatalf("duplicate topic: %d", s)
	}
	if s := owner.do("PATCH", "/api/threads/"+th.ID, map[string]any{"title": "Hiring 2027"}, &th); s != 200 || th.Title != "Hiring 2027" {
		t.Fatalf("rename: %d %+v", s, th)
	}
	if s := owner.do("POST", "/api/agents/head/send", map[string]any{"text": "find a designer", "thread_id": th.ID}, nil); s != 200 {
		t.Fatalf("send in topic: %d", s)
	}
	if s := owner.do("POST", "/api/agents/head/send", map[string]any{"text": "x", "thread_id": "t-nope"}, nil); s != 400 {
		t.Fatalf("send to an unknown topic: %d", s)
	}
	var b model.Board
	if s := owner.do("GET", "/api/board", nil, &b); s != 200 || b.HeadID == "" || len(b.Threads) != 2 {
		t.Fatalf("board: %d %+v", s, b)
	}
	// The owner is not the head: it moves cards, it does not file turns.
	if s := owner.do("POST", "/api/board/file", map[string]any{"title": "x"}, nil); s != 403 {
		t.Fatalf("owner filed a turn: %d", s)
	}
	var filed struct {
		Thread model.Thread `json:"thread"`
		Topics []string     `json:"topics"`
	}
	if s := head.do("POST", "/api/board/file", map[string]any{"title": "Design"}, &filed); s != 200 || filed.Thread.Title != "Design" || len(filed.Topics) != 3 {
		t.Fatalf("head files a turn: %d %+v", s, filed)
	}
	// The head arranges topics too (by ID or title); deleting moves the cards to General.
	if s := head.do("PATCH", "/api/threads/design", map[string]any{"archived": true}, &th); s != 200 || !th.Archived {
		t.Fatalf("head archives: %d %+v", s, th)
	}
	var list []struct {
		model.Thread
		Cards int `json:"cards"`
	}
	if s := head.do("GET", "/api/threads", nil, &list); s != 200 || len(list) != 3 {
		t.Fatalf("head lists: %d %+v", s, list)
	}
	if s := owner.do("DELETE", "/api/threads/general", nil, nil); s != 400 {
		t.Fatalf("deleted General: %d", s)
	}
	if s := head.do("DELETE", "/api/threads/Design", nil, nil); s != 200 {
		t.Fatalf("head deletes: %d", s)
	}
	if s := owner.do("DELETE", "/api/threads/"+th.ID, nil, nil); s != 404 {
		t.Fatalf("deleted twice: %d", s)
	}

	// Logs: 201 for a new line, 200 for the same line again; the owner sets the order.
	var e model.ThreadEntry
	if s := owner.do("POST", "/api/board/log", map[string]any{"thread": "general", "kind": "decision", "text": "Ship Fridays."}, &e); s != 201 || e.ID == "" {
		t.Fatalf("add entry: %d %+v", s, e)
	}
	if s := owner.do("POST", "/api/board/log", map[string]any{"thread": "general", "kind": "decision", "text": "ship fridays."}, nil); s != 200 {
		t.Fatalf("same entry: %d", s)
	}
	var log struct {
		Entries []model.ThreadEntry `json:"entries"`
	}
	if s := head.do("GET", "/api/board/log?thread=general", nil, &log); s != 200 || len(log.Entries) != 1 {
		t.Fatalf("head reads the log: %d %+v", s, log)
	}
	if s := head.do("PUT", "/api/board/order", map[string]any{"ids": []string{"general"}}, nil); s != 403 {
		t.Fatalf("the head set the order: %d", s)
	}
	if s := owner.do("PUT", "/api/board/order", map[string]any{"ids": []string{"general"}}, nil); s != 200 {
		t.Fatalf("owner sets the order: %d", s)
	}
	if s := owner.do("GET", "/api/board", nil, &b); s != 200 || !b.Ordered || len(b.Entries) != 1 {
		t.Fatalf("board after order: %d %+v", s, b)
	}
}

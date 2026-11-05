package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestChatTopicsStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tmx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := model.Chat{ID: "g1", Kind: model.ChatGroup, Name: "Trip", CreatedBy: "me", Status: model.ChatActive, CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutChat(c); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 7; i++ {
		if _, _, err := s.InsertChatMessage(model.ChatMessage{ID: fmt.Sprintf("m%d", i), ChatID: "g1", Sender: "p1", Body: "x",
			At: int64(i), State: model.ChatDelivered, ReceivedAt: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	s.InsertChatMessage(model.ChatMessage{ID: "n1", ChatID: "g1", Sender: model.ChatSystem, Body: "joined", At: 8, ReceivedAt: 8})

	// The register: a later change wins, at the same time the greater peer ID; older ones lose.
	for _, x := range []struct {
		msg, topic string
		at         int64
		by         string
		want       bool
	}{
		{"m1", "ta", 10, "p1", true}, {"m1", "tb", 9, "p9", false}, {"m1", "tb", 10, "p0", false},
		{"m1", "tb", 10, "p2", true}, {"m1", "tb", 10, "p2", false}, {"m1", "", 11, "p0", true},
		{"m2", "ta", 1, "p1", true}, {"m3", "ta", 1, "p1", true}, {"m4", "ta", 1, "p1", true}, {"m5", "tb", 1, "p1", true},
		{"later", "tb", 1, "p1", true}, // a move whose message is not here yet
	} {
		got, err := s.SetMessageTopic("g1", x.msg, x.topic, x.at, x.by)
		if err != nil || got != x.want {
			t.Fatalf("%+v: %v %v", x, got, err)
		}
	}
	if topic, at, by, _ := s.MessageTopic("g1", "m1"); topic != "" || at != 11 || by != "p0" {
		t.Fatalf("m1: %q %d %q", topic, at, by)
	}
	counts, _ := s.TopicMessageCounts("g1")
	if counts[""] != 3 || counts["ta"] != 3 || counts["tb"] != 1 || len(counts) != 3 {
		t.Fatalf("counts %v", counts)
	}
	// The newest two of each topic, oldest first, notices left out.
	ms, err := s.BoardMessages("g1", 2)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, m := range ms {
		got += m.ID + ":" + m.Topic + " "
	}
	if got != "m3:ta m4:ta m5:tb m6: m7: " {
		t.Fatalf("board messages: %s", got)
	}
	if ts, _ := s.MessageTopics("g1", []string{"m2", "m5", "m6"}); len(ts) != 2 || ts["m2"] != "ta" || ts["m5"] != "tb" {
		t.Fatalf("topics %v", ts)
	}

	// Topics and log entries round-trip; an entry is stored once.
	tp := model.ChatTopic{ChatID: "g1", ID: "ta", Title: "A", TitleAt: 5, TitleBy: "p1", Color: "red", ColorAt: 6, ColorBy: "p2",
		Archived: true, ArchivedAt: 7, ArchivedBy: "p3", CreatedBy: "p1", CreatedAt: 4}
	if err := s.PutChatTopic(tp); err != nil {
		t.Fatal(err)
	}
	back, err := s.GetChatTopic("g1", "ta")
	tp.Kind, tp.UpdatedAt = "topic", 7
	if err != nil || back != tp {
		t.Fatalf("topic: %+v %v", back, err)
	}
	e := model.ChatTopicEntry{ID: "0123456789abcdef", ChatID: "g1", TopicID: "ta", Kind: "decision", Text: "yes", By: "me", ByName: "Sam", At: 3}
	if added, _ := s.AddChatTopicEntry(e); !added {
		t.Fatal("not added")
	}
	if added, _ := s.AddChatTopicEntry(e); added {
		t.Fatal("added twice")
	}
	if n, _ := s.CountChatTopicEntries("g1"); n != 1 {
		t.Fatal(n)
	}
	// The personal order.
	if o, _ := s.ChatBoardOrder("g1"); o != nil {
		t.Fatalf("order before: %v", o)
	}
	s.SetChatBoardOrder("g1", []string{"ta", "general"}, 9)
	if o, _ := s.ChatBoardOrder("g1"); len(o) != 2 || o[0] != "ta" {
		t.Fatalf("order: %v", o)
	}
	// Deleting the chat removes its board.
	if err := s.DeleteChat("g1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountChatTopics("g1"); n != 0 {
		t.Fatal("topics left")
	}
	if ts, _ := s.MessageTopics("g1", []string{"m2", "later"}); len(ts) != 0 {
		t.Fatal("registers left")
	}
	if o, _ := s.ChatBoardOrder("g1"); o != nil {
		t.Fatal("order left")
	}
}

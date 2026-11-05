package store

import (
	"errors"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestPeerMessages(t *testing.T) {
	s := openTest(t)
	m := model.Message{ID: "loc1", From: "a1", To: "fleet:bob@hostc", Body: "hi", State: model.MsgQueued, CreatedAt: 1}
	p := PeerMessage{LocalID: "loc1", Peer: "hostc", WireID: "0123456789ab", Direction: PeerOut, SHA256: "x", NextAttemptAt: 100}
	if err := s.PutPeerMessage(m, p); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetMessage("loc1"); err != nil || got.To != m.To {
		t.Fatalf("message %+v %v", got, err)
	}
	// The same wire ID from the same peer again stores nothing.
	m2 := m
	m2.ID = "loc2"
	p2 := p
	p2.LocalID = "loc2"
	if err := s.PutPeerMessage(m2, p2); err == nil {
		t.Fatal("duplicate wire id stored")
	}
	if _, err := s.GetMessage("loc2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("half-stored duplicate: %v", err)
	}
	// Another peer may use the same wire ID.
	p2.Peer = "hosta"
	if err := s.PutPeerMessage(m2, p2); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PeerByWire("hostc", "0123456789ab"); err != nil || got.LocalID != "loc1" {
		t.Fatalf("by wire %+v %v", got, err)
	}
	if got, err := s.PeerByLocal("loc2"); err != nil || got.Peer != "hosta" {
		t.Fatalf("by local %+v %v", got, err)
	}
	if _, err := s.PeerByLocal("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	due, err := s.DuePeerMessages(100, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due %v %v", due, err)
	}
	if due, _ := s.DuePeerMessages(99, 10); len(due) != 0 {
		t.Fatalf("early: %v", due)
	}
	if err := s.SetPeerAttempt("loc1", 1, 0, "accepted"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPeerAttempt("loc2", 2, 500, "down"); err != nil {
		t.Fatal(err)
	}
	if at, err := s.NextPeerAttempt(); err != nil || at != 500 {
		t.Fatalf("next %d %v", at, err)
	}
	got, _ := s.PeerByLocal("loc2")
	if got.Attempts != 2 || got.LastError != "down" {
		t.Fatalf("attempt %+v", got)
	}
}

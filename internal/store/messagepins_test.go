package store

import (
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMessagePinsRetainedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := model.MessagePin{ID: "one", Source: "timeline:a:1", Label: "Agent a", Href: "#/agents/a", AgentID: "a", Text: "Remember this", MessageAt: 1, PinnedAt: 2}
	if err = s.SaveMessagePin(p); err != nil {
		t.Fatal(err)
	}
	// Repeated requests from two devices must not duplicate or reorder an active pin.
	p.ID = "duplicate"
	p.PinnedAt = 9
	if err = s.SaveMessagePin(p); err != nil {
		t.Fatal(err)
	}
	pins, err := s.MessagePins("a", false)
	if err != nil || len(pins) != 1 || pins[0].ID != "one" || pins[0].PinnedAt != 2 {
		t.Fatalf("idempotence: %+v %v", pins, err)
	}
	if err = s.SetMessagePinState("one", "done"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pins, err = s.MessagePins("", true)
	if err != nil || len(pins) != 1 || pins[0].State != "done" || pins[0].Text != "Remember this" {
		t.Fatalf("restart lost done pin: %+v %v", pins, err)
	}
	other, err := s.MessagePins("b", false)
	if err != nil || len(other) != 0 {
		t.Fatalf("other agent: %+v %v", other, err)
	}
	if err = s.SetMessagePinState("one", "unpinned"); err != nil {
		t.Fatal(err)
	}
	pins, _ = s.MessagePins("", true)
	if len(pins) != 0 {
		t.Fatal("unpin still visible")
	}
	if err = s.SaveMessagePin(p); err != nil {
		t.Fatal(err)
	}
	pins, _ = s.MessagePins("", true)
	if len(pins) != 1 || pins[0].State != "active" || pins[0].PinnedAt != 9 {
		t.Fatalf("repin: %+v", pins)
	}
	if err = s.SetMessagePinState("missing", "done"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestMessagePinsNewestFirst(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "pins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, p := range []model.MessagePin{{ID: "old", Source: "old", PinnedAt: 1}, {ID: "new", Source: "new", PinnedAt: 3}, {ID: "middle", Source: "middle", PinnedAt: 2}} {
		if err = s.SaveMessagePin(p); err != nil {
			t.Fatal(err)
		}
	}
	pins, err := s.MessagePins("", true)
	if err != nil || len(pins) != 3 || pins[0].ID != "new" || pins[1].ID != "middle" || pins[2].ID != "old" {
		t.Fatalf("order: %+v %v", pins, err)
	}
}

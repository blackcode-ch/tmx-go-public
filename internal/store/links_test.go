package store

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestThreadMatrixFieldsRoundTrip(t *testing.T) {
	s := openTest(t)
	in := model.Thread{ID: "t1", Title: "Buy computers", Kind: model.ThreadIntent, CreatedAt: 1, UpdatedAt: 2,
		ParentID: "p-x", Status: model.ThreadPaused, Lifecycle: model.LifecycleOngoing, Owner: "a-lead", Story: "As the owner, I want two laptops",
		IntentText: "go buy computers on the web", IntentSource: "voice 3 Nov 00:04", CanvasX: 120.5, CanvasY: -40}
	if err := s.PutThread(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThread("t1")
	if err != nil || got != in {
		t.Fatalf("got %+v %v", got, err)
	}
	// Threads from before the Matrix read back with empty Matrix fields.
	g, err := s.GetThread(model.GeneralThread)
	if err != nil || g.Status != "" || g.ParentID != "" || g.CanvasX != 0 {
		t.Fatalf("general %+v %v", g, err)
	}
}

func TestThreadLinks(t *testing.T) {
	s := openTest(t)
	l := model.ThreadLink{ID: "l1", ThreadID: "t1", Axis: model.AxisAgent, Ref: "a1", Role: "worker", Label: "w",
		By: "head", How: model.LinkAuto, At: 10}
	got, isNew, err := s.AddLink(l)
	if err != nil || !isNew || got != l {
		t.Fatalf("add %+v %v %v", got, isNew, err)
	}
	// The same open link again is the first one.
	again := l
	again.ID, again.At = "l2", 11
	got, isNew, err = s.AddLink(again)
	if err != nil || isNew || got.ID != "l1" {
		t.Fatalf("again %+v %v %v", got, isNew, err)
	}
	// Another role is another link.
	lead := l
	lead.ID, lead.Role = "l3", "lead"
	if _, isNew, err := s.AddLink(lead); err != nil || !isNew {
		t.Fatalf("lead %v %v", isNew, err)
	}
	if ls, _ := s.LinksTo(model.AxisAgent, "a1"); len(ls) != 2 {
		t.Fatalf("links to a1: %+v", ls)
	}

	if err := s.EndLink("l1", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.EndLink("l1", 30); err != nil { // already ended: no change
		t.Fatal(err)
	}
	if err := s.EndLink("nope", 30); err != ErrNotFound {
		t.Fatalf("end missing: %v", err)
	}
	open, _ := s.Links("t1", false)
	all, _ := s.Links("t1", true)
	if len(open) != 1 || open[0].ID != "l3" || len(all) != 2 {
		t.Fatalf("open %+v all %+v", open, all)
	}
	if e, _ := s.GetLink("l1"); e.EndedAt != 20 {
		t.Fatalf("ended at %d", e.EndedAt)
	}
	// Once ended, the same link can be made again (a new one; the old stays as history).
	again.ID = "l4"
	if _, isNew, err := s.AddLink(again); err != nil || !isNew {
		t.Fatalf("relink %v %v", isNew, err)
	}

	if n, err := s.EndLinksTo(model.AxisAgent, "a1", 40); err != nil || n != 2 {
		t.Fatalf("end all %d %v", n, err)
	}
	if ls, _ := s.LinksTo(model.AxisAgent, "a1"); len(ls) != 0 {
		t.Fatalf("still open: %+v", ls)
	}
}

func TestEntriesKeepFilingOrder(t *testing.T) {
	s := openTest(t)
	want := []string{"zz", "aa", "mm", "bb", "yy"} // IDs that sort differently from the filing order
	for _, id := range want {
		if _, err := s.AddEntry(model.ThreadEntry{ID: id, ThreadID: "t1", Kind: "info", Text: id, By: "owner", At: 5}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Entries("t1")
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range got {
		if e.ID != want[i] {
			t.Fatalf("entry %d is %s, want %s", i, e.ID, want[i])
		}
	}
}

func TestDeleteThreadEndsLinks(t *testing.T) {
	s := openTest(t)
	if err := s.PutThread(model.Thread{ID: "t1", Title: "Topic", Kind: model.ThreadTopic, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddLink(model.ThreadLink{ID: "l1", ThreadID: "t1", Axis: model.AxisDoc, Ref: "D:/x.html", By: "owner",
		How: model.LinkHand, At: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteThread("t1", 50); err != nil {
		t.Fatal(err)
	}
	l, err := s.GetLink("l1")
	if err != nil || l.EndedAt != 50 {
		t.Fatalf("link after delete %+v %v", l, err)
	}
}

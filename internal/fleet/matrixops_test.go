package fleet

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func eventKinds(t *testing.T, f *Fleet, thread string) []string {
	t.Helper()
	v, err := f.View(OwnerCaller, thread, false, true)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range v.Events {
		out = append(out, e.Kind)
	}
	return out
}

// M1b, stories S2 and S11: a request becomes a thread with the owner's words, an owner and a
// lifecycle; every state change is an event.
func TestMatrixNewThreadAndState(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	lead := Caller{AgentID: p.LeadID}

	th, err := f.NewThread(OwnerCaller, NewThreadRequest{
		Title:        "Buy computers",
		IntentText:   "go buy computers on the web with the phone number he bought and a browser",
		IntentSource: "voice, 3 Nov 00:04",
		Story:        "As the owner, I want two laptops bought online",
		Parent:       "Shop",
		Owner:        p.LeadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if th.Kind != model.ThreadIntent || th.ParentID != model.ProjectThread(p.ID) || th.ProjectID != p.ID || th.Owner != p.LeadID ||
		th.Status != model.ThreadActive {
		t.Fatalf("thread %+v", th)
	}
	if !hasAgentLink(openLinks(t, f, th.ID), p.LeadID, "lead") {
		t.Fatalf("its owner is not on it: %+v", openLinks(t, f, th.ID))
	}
	if k := eventKinds(t, f, th.ID); len(k) != 1 || k[0] != model.EventStart {
		t.Fatalf("events %v", k)
	}
	if _, err := f.NewThread(OwnerCaller, NewThreadRequest{Title: "buy  COMPUTERS"}); KindOf(err) != KindConflict {
		t.Fatalf("same title: %v", err)
	}
	if _, err := f.NewThread(OwnerCaller, NewThreadRequest{Title: "x", Lifecycle: "forever"}); KindOf(err) != KindInvalid {
		t.Fatalf("bad lifecycle: %v", err)
	}
	if _, err := f.NewThread(OwnerCaller, NewThreadRequest{Title: "x", IntentText: "token AbCdEf0123456789AbCdEf0123456789xyz"}); KindOf(err) != KindInvalid {
		t.Fatalf("a secret in the owner's words: %v", err)
	}

	// A lead opens threads under its own project only; a worker not at all.
	sub, err := f.NewThread(lead, NewThreadRequest{Title: "Compare offers", Lifecycle: "task"})
	if err != nil || sub.ProjectID != p.ID || sub.Owner != p.LeadID {
		t.Fatalf("lead's thread %+v %v", sub, err)
	}
	other, err := f.CreateThread("Elsewhere", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewThread(lead, NewThreadRequest{Title: "Sneaky", Parent: other.ID}); KindOf(err) != KindForbidden {
		t.Fatalf("lead outside its project: %v", err)
	}
	outsider, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "outsider"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewThread(lead, NewThreadRequest{Title: "Borrowed", Owner: outsider.ID}); KindOf(err) != KindForbidden {
		t.Fatalf("lead naming an owner outside its project: %v", err)
	}
	if _, err := f.NewThread(OwnerCaller, NewThreadRequest{Title: "token AbCdEf0123456789AbCdEf0123456789xyz"}); KindOf(err) != KindInvalid {
		t.Fatalf("a secret in the title: %v", err)
	}
	w, err := f.LaunchAgent(lead, LaunchRequest{Name: "shop-worker", ThreadID: th.ID})
	if err != nil {
		t.Fatal(err)
	}
	worker := Caller{AgentID: w.ID}
	if _, err := f.NewThread(worker, NewThreadRequest{Title: "Mine"}); KindOf(err) != KindForbidden {
		t.Fatalf("worker opening a thread: %v", err)
	}

	// The lifecycle: the worker can't move it; its owner (the lead) can, and each move is an event.
	if _, err := f.SetState(worker, th.ID, StateRequest{Status: "done"}); KindOf(err) != KindForbidden {
		t.Fatalf("worker closing the thread: %v", err)
	}
	if _, err := f.SetState(lead, th.ID, StateRequest{Status: "nearly"}); KindOf(err) != KindInvalid {
		t.Fatalf("bad status: %v", err)
	}
	got, err := f.SetState(lead, th.ID, StateRequest{Status: "paused", Why: "waiting for the card limit"})
	if err != nil || got.Status != model.ThreadPaused {
		t.Fatalf("pause %+v %v", got, err)
	}
	if _, err := f.SetState(lead, th.ID, StateRequest{Status: "paused"}); err != nil { // no change: no event
		t.Fatal(err)
	}
	if got, err := f.SetState(OwnerCaller, th.ID, StateRequest{Status: "done", Lifecycle: "ongoing"}); err != nil ||
		got.Status != model.ThreadDone || got.Lifecycle != model.LifecycleOngoing {
		t.Fatalf("done %+v %v", got, err)
	}
	k := eventKinds(t, f, th.ID)
	if strings.Join(k, ",") != "start,pause,done" { // the order they happened
		t.Fatalf("lifecycle events %v", k)
	}
	if _, err := f.SetState(OwnerCaller, model.GeneralThread, StateRequest{Status: "done"}); KindOf(err) != KindInvalid {
		t.Fatalf("General done: %v", err)
	}

	// Mine: the worker sees its project's threads and the one it is on, not Elsewhere.
	cards, err := f.Mine(worker)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]ThreadCard{}
	for _, c := range cards {
		ids[c.Thread.ID] = c
	}
	if _, ok := ids[th.ID]; !ok {
		t.Fatalf("worker's threads %+v", cards)
	}
	if _, ok := ids[other.ID]; ok {
		t.Fatal("the worker sees a thread it isn't on")
	}
	if ids[th.ID].Counts[model.AxisAgent] != 2 {
		t.Fatalf("counts %+v", ids[th.ID].Counts)
	}

	// Pivot by agent: the owner sees every thread; a stranger sees only its assigned
	// thread and its automatically assigned project thread.
	rows, err := f.Pivot(OwnerCaller, "agent")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Ref == w.ID {
			found = len(r.Threads) >= 1
		}
	}
	if !found {
		t.Fatalf("pivot %+v", rows)
	}
	stranger, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "stranger", ThreadID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	srows, err := f.Pivot(Caller{AgentID: stranger.ID}, "agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range srows {
		for _, id := range r.Threads {
			if id != other.ID && id != model.ProjectThread(stranger.ProjectID) {
				t.Fatalf("a stranger sees thread %s", id)
			}
		}
	}
	if _, err := f.Pivot(OwnerCaller, "bogus"); KindOf(err) != KindInvalid {
		t.Fatalf("bad axis: %v", err)
	}
}

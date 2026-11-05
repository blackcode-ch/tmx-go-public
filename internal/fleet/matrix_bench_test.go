package fleet

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// seedMatrix fills a store with n intent threads, each with 3 links and 4 events.
func seedMatrix(tb testing.TB, e *env, n int) {
	tb.Helper()
	now := model.Now()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("i-%05d", i)
		if err := e.st.PutThread(model.Thread{ID: id, Title: fmt.Sprintf("Request %d", i), Kind: model.ThreadIntent,
			Status: model.ThreadActive, CreatedAt: now, UpdatedAt: now}); err != nil {
			tb.Fatal(err)
		}
		for j, axis := range []string{model.AxisAgent, model.AxisDoc, model.AxisShell} {
			if _, _, err := e.st.AddLink(model.ThreadLink{ID: fmt.Sprintf("%s-l%d", id, j), ThreadID: id, Axis: axis,
				Ref: fmt.Sprintf("ref-%d-%d", i%50, j), By: model.Owner, How: model.LinkHand, At: now}); err != nil {
				tb.Fatal(err)
			}
		}
		for k := 0; k < 4; k++ {
			if _, err := e.st.AddEntry(model.ThreadEntry{ID: fmt.Sprintf("%s-e%d", id, k), ThreadID: id, Kind: model.EventResult,
				Text: "done", By: model.Owner, At: now + int64(k)}); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

// M3's load budget: the Matrix's first screen (every thread with its counts) for 5,000
// threads is one request; it must stay well under a second and its JSON small enough for a
// phone. It is a test (skipped with -short) that times f.Mine with testing.Benchmark.
func TestMatrixFirstScreenBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 5,000 threads")
	}
	e := newEnv(t)
	f := e.started()
	seedMatrix(t, e, 5000)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := f.Mine(OwnerCaller); err != nil {
				b.Fatal(err)
			}
		}
	})
	cards, err := f.Mine(OwnerCaller)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(cards)
	t.Logf("Mine over %d threads: %v per call, %d kB JSON", len(cards), time.Duration(res.NsPerOp()), len(body)/1024)
	if len(cards) < 5000 {
		t.Fatalf("only %d cards", len(cards))
	}
	if per := time.Duration(res.NsPerOp()); !raceDetector && per.Milliseconds() > 500 {
		t.Fatalf("the first screen takes %v (budget 500 ms on CI)", per)
	}
}

// Mine inlines onThreadLocked for speed (one query for the caller's links instead of one per
// thread); go-reviewer-o on #136: the two must never drift apart. Over a mixed fixture, every
// caller's Mine is exactly the live threads onThreadLocked lets it see.
func TestMineAgreesWithOnThread(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Books"})
	if err != nil {
		t.Fatal(err)
	}
	topic, _ := f.CreateThread("Loose topic", "")
	if _, err := f.NewThread(OwnerCaller, NewThreadRequest{Title: "Under Shop", Parent: "Shop"}); err != nil {
		t.Fatal(err)
	}
	gone, _ := f.CreateThread("Archived topic", "")
	archived := true
	if _, err := f.UpdateThread(OwnerCaller, gone.ID, ThreadPatch{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	chat := model.Thread{ID: model.ChatThread("ch9"), Title: "With someone", Kind: model.ThreadChat, ChatID: "ch9", CreatedAt: 1, UpdatedAt: 1}
	if err := e.st.PutThread(chat); err != nil {
		t.Fatal(err)
	}
	loose, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "loose", ThreadID: topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := f.LaunchAgent(Caller{AgentID: p.LeadID}, LaunchRequest{Name: "shop-w"})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{p.LeadID, q.LeadID, loose.ID, worker.ID} {
		cards, err := f.Mine(Caller{AgentID: id})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, c := range cards {
			got[c.Thread.ID] = true
		}
		threads, err := e.st.ListThreads()
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		r := f.agents[id]
		for _, th := range threads {
			want := !th.Archived && f.onThreadLocked(r, th)
			if got[th.ID] != want {
				f.mu.Unlock()
				t.Fatalf("%s and thread %s (%s): Mine says %v, onThreadLocked %v", r.a.Name, th.ID, th.Kind, got[th.ID], want)
			}
		}
		f.mu.Unlock()
	}
}

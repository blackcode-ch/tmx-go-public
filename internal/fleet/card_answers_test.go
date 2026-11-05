package fleet

import (
	"strings"
	"sync"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func TestCardLateWordsAreCorrespondenceAndSurviveRestart(t *testing.T) {
	e, f, _ := sectionFixture(t)
	a, err := f.AskOwner(Caller{AgentID: "lead"}, AskRequest{Question: "Which plan?", Options: []string{"First", "Second"}})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := f.Decide(OwnerCaller, a.ID, DecideRequest{Decision: "answer", Answer: "First", Version: a.Version, AnswerID: "choice"})
	if err != nil {
		t.Fatal(err)
	}
	late := DecideRequest{Decision: "answer", Answer: "Words recorded before the card closed\nKeep both lines.", Version: a.Version, AnswerID: "dictated", FreeText: true}
	for i := 0; i < 2; i++ {
		got, err := e.fleet().Decide(OwnerCaller, a.ID, late)
		if err != nil || got.Answer != closed.Answer || got.AnswerID != closed.AnswerID || got.State != closed.State {
			t.Fatalf("late correspondence changed decision: %+v %v", got, err)
		}
	}
	mail, err := e.st.ListMessages(store.MessageFilter{To: "lead"})
	if err != nil || len(mail) != 2 {
		t.Fatalf("mail count: %d %v", len(mail), err)
	}
	found := false
	for _, m := range mail {
		if strings.Contains(m.Body, late.Answer) && strings.Contains(m.Body, "not a new decision") {
			found = true
		}
	}
	if !found {
		t.Fatal("late words not durably queued as correspondence")
	}
	late.FreeText, late.AnswerID = false, "stale-choice"
	if _, err := f.Decide(OwnerCaller, a.ID, late); KindOf(err) != KindConflict {
		t.Fatalf("closed choice accepted: %v", err)
	}
	late.FreeText = true
	if _, err := f.Decide(Caller{AgentID: "lead"}, a.ID, late); KindOf(err) != KindForbidden {
		t.Fatalf("agent correspondence accepted: %v", err)
	}
}

func TestCardChangedFreeTextRequiresReview(t *testing.T) {
	e, f, _ := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	a, _ := f.AskOwner(c, AskRequest{Question: "Original question"})
	draft := DecideRequest{Decision: "answer", Answer: "My typed words", Version: a.Version, AnswerID: "original-draft", FreeText: true}
	changed, err := f.AskOwner(c, AskRequest{ID: a.ID, Question: "A different question"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Decide(OwnerCaller, a.ID, draft); KindOf(err) != KindConflict {
		t.Fatalf("stale text accepted: %v", err)
	}
	mail, _ := e.st.ListMessages(store.MessageFilter{To: "lead"})
	if len(mail) != 0 {
		t.Fatal("stale draft sent")
	}
	draft.Version, draft.AnswerID = changed.Version, "reviewed-draft"
	if _, err := f.Decide(OwnerCaller, a.ID, draft); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fleet().Decide(OwnerCaller, a.ID, draft); err != nil {
		t.Fatal(err)
	}
	mail, _ = e.st.ListMessages(store.MessageFilter{To: "lead"})
	if len(mail) != 1 || !strings.Contains(mail[0].Body, draft.Answer) {
		t.Fatalf("reviewed answer lost or duplicated: %+v", mail)
	}
}

func TestCardAnswerStaleDuplicateAndReconnect(t *testing.T) {
	e, f, _ := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	a, err := f.AskOwner(c, AskRequest{Question: "Choose a database"})
	if err != nil {
		t.Fatal(err)
	}
	stale := DecideRequest{Decision: "answer", Answer: "sqlite", Version: a.Version, AnswerID: "request-one"}
	a, err = f.AskOwner(c, AskRequest{ID: a.ID, Question: "Choose a database", Context: "Now requires replication"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Decide(OwnerCaller, a.ID, stale); KindOf(err) != KindConflict {
		t.Fatalf("stale accepted: %v", err)
	}
	req := stale
	req.Version = a.Version
	if _, err := f.Decide(c, a.ID, req); KindOf(err) != KindForbidden {
		t.Fatalf("agent accepted: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.Decide(OwnerCaller, a.ID, req)
			if err != nil || result.AnswerID != req.AnswerID {
				t.Errorf("retry: %+v %v", result, err)
			}
		}()
	}
	wg.Wait()
	messages, err := e.st.ListMessages(store.MessageFilter{To: "lead"})
	if err != nil || len(messages) != 1 || messages[0].State != model.MsgQueued || messages[0].Receipt != "queued" {
		t.Fatalf("mail: %+v %v", messages, err)
	}
	restarted := e.fleet()
	result, err := restarted.Decide(OwnerCaller, a.ID, req)
	if err != nil || result.AnswerID != req.AnswerID {
		t.Fatalf("restart retry: %+v %v", result, err)
	}
	req.Answer = "postgres"
	if _, err := restarted.Decide(OwnerCaller, a.ID, req); KindOf(err) != KindConflict {
		t.Fatalf("identity rebound: %v", err)
	}
	messages, _ = e.st.ListMessages(store.MessageFilter{To: "lead"})
	if len(messages) != 1 {
		t.Fatalf("duplicate mail: %d", len(messages))
	}
}

func TestCardDeferralAndReask(t *testing.T) {
	e, f, _ := sectionFixture(t)
	a, _ := f.AskOwner(Caller{AgentID: "lead"}, AskRequest{Question: "Proceed?"})
	d := DecideRequest{Decision: "defer", Version: a.Version, AnswerID: "later"}
	parked, err := f.Decide(OwnerCaller, a.ID, d)
	if err != nil || !parked.Deferred || parked.Version != 2 {
		t.Fatalf("defer: %+v %v", parked, err)
	}
	b, _ := f.Board()
	if len(b.YourTurn) != 0 {
		t.Fatal("parked card still on Your turn")
	}
	if _, err := f.Decide(OwnerCaller, a.ID, DecideRequest{Decision: "answer", Answer: "yes", Version: 1}); KindOf(err) != KindConflict {
		t.Fatalf("pre-deferral answer: %v", err)
	}
	opened, err := f.Decide(OwnerCaller, a.ID, DecideRequest{Decision: "reask", Version: 2, AnswerID: "open"})
	if err != nil || opened.Deferred || opened.Version != 3 {
		t.Fatalf("reask: %+v %v", opened, err)
	}
	lateRetry, err := f.Decide(OwnerCaller, a.ID, d)
	if err != nil || lateRetry.Deferred || lateRetry.Version != 3 {
		t.Fatalf("old defer receipt resurrected old state: %+v %v", lateRetry, err)
	}
	answer, err := f.Decide(OwnerCaller, a.ID, DecideRequest{Decision: "answer", Answer: "yes", Version: 3, AnswerID: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	req := DecideRequest{Decision: "reask", Version: answer.Version, AnswerID: "again"}
	fresh, err := f.Decide(OwnerCaller, a.ID, req)
	if err != nil || fresh.ID == a.ID || fresh.Version != 1 || fresh.AnswerID != "" {
		t.Fatalf("fresh reask: %+v %v", fresh, err)
	}
	duplicate, err := f.Decide(OwnerCaller, a.ID, req)
	if err != nil || duplicate.ID != fresh.ID {
		t.Fatalf("reask retry: %+v %v", duplicate, err)
	}
	messages, _ := e.st.ListMessages(store.MessageFilter{To: "lead"})
	if len(messages) != 2 {
		t.Fatalf("want answer and reask notice, got %d", len(messages))
	}
}

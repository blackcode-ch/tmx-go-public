package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestOwnerCardEffectsAndVersions(t *testing.T) {
	e, f, _ := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	req := AskRequest{Type: "human_action", Action: &model.CardAction{Goal: "Send the update", Steps: []string{"Read it", "Email it"}, Success: "Delivered", Effects: model.CardEffectNone}}
	ap, err := f.AskOwner(c, req)
	if err != nil || ap.Version != 1 || ap.Card.Action.Effects != model.CardEffectOutward {
		t.Fatalf("create: %+v %v", ap, err)
	}
	req.ID = ap.ID
	ap, err = f.AskOwner(c, req)
	if err != nil || ap.Version != 1 {
		t.Fatalf("unchanged: %+v %v", ap, err)
	}
	req.Action.Steps = []string{"Pay the invoice", "Email it"}
	ap, err = f.AskOwner(c, req)
	if err != nil || ap.Version != 2 || ap.Card.Action.Effects != model.CardEffectMoney {
		t.Fatalf("changed: %+v %v", ap, err)
	}
	stored, err := e.st.GetApproval(ap.ID)
	if err != nil || stored.Version != 2 || stored.Card.Action.Effects != model.CardEffectMoney {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	b, err := f.Board()
	if err != nil || len(b.YourTurn) != 1 || b.YourTurn[0].Version != 2 {
		t.Fatalf("board: %+v %v", b, err)
	}
	if _, err := f.AskOwner(Caller{AgentID: "head"}, req); KindOf(err) != KindForbidden {
		t.Fatalf("other asker: %v", err)
	}
	if _, err := f.AskOwner(OwnerCaller, req); KindOf(err) != KindForbidden {
		t.Fatalf("owner posting: %v", err)
	}
}

func TestOwnerCardValidation(t *testing.T) {
	_, f, _ := sectionFixture(t)
	for _, req := range []AskRequest{
		{Question: "Proceed?", Type: "unknown"},
		{Question: "Proceed?", Options: []string{"yes"}, Recommendation: "no"},
		{Type: "human_action", Action: &model.CardAction{Goal: "Do it"}},
		{Question: "Proceed?", Type: "decision", Action: &model.CardAction{}},
	} {
		if _, err := f.AskOwner(Caller{AgentID: "lead"}, req); KindOf(err) != KindInvalid {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	for _, tc := range []struct {
		goal, category string
		want           model.CardEffect
	}{
		{"Read the report", "", model.CardEffectNone},
		{"Log in", "", model.CardEffectAccount},
		{"Read the report", "money", model.CardEffectMoney},
		{"Read the report", "security", model.CardEffectAccount},
		{"Publish the report", "", model.CardEffectOutward},
	} {
		if got := cardEffects(model.CardAction{Goal: tc.goal}, tc.category); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

func TestOwnerCardRejectsDisplayedOldVersion(t *testing.T) {
	e, f, _ := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	old, err := f.AskOwner(c, AskRequest{Question: "Read the report?"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.AskOwner(c, AskRequest{ID: old.ID, Question: "Publish the report?"})
	if err != nil {
		t.Fatal(err)
	}
	d := DecideRequest{Decision: "answer", Answer: "yes", Version: old.Version}
	if _, err := f.Decide(OwnerCaller, old.ID, d); KindOf(err) != KindConflict {
		t.Fatalf("stale answer accepted: %v", err)
	}
	// Also exercise an edit between Decide's first read and answerQuestion's lock.
	if _, err := f.answerQuestion(old, d); KindOf(err) != KindConflict {
		t.Fatalf("stale close accepted: %v", err)
	}
	got, _ := e.st.GetApproval(old.ID)
	if got.State != model.ApprovalPending || got.Version != changed.Version {
		t.Fatalf("stale answer mutated card: %+v", got)
	}
}

func TestOwnerCardStripsInvisibleControls(t *testing.T) {
	req := AskRequest{Question: "Read\u202e?", Action: &model.CardAction{Goal: "E\u200bmail report", Steps: []string{"Se\u2060nd it"}, Success: "Done"}}
	c, err := prepareOwnerCard(&req)
	if err != nil || req.Question != "Read?" || c.Action.Goal != "Email report" || c.Action.Steps[0] != "Send it" || c.Action.Effects != model.CardEffectOutward {
		t.Fatalf("%+v %+v %v", req, c, err)
	}
}

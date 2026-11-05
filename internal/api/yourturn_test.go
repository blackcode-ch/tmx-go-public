package api_test

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// A real HTTP server and fresh database; never uses a live daemon or owner state.
func TestYourTurnThrowawayCardJourney(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	asker := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	var card model.Approval
	if s := asker.do("POST", "/api/approvals", map[string]any{"question": "Throwaway: choose a plan"}, &card); s != 201 {
		t.Fatalf("create: %d", s)
	}
	var listed []model.Approval
	if s := owner.do("GET", "/api/approvals", nil, &listed); s != 200 || len(listed) != 1 || listed[0].ID != card.ID {
		t.Fatalf("list: %d %+v", s, listed)
	}
	t.Log("throwaway card created and listed")
	words := "Keep my typed answer\nand dictated second line"
	draft := map[string]any{"decision": "answer", "answer": words, "free_text": true, "version": card.Version, "answer_id": "draft"}
	if s := asker.do("POST", "/api/approvals", map[string]any{"id": card.ID, "question": "Throwaway: changed plan"}, &card); s != 201 {
		t.Fatalf("change: %d", s)
	}
	if s := owner.do("POST", "/api/approvals/"+card.ID, draft, nil); s != 409 {
		t.Fatalf("stale answer: %d", s)
	}
	draft["version"], draft["answer_id"] = card.Version, "reviewed"
	for range 2 {
		var result model.Approval
		if s := owner.do("POST", "/api/approvals/"+card.ID, draft, &result); s != 200 || result.Answer != words {
			t.Fatalf("answer/retry: %d %+v", s, result)
		}
	}
	t.Log("changed card rejected stale decision; reviewed words answered and retried intact")
	draft["answer"], draft["answer_id"] = "Late dictation after closure", "late"
	for range 2 {
		var result model.Approval
		if s := owner.do("POST", "/api/approvals/"+card.ID, draft, &result); s != 200 || result.Answer != words {
			t.Fatalf("late correspondence: %d %+v", s, result)
		}
	}
	if s := asker.do("POST", "/api/approvals/"+card.ID, draft, nil); s != 403 {
		t.Fatalf("agent answer: %d", s)
	}
	var mail []model.Message
	if s := asker.do("GET", "/api/messages", nil, &mail); s != 200 {
		t.Fatalf("mail: %d", s)
	}
	answers, late := 0, 0
	for _, m := range mail {
		if strings.Contains(m.Body, words) {
			answers++
		}
		if strings.Contains(m.Body, "Late dictation after closure") && strings.Contains(m.Body, "not a new decision") {
			late++
		}
	}
	if answers != 1 || late != 1 {
		t.Fatalf("lost or duplicate words: answers=%d late=%d", answers, late)
	}
	t.Log("late dictated words delivered once as correspondence; original decision intact; agent forbidden")
}

// Project-state uses the helper to create cards; the owner sees and answers them
// through the same HTTP routes as ask_owner, without a second decision mechanism.
func TestYourTurnProjectCardJourney(t *testing.T) {
	d := apitest.New(t)
	p, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "Throwaway questions"})
	if err != nil {
		t.Fatal(err)
	}
	asker := fleet.Caller{AgentID: p.LeadID}
	req := fleet.ProjectCardRequest{AskRequest: fleet.AskRequest{Question: "Which project name?", Category: "naming"}, Urgent: true}
	card, err := d.Fleet.AskProjectOwner(asker, p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var cards []model.Approval
	if status := owner.do("GET", "/api/approvals", nil, &cards); status != 200 || len(cards) != 1 || cards[0].Card.ProjectID != p.ID {
		t.Fatalf("project card list: %d %+v", status, cards)
	}
	req.ID = card.ID
	req.Question = "Revised project question?"
	changed, err := d.Fleet.AskProjectOwner(asker, p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	words := map[string]any{"decision": "answer", "answer": "Keep these words", "free_text": true, "version": card.Version, "answer_id": "project-words"}
	if status := owner.do("POST", "/api/approvals/"+card.ID, words, nil); status != 409 {
		t.Fatalf("stale project answer: %d", status)
	}
	words["version"] = changed.Version
	var result model.Approval
	if status := owner.do("POST", "/api/approvals/"+card.ID, words, &result); status != 200 || result.Answer != "Keep these words" {
		t.Fatalf("reviewed project answer: %d %+v", status, result)
	}
	t.Log("project card listed; stale answer refused; reviewed words accepted through existing owner route")
}

func TestYourTurnSupervisorConfigurationVisible(t *testing.T) {
	d := apitest.New(t)
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	var status struct {
		Supervisor string `json:"supervisor"`
	}
	if code := owner.do("GET", "/api/peers", nil, &status); code != 200 || status.Supervisor != "architect" {
		t.Fatalf("supervisor: %d %+v", code, status)
	}
}

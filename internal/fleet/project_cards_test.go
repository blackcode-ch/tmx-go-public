package fleet

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func TestProjectCardsShareYourTurnAndPreserveLateWords(t *testing.T) {
	e, f, p := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	req := ProjectCardRequest{AskRequest: AskRequest{Question: "Choose the project name", Options: []string{"One", "Two"}, Category: "naming"}, Priority: 2, Urgent: true, Batchable: true, OwnerOnly: true, CloseWhen: "state:report:DONE"}
	a, err := f.AskProjectOwner(c, p.Slug, req)
	if err != nil || a.Card.ProjectID != p.ID || !a.Card.Urgent || a.Card.Priority != 2 || !a.Card.OwnerOnly || a.Card.Category != "naming" {
		t.Fatalf("create: %+v %v", a, err)
	}
	other, err := f.AskProjectOwner(c, p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "Second independent question"}})
	if err != nil || other.ID == a.ID {
		t.Fatalf("second card: %+v %v", other, err)
	}
	listed, err := f.Approvals(model.ApprovalPending)
	if err != nil || len(listed) != 2 {
		t.Fatalf("Your turn: %+v %v", listed, err)
	}
	req.ID = a.ID
	unchanged, err := f.AskProjectOwner(c, p.ID, req)
	if err != nil || unchanged.Version != a.Version {
		t.Fatalf("unchanged version: %+v %v", unchanged, err)
	}
	req.Question = "Revised project name?"
	req.OwnerOnly = false
	changed, err := f.AskProjectOwner(c, p.ID, req)
	if err != nil || changed.Version != a.Version+1 || changed.Card.OwnerOnly {
		t.Fatalf("edit: %+v %v", changed, err)
	}
	answer := DecideRequest{Decision: "answer", Version: a.Version, AnswerID: "words", Answer: "Typed words\nDictated words", FreeText: true}
	if _, err = f.Decide(OwnerCaller, a.ID, answer); KindOf(err) != KindConflict {
		t.Fatalf("stale accepted: %v", err)
	}
	answer.Version = changed.Version
	for range 2 {
		if _, err = f.Decide(OwnerCaller, a.ID, answer); err != nil {
			t.Fatal(err)
		}
	}
	closed, err := f.CloseProjectOwnerCard(c, p.ID, other.ID, "No longer needed")
	if err != nil || closed.State != model.ApprovalExpired || closed.Answer != "" || closed.Card.CloseReason != "No longer needed" {
		t.Fatalf("close: %+v %v", closed, err)
	}
	again, err := f.CloseProjectOwnerCard(c, p.ID, other.ID, "No longer needed")
	if err != nil || again.Version != closed.Version {
		t.Fatalf("repeat close: %+v %v", again, err)
	}
	late := DecideRequest{Decision: "answer", Version: other.Version, AnswerID: "late", Answer: "My late dictated words", FreeText: true}
	restarted := e.fleet()
	for range 2 {
		result, err := restarted.Decide(OwnerCaller, other.ID, late)
		if err != nil || result.State != model.ApprovalExpired || result.Answer != "" {
			t.Fatalf("late: %+v %v", result, err)
		}
	}
	mail, err := e.st.ListMessages(store.MessageFilter{To: "lead"})
	if err != nil || len(mail) != 2 {
		t.Fatalf("mail: %+v %v", mail, err)
	}
	found := false
	for _, m := range mail {
		if strings.Contains(m.Body, late.Answer) && strings.Contains(m.Body, "not a new decision") {
			found = true
		}
	}
	if !found {
		t.Fatal("late correspondence missing")
	}
	restored, err := restarted.ProjectOwnerCards(OwnerCaller, p.ID)
	if err != nil || len(restored) != 2 {
		t.Fatalf("restart list: %+v %v", restored, err)
	}
}

func TestProjectCardsKeepMembershipAndOwnerDecisionBoundaries(t *testing.T) {
	_, f, p := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	a, err := f.AskProjectOwner(c, p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "Private project question"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, outsider := range []Caller{{AgentID: "head"}, {Companion: true}, {AgentID: "missing"}, PeerCaller("peer"), {Owner: true, Peer: "peer"}, {AgentID: "lead", Peer: "peer"}} {
		if _, err = f.ProjectOwnerCards(outsider, p.ID); KindOf(err) != KindForbidden {
			t.Fatalf("outsider read: %v", err)
		}
		if _, err = f.CloseProjectOwnerCard(outsider, p.ID, a.ID, "close"); KindOf(err) != KindForbidden {
			t.Fatalf("outsider close: %v", err)
		}
		if _, err = f.AskProjectOwner(outsider, p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "bad"}}); KindOf(err) != KindForbidden {
			t.Fatalf("outsider create: %v", err)
		}
	}
	f.agents["member"] = &agentRec{a: model.Agent{ID: "member", ProjectID: p.ID}}
	if _, err = f.CloseProjectOwnerCard(Caller{AgentID: "member"}, p.ID, a.ID, "close"); KindOf(err) != KindForbidden {
		t.Fatalf("other member closed: %v", err)
	}
	if _, err = f.AskOwner(c, AskRequest{ID: a.ID, Question: "strip binding"}); KindOf(err) != KindForbidden {
		t.Fatalf("generic edit escaped project: %v", err)
	}
	ordinary, err := f.AskOwner(c, AskRequest{Question: "Ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.AskProjectOwner(c, p.ID, ProjectCardRequest{AskRequest: AskRequest{ID: ordinary.ID, Question: "rebind"}}); KindOf(err) != KindForbidden {
		t.Fatalf("ordinary rebound: %v", err)
	}
	if _, err = f.Decide(c, a.ID, DecideRequest{Version: a.Version, Decision: "answer", Answer: "agent decision"}); KindOf(err) != KindForbidden {
		t.Fatalf("agent answered: %v", err)
	}
	if _, err = f.CloseProjectOwnerCard(c, p.ID, a.ID, "  "); KindOf(err) != KindInvalid {
		t.Fatalf("empty reason: %v", err)
	}
	if _, err = f.CloseProjectOwnerCard(OwnerCaller, p.ID, a.ID, "Owner retired it"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCardsParkingAndValidation(t *testing.T) {
	_, f, p := sectionFixture(t)
	c := Caller{AgentID: "lead"}
	later := true
	req := ProjectCardRequest{AskRequest: AskRequest{Question: "Park this"}, Later: &later}
	a, err := f.AskProjectOwner(c, p.ID, req)
	if err != nil || !a.Deferred {
		t.Fatalf("park: %+v %v", a, err)
	}
	req.ID = a.ID
	req.Later = nil
	req.Question = "Changed parked question"
	changed, err := f.AskProjectOwner(c, p.ID, req)
	if err != nil || !changed.Deferred || changed.Version != a.Version+1 {
		t.Fatalf("preserve park: %+v %v", changed, err)
	}
	later = false
	req.Later = &later
	unparked, err := f.AskProjectOwner(c, p.ID, req)
	if err != nil || unparked.Deferred || unparked.Version != changed.Version+1 {
		t.Fatalf("unpark: %+v %v", unparked, err)
	}
	for _, bad := range []ProjectCardRequest{{AskRequest: AskRequest{Question: "Bad"}, Priority: -1}, {AskRequest: AskRequest{Question: "Bad"}, CloseWhen: "exec:do something"}, {AskRequest: AskRequest{Question: " "}}} {
		if _, err = f.AskProjectOwner(c, p.ID, bad); KindOf(err) != KindInvalid {
			t.Fatalf("invalid accepted: %+v %v", bad, err)
		}
	}
}

func TestProjectCardAnswerFallsBackOnlyWhenAskerStops(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       model.AgentState
		shelved     int64
		leadRunning bool
		want        string
	}{
		{"running asker", model.StateIdle, 0, true, "asker"},
		{"stopped asker", model.StateStopped, 0, true, "lead"},
		{"exited asker", model.StateExited, 0, true, "lead"},
		{"shelved asker", model.StateIdle, 1, true, "lead"},
		{"no running project lead", model.StateStopped, 0, false, "asker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, p := sectionFixture(t)
			f.agents["asker"] = &agentRec{a: model.Agent{ID: "asker", Name: "project-worker", ProjectID: p.ID, Level: model.LevelWorker, State: model.StateIdle, Desired: "running"}}
			// An unrelated global lead must never receive project words by default.
			f.agents["global-lead"] = &agentRec{a: model.Agent{ID: "global-lead", Name: "tmx-lead", Level: model.LevelHead, State: model.StateIdle, Desired: "running"}}
			a, err := f.AskProjectOwner(Caller{AgentID: "asker"}, p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "Which plan?"}})
			if err != nil {
				t.Fatal(err)
			}
			f.agents["asker"].a.State = tc.state
			f.agents["asker"].a.ShelvedAt = tc.shelved
			if !tc.leadRunning {
				f.agents["lead"].a.State = model.StateStopped
			}
			answer := DecideRequest{Decision: "answer", Answer: "Original typed words", Version: a.Version, AnswerID: "route-once", FreeText: true}
			result, err := f.Decide(OwnerCaller, a.ID, answer)
			if err != nil || result.AgentID != "asker" {
				t.Fatalf("answer changed ownership: %+v %v", result, err)
			}
			// Reconnect after states change must not send the same answer to a second agent.
			f.agents["asker"].a.State = model.StateIdle
			f.agents["asker"].a.ShelvedAt = 0
			f.agents["lead"].a.State = model.StateIdle
			if _, err = f.Decide(OwnerCaller, a.ID, answer); err != nil {
				t.Fatal(err)
			}
			mails, err := e.st.ListMessages(store.MessageFilter{})
			if err != nil || len(mails) != 1 || mails[0].To != tc.want || !strings.Contains(mails[0].Body, answer.Answer) {
				t.Fatalf("routing: %+v %v", mails, err)
			}
		})
	}
}

func TestProjectCardArchitectUsesConfiguredSupervisor(t *testing.T) {
	empty, custom := "", "design-owner"
	for _, tc := range []struct {
		name           string
		supervisor     *string
		heads          int
		supervisorName string
		want           string
	}{
		{"default architect name", nil, 2, "architect", "supervisor"},
		{"configured name", &custom, 2, "design-owner", "supervisor"},
		{"configured missing never guesses head", &custom, 1, "architect", "asker"},
		{"default missing never guesses head", nil, 1, "", "asker"},
		{"unconfigured unique head", &empty, 1, "", "head0"},
		{"unconfigured two heads retain asker", &empty, 2, "", "asker"},
		{"unconfigured zero heads retain asker", &empty, 0, "", "asker"},
		{"unique tmx-lead never fallback", &empty, 0, "tmx-lead", "asker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, p := sectionFixture(t)
			f.opts.Supervisor = tc.supervisor
			delete(f.agents, "head")
			f.agents["lead"].a.State = model.StateStopped
			f.agents["asker"] = &agentRec{a: model.Agent{ID: "asker", Name: "asker", ProjectID: p.ID, Level: model.LevelWorker, State: model.StateIdle, Desired: "running"}}
			// Profile slugs and role prose never confer supervisor identity.
			f.agents["impostor"] = &agentRec{a: model.Agent{ID: "impostor", Name: "lookalike", Profile: "architect", Role: "architect", Level: model.LevelWorker, State: model.StateIdle, Desired: "running"}}
			for n := 0; n < tc.heads; n++ {
				id := fmt.Sprintf("head%d", n)
				f.agents[id] = &agentRec{a: model.Agent{ID: id, Name: id, Level: model.LevelHead, State: model.StateIdle, Desired: "running"}}
			}
			if tc.supervisorName != "" {
				f.agents["supervisor"] = &agentRec{a: model.Agent{ID: "supervisor", Name: tc.supervisorName, Level: model.LevelHead, State: model.StateIdle, Desired: "running"}}
			}
			a, err := f.AskProjectOwner(Caller{AgentID: "asker"}, p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "Where does this answer go?"}})
			if err != nil {
				t.Fatal(err)
			}
			f.agents["asker"].a.State = model.StateStopped
			var warnings bytes.Buffer
			f.log = slog.New(slog.NewTextHandler(&warnings, nil))
			for _, id := range []string{"first", "late", "late"} {
				_, err = f.Decide(OwnerCaller, a.ID, DecideRequest{Decision: "answer", Version: a.Version, AnswerID: id, Answer: "Keep my words", FreeText: true})
				if err != nil {
					t.Fatal(err)
				}
			}
			mails, err := e.st.ListMessages(store.MessageFilter{})
			if err != nil || len(mails) != 2 {
				t.Fatalf("mail: %+v %v", mails, err)
			}
			for _, m := range mails {
				if m.To != tc.want {
					t.Fatalf("recipient %s want %s", m.To, tc.want)
				}
			}
			wantWarnings := 0
			if tc.want == "asker" {
				wantWarnings = 1
			}
			if got := strings.Count(warnings.String(), "no fallback recipient"); got != wantWarnings {
				t.Fatalf("warnings %d want %d: %s", got, wantWarnings, warnings.String())
			}
		})
	}
}

func TestProjectCardSupervisorRejectsLeadCreatedNamesAndStoppedAgents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent string
		level  model.Level
		state  model.AgentState
		want   string
	}{
		{"lead-created architect is ignored", "lead", model.LevelWorker, model.StateIdle, "asker"},
		{"owner-created architect is accepted", "", model.LevelWorker, model.StateIdle, "architect-id"},
		{"head architect is accepted", "owner-root", model.LevelHead, model.StateIdle, "architect-id"},
		{"stopped supervisor is ignored", "", model.LevelHead, model.StateStopped, "asker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f, p := sectionFixture(t)
			f.agents["lead"].a.State = model.StateStopped
			f.agents["asker"] = &agentRec{a: model.Agent{ID: "asker", Name: "asker", ProjectID: p.ID, Level: model.LevelWorker, State: model.StateIdle, Desired: "running"}}
			f.agents["architect-id"] = &agentRec{a: model.Agent{ID: "architect-id", Name: "architect", ParentID: tc.parent, Level: tc.level, State: tc.state, Desired: "running"}}
			card, err := f.AskProjectOwner(Caller{AgentID: "asker"}, p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "Owner words must stay within the authorized route"}})
			if err != nil {
				t.Fatal(err)
			}
			f.agents["asker"].a.State = model.StateStopped
			if _, err = f.Decide(OwnerCaller, card.ID, DecideRequest{Decision: "answer", Answer: "Private project words", Version: card.Version, AnswerID: "trusted-route", FreeText: true}); err != nil {
				t.Fatal(err)
			}
			mails, err := e.st.ListMessages(store.MessageFilter{})
			if err != nil || len(mails) != 1 || mails[0].To != tc.want {
				t.Fatalf("mail: %+v %v", mails, err)
			}
		})
	}
}

func TestProjectCardAnswerReachesRemoteLeadOnce(t *testing.T) {
	fastPeerRetry(t)
	e, f, p := sectionFixture(t)
	f.agents["asker"] = &agentRec{a: model.Agent{ID: "asker", Name: "project-worker", ProjectID: p.ID, Level: model.LevelWorker, State: model.StateIdle, Desired: "running"}}
	a, err := f.AskProjectOwner(AgentCaller("asker"), p.ID, ProjectCardRequest{AskRequest: AskRequest{Question: "Which plan?"}})
	if err != nil {
		t.Fatal(err)
	}
	f.agents["asker"].a.State = model.StateStopped
	f.projects[p.ID].LeadID = PeerParty("remote-lead", "hostb")
	peers := &fakePeers{hosts: map[string]bool{"hostb": true}, errs: []error{ErrPeerUnreachable}}
	f.SetPeerSender(peers, "hostc")
	req := DecideRequest{Decision: "answer", Answer: "Keep these exact words", FreeText: true, Version: a.Version, AnswerID: "peer-once"}
	if _, err := f.Decide(OwnerCaller, a.ID, req); err != nil {
		t.Fatal(err)
	}
	_, _, mailID, err := e.st.CardAnswer(a.ID, req.AnswerID)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "peer answer accepted", func() bool { m, _ := e.st.GetMessage(mailID); return m.State == model.MsgDelivered })
	f.mu.Lock()
	f.agents["asker"].a.State = model.StateIdle
	f.projects[p.ID].LeadID = "lead"
	f.mu.Unlock()
	if _, err := f.Decide(OwnerCaller, a.ID, req); err != nil {
		t.Fatal(err)
	}
	sent := peers.Sent()
	if len(sent) != 2 || sent[0].ID != sent[1].ID || sent[1].To != "remote-lead" || sent[1].Sender != PeerOperator || !strings.Contains(sent[1].Body, req.Answer) {
		t.Fatalf("peer answer: %+v", sent)
	}
	mails, err := e.st.ListMessages(store.MessageFilter{})
	if err != nil || len(mails) != 1 || mails[0].To != PeerParty("remote-lead", "hostb") {
		t.Fatalf("retry rerouted/duplicated: %+v %v", mails, err)
	}
}

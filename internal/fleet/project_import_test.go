package fleet

import (
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
	"testing"
)

func importFixtureRequest() ProjectImportRequest {
	return ProjectImportRequest{ID: "migration-1", Source: "python@hostb", Original: json.RawMessage(`{"legacy_extra":"retained"}`), Section: &ProjectSectionImport{Status: "Ready\nReview needed\nNext step", Needs: []model.ProjectNeed{{Text: "Reviewed", Done: true}, {Text: "Ship"}}, PreviewDir: "C:/legacy/preview", UpdatedAt: 1000, UpdatedBy: "old-lead"}, Cards: []ProjectCardImport{
		{ProjectCardRequest: ProjectCardRequest{AskRequest: AskRequest{ID: "open-1", Question: "Choose", Options: []string{"A", "B"}}, Urgent: true, OwnerOnly: true}, Asker: "missing-lead", CreatedAt: 900},
		{ProjectCardRequest: ProjectCardRequest{AskRequest: AskRequest{ID: "later-1", Question: "Later?"}}, Status: "later", CreatedAt: 901},
		{ProjectCardRequest: ProjectCardRequest{AskRequest: AskRequest{ID: "answered-1", Question: "Previous?"}}, Status: "answered", Answer: "Original words", CreatedAt: 902, ClosedAt: 950},
	}}
}

func TestProjectImportPreservesPageCardsAndRetry(t *testing.T) {
	e, f, p := sectionFixture(t)
	req := importFixtureRequest()
	receipt, err := f.ImportProject(Caller{AgentID: "lead"}, p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := e.st.ProjectSection(p.ID)
	if err != nil || sec.UpdatedAt != 1000 || sec.UpdatedBy != "old-lead" || !sec.Needs[0].Done || sec.PreviewDir != req.Section.PreviewDir || sec.Origin.ImportedBy != "lead" {
		t.Fatalf("section: %+v %v", sec, err)
	}
	if len(receipt.CardIDs) != 3 {
		t.Fatal(receipt)
	}
	first, _ := e.st.GetApproval(receipt.CardIDs[0])
	later, _ := e.st.GetApproval(receipt.CardIDs[1])
	history, _ := e.st.GetApproval(receipt.CardIDs[2])
	if first.Kind != model.ApprovalQuestion || first.Version != 1 || first.CreatedAt != 900 || !first.Card.OwnerOnly || !later.Deferred || history.State != model.ApprovalExpired || history.Answer != "Original words" || history.DecidedAt != 950 {
		t.Fatalf("cards: %+v %+v %+v", first, later, history)
	}
	mail, _ := e.st.ListMessages(store.MessageFilter{})
	if len(mail) != 0 {
		t.Fatal("import sent mail", mail)
	}
	answer := DecideRequest{Decision: "answer", Version: 1, AnswerID: "import-answer", Answer: "Keep typed and dictated words", FreeText: true}
	if _, err := f.Decide(OwnerCaller, first.ID, answer); err != nil {
		t.Fatal(err)
	}
	// Retry after a real answer and restart cannot restore the original pending state.
	restarted := e.fleet()
	again, err := restarted.ImportProject(OwnerCaller, p.ID, req)
	if err != nil || again.SHA256 != receipt.SHA256 {
		t.Fatalf("retry: %+v %v", again, err)
	}
	got, _ := e.st.GetApproval(first.ID)
	if got.Answer != answer.Answer || got.State == model.ApprovalPending {
		t.Fatalf("answer overwritten: %+v", got)
	}
	mail, _ = e.st.ListMessages(store.MessageFilter{To: "lead"})
	if len(mail) != 1 {
		t.Fatal("answer did not reach assigned lead once", mail)
	}
	req.Section.Status = "Different"
	if _, err := f.ImportProject(OwnerCaller, p.ID, req); KindOf(err) != KindConflict {
		t.Fatal("changed replay accepted", err)
	}
}

func TestProjectImportValidationIsAtomic(t *testing.T) {
	e, f, p := sectionFixture(t)
	req := importFixtureRequest()
	req.Cards[2].Question = ""
	if _, err := f.ImportProject(OwnerCaller, p.ID, req); KindOf(err) != KindInvalid {
		t.Fatal(err)
	}
	if _, err := e.st.ProjectSection(p.ID); err != store.ErrNotFound {
		t.Fatal("partial section", err)
	}
	cards, _ := e.st.ListApprovals("")
	if len(cards) != 0 {
		t.Fatal("partial cards", cards)
	}
	req = importFixtureRequest()
	first, err := f.ImportProject(OwnerCaller, p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ID = "another-batch"
	if _, err := f.ImportProject(OwnerCaller, p.ID, req); KindOf(err) != KindConflict {
		t.Fatal("unguarded replacement", err)
	}
	stamp := int64(1000)
	req.ExpectedUpdatedAt = &stamp
	if _, err := f.ImportProject(OwnerCaller, p.ID, req); KindOf(err) != KindConflict {
		t.Fatal("duplicate source card accepted", err)
	}
	req.Cards = nil
	req.Section.UpdatedAt = 2000
	if _, err := f.ImportProject(OwnerCaller, p.ID, req); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.ProjectImport(p.ID, first.Source, first.ID)
	if string(got.Snapshot) != string(first.Snapshot) {
		t.Fatal("source snapshot changed")
	}
}

func TestProjectImportWriterAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  Caller
		agent   model.Agent
		allowed bool
	}{
		{"owner", OwnerCaller, model.Agent{}, true},
		{"assigned lead", Caller{AgentID: "lead"}, model.Agent{ID: "lead", Name: "lead", ProjectID: "other", Level: model.LevelLead}, true},
		{"member", Caller{AgentID: "member"}, model.Agent{ID: "member", Name: "member", ProjectID: "section-project", Level: model.LevelWorker}, true},
		{"architect", Caller{AgentID: "architect"}, model.Agent{ID: "architect", Name: "architect", Level: model.LevelHead}, true},
		{"unrelated head", Caller{AgentID: "head"}, model.Agent{ID: "head", Name: "another-head", Level: model.LevelHead}, false},
		{"squatter", Caller{AgentID: "squat"}, model.Agent{ID: "squat", Name: "architect", ParentID: "lead", Level: model.LevelWorker}, false},
		{"foreign worker", Caller{AgentID: "foreign"}, model.Agent{ID: "foreign", Name: "foreign", ProjectID: "other", Level: model.LevelWorker}, false},
		{"anonymous", Caller{}, model.Agent{}, false},
		{"companion", CompanionCaller, model.Agent{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, f, p := sectionFixture(t)
			if tc.agent.ID != "" {
				tc.agent.Harness = model.HarnessClaude
				tc.agent.State = model.StateIdle
				tc.agent.Desired = "running"
				f.agents[tc.agent.ID] = &agentRec{a: tc.agent}
			}
			_, err := f.ImportProject(tc.caller, p.ID, importFixtureRequest())
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
			status := "Updated status"
			_, err = f.SetProjectSection(tc.caller, p.ID, ProjectSectionPatch{Status: &status})
			if (err == nil) != tc.allowed {
				t.Fatalf("section allowed=%v: %v", tc.allowed, err)
			}
			_, err = f.SetMyProjectContribution(tc.caller, p.ID, ProjectContributionPatch{Status: &status})
			want := tc.allowed && !tc.caller.Owner
			if (err == nil) != want {
				t.Fatalf("contribution allowed=%v: %v", want, err)
			}
		})
	}
}

func TestProjectImportCannotRouteWordsToForeignSourceAuthor(t *testing.T) {
	e, f, p := sectionFixture(t)
	f.agents["foreign"] = &agentRec{a: model.Agent{ID: "foreign", Name: "foreign", ProjectID: "unrelated", Level: model.LevelWorker, State: model.StateIdle, Desired: "running", Harness: model.HarnessClaude}}
	req := importFixtureRequest()
	req.Cards = req.Cards[:1]
	req.Cards[0].Asker = "foreign"
	rec, err := f.ImportProject(Caller{AgentID: "lead"}, p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	ap, _ := e.st.GetApproval(rec.CardIDs[0])
	if ap.AgentID == "foreign" || ap.Card.Origin.SourceAuthor != "foreign" {
		t.Fatalf("source impersonation: %+v", ap)
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "answer", Version: 1, AnswerID: "words", Answer: "Private owner words", FreeText: true}); err != nil {
		t.Fatal(err)
	}
	foreign, _ := e.st.ListMessages(store.MessageFilter{To: "foreign"})
	lead, _ := e.st.ListMessages(store.MessageFilter{To: "lead"})
	if len(foreign) != 0 || len(lead) != 1 {
		t.Fatalf("cross-project route: foreign=%+v lead=%+v", foreign, lead)
	}
}

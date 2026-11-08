package fleet

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"testing"
)

func TestAgentAnswerUsesLivePrompt(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "answerer")
	p := e.chat.Last(w.ID)
	f.mu.Lock()
	f.addPermissionLocked(f.agents[w.ID], p, runner.ApprovalRequest{ID: "question", Kind: model.ApprovalQuestion, Options: []string{"first", "second"}})
	f.mu.Unlock()
	if _, _, err := f.AgentAnswer(AgentCaller(w.ID), w.ID, "1"); KindOf(err) != KindForbidden {
		t.Fatalf("worker: %v", err)
	}
	ap, d, err := f.AgentAnswer(OwnerCaller, w.ID, "2")
	if err != nil || d.Answer != "second" {
		t.Fatalf("answer: %+v %v", d, err)
	}
	if _, err = f.Decide(OwnerCaller, ap.ID, d); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.AgentAnswer(OwnerCaller, w.ID, "1"); KindOf(err) != KindConflict {
		t.Fatalf("resolved prompt: %v", err)
	}
}

func TestExplicitShelfAndRenamePersist(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	w := shelveWorker(t, e, f, "old-name")
	if _, err := f.ShelveAgent(OwnerCaller, w.ID, "waiting"); err != nil {
		t.Fatal(err)
	}
	a, err := f.RenameAgent(OwnerCaller, w.ID, "new-name")
	if err != nil || a.ID != w.ID || a.ShelvedAt == 0 {
		t.Fatalf("rename: %+v %v", a, err)
	}
	stored, err := f.st.GetAgent(w.ID)
	if err != nil || stored.Name != "new-name" || stored.ShelvedAt == 0 {
		t.Fatalf("persisted: %+v %v", stored, err)
	}
	if _, err = f.Agent("old-name"); KindOf(err) != KindNotFound {
		t.Fatalf("old alias unexpectedly resolves: %v", err)
	}
}

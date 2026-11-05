package store

import (
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestProjectSectionFeedbackTransaction(t *testing.T) {
	s := openTest(t)
	p := model.Project{ID: "p", Slug: "project"}
	if err := s.PutProject(p); err != nil {
		t.Fatal(err)
	}
	sec := model.ProjectSection{ProjectID: p.ID, Status: "Original"}
	if err := s.PutProjectSection(sec); err != nil {
		t.Fatal(err)
	}
	existing := model.Message{ID: "existing", From: model.Owner, To: "lead", Body: "Keep", State: model.MsgDelivered}
	if err := s.PutMessage(existing); err != nil {
		t.Fatal(err)
	}
	sec.Status = "Changed"
	rec := model.ProjectFeedback{ProjectID: p.ID, ID: "receipt", Messages: []model.Message{
		{ID: "new", From: model.Owner, To: "lead", Body: "New", State: model.MsgQueued},
		{ID: "existing", From: model.Owner, To: "lead", Body: "Overwrite", State: model.MsgQueued},
	}}
	if err := s.CommitProjectFeedback(sec, rec); err == nil {
		t.Fatal("expected duplicate message failure")
	}
	if _, err := s.ProjectFeedback(p.ID, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("receipt survived rollback: %v", err)
	}
	if _, err := s.GetMessage("new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mail survived rollback: %v", err)
	}
	got, _ := s.ProjectSection(p.ID)
	if got.Status != "Original" {
		t.Fatal("section survived rollback")
	}
	msg, _ := s.GetMessage("existing")
	if msg.Body != "Keep" || msg.State != model.MsgDelivered {
		t.Fatal("delivery overwritten")
	}
	rec.Messages = rec.Messages[:1]
	if err := s.CommitProjectFeedback(sec, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectSection(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan section")
	}
	if _, err := s.ProjectFeedback(p.ID, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan receipt")
	}
}

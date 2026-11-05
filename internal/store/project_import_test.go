package store

import (
	"encoding/json"
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"path/filepath"
	"testing"
)

func TestProjectImportCommitRollback(t *testing.T) {
	s := openTest(t)
	if err := s.PutProject(model.Project{ID: "p", Slug: "p"}); err != nil {
		t.Fatal(err)
	}
	sec := model.ProjectSection{ProjectID: "p", Status: "Imported", UpdatedAt: 1000}
	card := model.Approval{ID: "card", Kind: model.ApprovalQuestion, State: model.ApprovalPending, Version: 1, Question: "Keep?"}
	rec := model.ProjectImportReceipt{ProjectID: "p", Source: "python", ID: "batch", Snapshot: json.RawMessage(`{"extra":"kept"}`)}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_import BEFORE INSERT ON project_imports BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitProjectImport(&sec, nil, []model.Approval{card}, rec); err == nil {
		t.Fatal("expected rollback")
	}
	if _, err := s.ProjectSection("p"); !errors.Is(err, ErrNotFound) {
		t.Fatal("section escaped rollback", err)
	}
	if _, err := s.GetApproval("card"); !errors.Is(err, ErrNotFound) {
		t.Fatal("card escaped rollback", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_import`); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitProjectImport(&sec, nil, []model.Approval{card}, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectImport("p", "python", "batch")
	if err != nil || string(got.Snapshot) != string(rec.Snapshot) {
		t.Fatalf("receipt: %+v %v", got, err)
	}
	if err := s.DeleteProject("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectImport("p", "python", "batch"); !errors.Is(err, ErrNotFound) {
		t.Fatal("orphan receipt", err)
	}
}

func TestProjectImportSurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(model.Project{ID: "p", Slug: "p"}); err != nil {
		t.Fatal(err)
	}
	rec := model.ProjectImportReceipt{ProjectID: "p", Source: "python", ID: "batch", Snapshot: json.RawMessage(`{"original":{"unknown":"retained"}}`), CardIDs: []string{"card"}}
	card := model.Approval{ID: "card", Kind: model.ApprovalQuestion, State: model.ApprovalPending, Question: "Kept", Version: 1}
	if err := s.CommitProjectImport(nil, nil, []model.Approval{card}, rec); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ProjectImport("p", "python", "batch")
	if err != nil || string(got.Snapshot) != string(rec.Snapshot) {
		t.Fatalf("receipt lost: %+v %v", got, err)
	}
	ap, err := s.GetApproval("card")
	if err != nil || ap.Question != "Kept" {
		t.Fatalf("card lost: %+v %v", ap, err)
	}
}

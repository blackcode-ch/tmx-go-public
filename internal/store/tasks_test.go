package store

import (
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestTaskAtomicAcceptanceRecoveryAndImmutableTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	task := model.TaskRecord{Principal: "operator@box", AgentID: "agent", Receipt: model.TaskReceipt{ID: "123456789abc", State: "queued", CreatedAt: 1, UpdatedAt: 1}}
	m := model.Message{ID: task.Receipt.ID, From: "owner", To: "agent", Body: "do it", State: model.MsgQueued, Receipt: "queued"}
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptTask(task, m); err == nil {
		t.Fatal("ordinary mail ID replaced")
	}
	if _, err := s.Task(task.Receipt.ID); err != ErrNotFound {
		t.Fatal("partial acceptance persisted", err)
	}
	task.Receipt.ID = "123456789abd"
	m.ID = task.Receipt.ID
	if err := s.AcceptTask(task, m); err != nil {
		t.Fatal(err)
	}
	task.Receipt.State = "dispatching"
	if err := s.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverTasks(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(task.Receipt.ID)
	if err != nil || got.Receipt.State != "uncertain" || got.Receipt.ReceiptID == nil {
		t.Fatal(got, err)
	}
	mail, _ := s.GetMessage(m.ID)
	if mail.State == model.MsgQueued {
		t.Fatal("uncertain task can still be replayed")
	}
	if err := s.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Task(task.Receipt.ID)
	if again.Receipt.State != "uncertain" || *again.Receipt.ReceiptID != *got.Receipt.ReceiptID || again.Receipt.UpdatedAt != got.Receipt.UpdatedAt {
		t.Fatal("terminal receipt changed")
	}
}

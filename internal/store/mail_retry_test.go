package store

import (
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMailRetryBindingAndClaimSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := model.Message{ID: "one", From: "sender", To: "recipient", Body: "body", State: model.MsgQueued, RetryID: "retry", RetryHash: "binding", Receipt: "queued"}
	if err = s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	duplicate := m
	duplicate.ID = "two"
	if err = s.PutMessage(duplicate); err == nil {
		t.Fatal("duplicate sender identity admitted")
	}
	if err = s.ClaimMail([]string{m.ID, "missing"}, 10); err == nil {
		t.Fatal("partial claim accepted")
	}
	got, _ := s.GetMessage(m.ID)
	if got.State != model.MsgQueued {
		t.Fatal("partial claim was not rolled back")
	}
	if err = s.ClaimMail([]string{m.ID}, 11); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RecoverMailReceipts(); err != nil {
		t.Fatal(err)
	}
	got, err = s.MessageByRetry("sender", "retry")
	if err != nil || got.ID != m.ID || got.RetryHash != "binding" || got.Receipt != "unconfirmed" || got.State != model.MsgDelivered {
		t.Fatalf("recovered %+v %v", got, err)
	}
	if err = s.ClaimMail([]string{m.ID}, 12); err == nil {
		t.Fatal("uncertain mail re-claimed")
	}
}

package store

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"path/filepath"
	"testing"
)

func TestCardAnswerRollback(t *testing.T) {
	s := openTest(t)
	a := model.Approval{ID: "card", AgentID: "asker", Kind: model.ApprovalQuestion, State: model.ApprovalPending, Version: 1}
	if err := s.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_card_receipt BEFORE INSERT ON card_answers BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	answer := a
	answer.State = model.ApprovalAnswered
	answer.AnswerID = "request"
	m := model.Message{ID: "mail", From: model.Owner, To: "fleet:lead@hostb", Body: "answer", State: model.MsgQueued}
	if err := s.CommitCardAnswer(a, answer, "request", "{}", &m, PeerMessage{LocalID: m.ID, Peer: "hostb", WireID: "123456789abc", Direction: PeerOut}); err == nil {
		t.Fatal("expected failure")
	}
	got, err := s.GetApproval(a.ID)
	if err != nil || got.State != model.ApprovalPending || got.AnswerID != "" {
		t.Fatalf("closed without receipt: %+v %v", got, err)
	}
	if _, err := s.GetMessage(m.ID); err != ErrNotFound {
		t.Fatalf("mail escaped rollback: %v", err)
	}
	if _, err := s.PeerByLocal(m.ID); err != ErrNotFound {
		t.Fatalf("outbox escaped rollback: %v", err)
	}
}

func TestCardAnswerReceiptSurvivesDatabaseReopen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TMXG_HOME", home)
	path := filepath.Join(home, "cards.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := model.Approval{ID: "card", AgentID: "asker", Kind: model.ApprovalQuestion, State: model.ApprovalPending, Version: 2}
	if err := s.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	answer := a
	answer.State, answer.Answer, answer.AnswerID = model.ApprovalAnswered, "yes", "stable-request"
	m := model.Message{ID: "queued-answer", From: model.Owner, To: "fleet:lead@hostb", Body: "yes", State: model.MsgQueued}
	if err := s.CommitCardAnswer(a, answer, answer.AnswerID, "request-body", &m, PeerMessage{LocalID: m.ID, Peer: "hostb", WireID: "abcdef123456", Direction: PeerOut, NextAttemptAt: 123}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request, result, messageID, err := s.CardAnswer(a.ID, answer.AnswerID)
	if err != nil || request != "request-body" || result.AnswerID != answer.AnswerID || messageID != m.ID {
		t.Fatalf("lost receipt: %s %+v %s %v", request, result, messageID, err)
	}
	pm, err := s.PeerByLocal(messageID)
	if err != nil || pm.WireID != "abcdef123456" || pm.NextAttemptAt != 123 {
		t.Fatalf("lost peer outbox: %+v %v", pm, err)
	}
	mail, err := s.GetMessage(messageID)
	if err != nil || mail.State != model.MsgQueued {
		t.Fatalf("lost queued answer: %+v %v", mail, err)
	}
}

func TestCardAnswerOutboxFailureRollsBackDecision(t *testing.T) {
	s := openTest(t)
	a := model.Approval{ID: "card", AgentID: "asker", Kind: model.ApprovalQuestion, State: model.ApprovalPending, Version: 1}
	if err := s.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_peer_mapping BEFORE INSERT ON peer_messages BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	result := a
	result.State = model.ApprovalAnswered
	m := model.Message{ID: "answer", From: model.Owner, To: "fleet:lead@hostb", State: model.MsgQueued}
	if err := s.CommitCardAnswer(a, result, "request", "{}", &m, PeerMessage{LocalID: m.ID, Peer: "hostb", WireID: "abcdef123456", Direction: PeerOut}); err == nil {
		t.Fatal("accepted failed outbox")
	}
	if got, err := s.GetApproval(a.ID); err != nil || got.State != model.ApprovalPending {
		t.Fatalf("closed without outbox: %+v %v", got, err)
	}
	if _, err := s.GetMessage(m.ID); err != ErrNotFound {
		t.Fatalf("message escaped rollback: %v", err)
	}
	if _, _, _, err := s.CardAnswer(a.ID, "request"); err != ErrNotFound {
		t.Fatalf("receipt escaped rollback: %v", err)
	}
}

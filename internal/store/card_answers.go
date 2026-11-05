package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// CardAnswer is a durable receipt, scoped to one card and one request identity.
func (s *Store) CardAnswer(id, requestID string) (string, model.Approval, string, error) {
	var request, result, messageID string
	err := s.db.QueryRow(`SELECT request, result, message_id FROM card_answers WHERE approval_id=? AND request_id=?`, id, requestID).Scan(&request, &result, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", model.Approval{}, "", ErrNotFound
	}
	if err != nil {
		return "", model.Approval{}, "", err
	}
	var ap model.Approval
	err = json.Unmarshal([]byte(result), &ap)
	return request, ap, messageID, err
}

// CommitCardAnswer commits closure/deferral/reask, its receipt and any owner mail
// together. A crash cannot leave a closed card without its queued answer.
func (s *Store) CommitCardAnswer(old, result model.Approval, requestID, request string, mail *model.Message, peers ...PeerMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cur, err := scanApproval(tx.QueryRow(`SELECT `+approvalCols+` FROM approvals WHERE id=?`, old.ID))
	if err != nil {
		return err
	}
	if cur.Version != old.Version || cur.State != old.State {
		return fmt.Errorf("card changed before commit")
	}
	if err := putApproval(tx, result); err != nil {
		return err
	}
	messageID := ""
	if mail != nil {
		m := *mail
		messageID = m.ID
		if _, err := tx.Exec(`INSERT INTO messages (`+messageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.From, m.To, m.Body, m.State, nullable(m.ReplyTo), m.CreatedAt, m.DeliveredAt, m.ThreadID, m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt); err != nil {
			return err
		}
	}
	for _, peer := range peers {
		if err := putPeerMapping(tx, peer); err != nil {
			return err
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO card_answers (approval_id, request_id, request, result, message_id) VALUES (?, ?, ?, ?, ?)`, old.ID, requestID, request, string(data), messageID); err != nil {
		return err
	}
	return tx.Commit()
}

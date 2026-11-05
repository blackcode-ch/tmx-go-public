package store

import (
	"database/sql"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const messageCols = `id, from_id, to_id, body, state, reply_to, created_at, delivered_at, thread_id, retry_id, retry_hash, receipt, receipt_at`

func scanMessage(r scanner) (model.Message, error) {
	var m model.Message
	var reply sql.NullString
	err := r.Scan(&m.ID, &m.From, &m.To, &m.Body, &m.State, &reply, &m.CreatedAt, &m.DeliveredAt, &m.ThreadID, &m.RetryID, &m.RetryHash, &m.Receipt, &m.ReceiptAt)
	m.ReplyTo = reply.String
	return m, err
}

func (s *Store) PutMessage(m model.Message) error {
	_, err := s.db.Exec(upsert("messages", messageCols),
		m.ID, m.From, m.To, m.Body, m.State, nullable(m.ReplyTo), m.CreatedAt, m.DeliveredAt, m.ThreadID, m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt)
	return err
}

// MessageByRetry is scoped to the authenticated sender, never a client-supplied identity.
func (s *Store) MessageByRetry(from, key string) (model.Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+messageCols+` FROM messages WHERE from_id=? AND retry_id=?`, from, key))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return m, err
}

func (s *Store) SetMessageReceipt(id, receipt string, at int64) error {
	_, err := s.db.Exec(`UPDATE messages SET receipt=?, receipt_at=? WHERE id=?`, receipt, at, id)
	return err
}

// ClaimMail commits the no-replay boundary before any runner can receive bytes.
func (s *Store) ClaimMail(ids []string, at int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE messages SET state='delivered', delivered_at=?, receipt='dispatching', receipt_at=? WHERE id=? AND state='queued'`, at, at, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("mail is no longer queued")
		}
	}
	return tx.Commit()
}

// A daemon restart cannot prove how far an in-flight write got. Never replay it.
func (s *Store) RecoverMailReceipts() error {
	_, err := s.db.Exec(`UPDATE messages SET receipt='unconfirmed', receipt_at=? WHERE receipt='dispatching'`, model.Now())
	return err
}

func (s *Store) PendingMailReceipts(to string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM messages WHERE to_id=? AND receipt='dispatching'`, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetMessage(id string) (model.Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+messageCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// SetMessageState updates state (and delivered_at when non-zero).
func (s *Store) SetMessageState(id string, st model.MessageState, deliveredAt int64) error {
	_, err := s.db.Exec(`UPDATE messages SET state = ?,
		delivered_at = CASE WHEN ? > 0 THEN ? ELSE delivered_at END WHERE id = ?`,
		st, deliveredAt, deliveredAt, id)
	return err
}

// MessageFilter selects messages for ListMessages. Empty fields match everything.
type MessageFilter struct {
	Party      string // from OR to this ID
	From       string
	To         string
	BodyPrefix string // the body starts with this (e.g. a label)
	State      model.MessageState
	Limit      int // 0 = 200
}

// ListMessages returns matching messages, oldest first (the newest Limit of them).
func (s *Store) ListMessages(f MessageFilter) ([]model.Message, error) {
	q := `SELECT ` + messageCols + `, rowid AS rid FROM messages WHERE 1=1`
	var args []any
	if f.Party != "" {
		q += ` AND (from_id = ? OR to_id = ?)`
		args = append(args, f.Party, f.Party)
	}
	if f.From != "" {
		q += ` AND from_id = ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND to_id = ?`
		args = append(args, f.To)
	}
	if f.BodyPrefix != "" {
		q += ` AND substr(body, 1, ?) = ?`
		args = append(args, len([]rune(f.BodyPrefix)), f.BodyPrefix)
	}
	if f.State != "" {
		q += ` AND state = ?`
		args = append(args, f.State)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q = `SELECT ` + messageCols + ` FROM (` + q + ` ORDER BY created_at DESC, rid DESC LIMIT ?) ORDER BY created_at, rid`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountQueued counts messages waiting for delivery to an agent.
func (s *Store) CountQueued(to string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_id = ? AND state = ?`, to, model.MsgQueued).Scan(&n)
	return n, err
}

// CountSent counts messages from one party to another created at or after since (ms).
func (s *Store) CountSent(from, to string, since int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE from_id = ? AND to_id = ? AND created_at >= ?`,
		from, to, since).Scan(&n)
	return n, err
}

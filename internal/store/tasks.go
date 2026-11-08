package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// taskTransaction uses FULL synchronous for receipts which acknowledge durable
// acceptance. Restore the connection's normal setting before returning it.
func (s *Store) taskTransaction(fn func(*sql.Tx) error) error {
	ctx := context.Background()
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	defer c.ExecContext(ctx, "PRAGMA synchronous=NORMAL")
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// AcceptTask commits the receipt and corresponding mail together. Neither ID
// may replace an existing task or an ordinary message.
func (s *Store) AcceptTask(t model.TaskRecord, m model.Message) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.taskTransaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_receipts(id,agent_id,state,data) VALUES(?,?,?,?)`, t.Receipt.ID, t.AgentID, t.Receipt.State, b); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO messages (`+messageCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.ID, m.From, m.To, m.Body, m.State, nullable(m.ReplyTo), m.CreatedAt, m.DeliveredAt, m.ThreadID, m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt)
		return err
	})
}

func (s *Store) Task(id string) (model.TaskRecord, error) {
	var b []byte
	err := s.db.QueryRow(`SELECT data FROM task_receipts WHERE id=?`, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	var t model.TaskRecord
	if err == nil {
		err = json.Unmarshal(b, &t)
	}
	return t, err
}

func (s *Store) ActiveTasks(agent string) ([]model.TaskRecord, error) {
	q := `SELECT data FROM task_receipts WHERE state IN ('queued','dispatching','running')`
	var args []any
	if agent != "" {
		q += ` AND agent_id=?`
		args = append(args, agent)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TaskRecord
	for rows.Next() {
		var b []byte
		var t model.TaskRecord
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TaskCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM task_receipts`).Scan(&n)
	return n, err
}

// SaveTask never rewrites a terminal receipt. Settling also removes any queued
// input, preventing a retry tick from delivering a task already reported final.
func (s *Store) SaveTask(t model.TaskRecord) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.taskTransaction(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE task_receipts SET state=?, data=? WHERE id=? AND state IN ('queued','dispatching','running')`, t.Receipt.State, b, t.Receipt.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if !t.Receipt.Active() {
			_, err = tx.Exec(`UPDATE messages SET state='delivered',receipt='unconfirmed' WHERE id=? AND state='queued'`, t.Receipt.ID)
		}
		return err
	})
}

func (s *Store) RecoverTasks() error {
	tasks, err := s.ActiveTasks("")
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Receipt.State == "queued" {
			continue
		}
		t.Receipt.Finish("uncertain", "Daemon restarted; the prior turn's causal observation was lost.", "")
		if err := s.SaveTask(t); err != nil {
			return err
		}
	}
	return nil
}

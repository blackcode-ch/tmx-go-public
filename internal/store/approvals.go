package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const approvalCols = `id, agent_id, kind, tool, input, description, question, options, state, answer, created_at, decided_at, thread_id, version, card, deferred, answer_id`

func scanApproval(r scanner) (model.Approval, error) {
	var a model.Approval
	var input sql.NullString
	var options string
	var card string
	err := r.Scan(&a.ID, &a.AgentID, &a.Kind, &a.Tool, &input, &a.Description, &a.Question, &options,
		&a.State, &a.Answer, &a.CreatedAt, &a.DecidedAt, &a.ThreadID, &a.Version, &card, &a.Deferred, &a.AnswerID)
	if err != nil {
		return a, err
	}
	if input.Valid && input.String != "" {
		a.Input = json.RawMessage(input.String)
	}
	if err := json.Unmarshal([]byte(card), &a.Card); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(options), &a.Options); err != nil {
		a.Options = nil
	}
	if a.Options == nil {
		a.Options = []string{}
	}
	return a, nil
}

func (s *Store) PutApproval(a model.Approval) error {
	return putApproval(s.db, a)
}

func putApproval(db interface {
	Exec(string, ...any) (sql.Result, error)
}, a model.Approval) error {
	card, err := json.Marshal(a.Card)
	if err != nil {
		return err
	}
	if a.Version == 0 {
		a.Version = 1
	}
	opts := a.Options
	if opts == nil {
		opts = []string{}
	}
	ob, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	var input any
	if len(a.Input) > 0 {
		input = string(a.Input)
	}
	_, err = db.Exec(upsert("approvals", approvalCols),
		a.ID, a.AgentID, a.Kind, a.Tool, input, a.Description, a.Question, string(ob), a.State, a.Answer,
		a.CreatedAt, a.DecidedAt, a.ThreadID, a.Version, string(card), a.Deferred, a.AnswerID)
	return err
}

func (s *Store) GetApproval(id string) (model.Approval, error) {
	a, err := scanApproval(s.db.QueryRow(`SELECT `+approvalCols+` FROM approvals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// ListApprovals returns approvals in a state ("" = all, newest 500), oldest first.
func (s *Store) ListApprovals(state model.ApprovalState) ([]model.Approval, error) {
	q := `SELECT * FROM (SELECT ` + approvalCols + ` FROM approvals`
	var args []any
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY created_at DESC LIMIT 500) ORDER BY created_at`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

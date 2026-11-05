package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// PutCompanionTask inserts or replaces a task for Companion.
func (s *Store) PutCompanionTask(t model.CompanionTask) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(upsert("companion_tasks", "id, state, data, created_at, updated_at"),
		t.ID, string(t.State), string(b), t.CreatedAt, t.UpdatedAt)
	return err
}

// GetCompanionTask returns one task (ErrNotFound).
func (s *Store) GetCompanionTask(id string) (model.CompanionTask, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM companion_tasks WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanionTask{}, ErrNotFound
	}
	if err != nil {
		return model.CompanionTask{}, err
	}
	var t model.CompanionTask
	return t, json.Unmarshal([]byte(data), &t)
}

// ListCompanionTasks returns tasks newest first: those in the given states (none = all), at
// most limit (<= 0: 200).
func (s *Store) ListCompanionTasks(limit int, states ...model.CompanionTaskState) ([]model.CompanionTask, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT data FROM companion_tasks`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (`
		for i, st := range states {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, string(st))
		}
		q += `)`
	}
	q += ` ORDER BY created_at DESC, rowid DESC LIMIT ?` // same millisecond: the later insert is newer (upserts keep rowid)
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CompanionTask{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var t model.CompanionTask
		if err := json.Unmarshal([]byte(data), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

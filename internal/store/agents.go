package store

import (
	"database/sql"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const agentCols = `id, name, project_id, parent_id, level, harness, mode, model, cwd, role, brief,
	state, desired, session_id, pid, created_at, updated_at, last_activity_at, cost_usd, turns, error, profile, shelved_at, done_at, protected`

type scanner interface{ Scan(...any) error }

func scanAgent(r scanner) (model.Agent, error) {
	var a model.Agent
	var project, parent sql.NullString
	err := r.Scan(&a.ID, &a.Name, &project, &parent, &a.Level, &a.Harness, &a.Mode, &a.Model,
		&a.Cwd, &a.Role, &a.Brief, &a.State, &a.Desired, &a.SessionID, &a.PID, &a.CreatedAt,
		&a.UpdatedAt, &a.LastActivityAt, &a.CostUSD, &a.Turns, &a.Error, &a.Profile, &a.ShelvedAt, &a.DoneAt, &a.Protected)
	a.ProjectID, a.ParentID = project.String, parent.String
	return a, err
}

// PutAgent inserts or replaces an agent (Queued is derived, not stored).
func (s *Store) PutAgent(a model.Agent) error {
	_, err := s.db.Exec(upsert("agents", agentCols),
		a.ID, a.Name, nullable(a.ProjectID), nullable(a.ParentID), a.Level, a.Harness, a.Mode, a.Model,
		a.Cwd, a.Role, a.Brief, a.State, a.Desired, a.SessionID, a.PID, a.CreatedAt, a.UpdatedAt,
		a.LastActivityAt, a.CostUSD, a.Turns, a.Error, a.Profile, a.ShelvedAt, a.DoneAt, a.Protected)
	return err
}

func (s *Store) GetAgent(id string) (model.Agent, error) {
	a, err := scanAgent(s.db.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) ListAgents() ([]model.Agent, error) {
	rows, err := s.db.Query(`SELECT ` + agentCols + ` FROM agents ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes an agent with its timeline and approvals. Messages stay (they are
// the other party's history too).
func (s *Store) DeleteAgent(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM timeline WHERE agent_id = ?`,
		`DELETE FROM approvals WHERE agent_id = ?`,
		`DELETE FROM agents WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

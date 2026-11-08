package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

func (s *Store) AgentProjectView(id string, out any) error {
	var data string
	err := s.db.QueryRow(`SELECT data FROM agent_project_views WHERE agent_id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), out)
}

func (s *Store) PutAgentProjectView(id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO agent_project_views(agent_id,data) VALUES (?,?) ON CONFLICT(agent_id) DO UPDATE SET data=excluded.data`, id, string(data))
	return err
}

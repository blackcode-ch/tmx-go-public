package store

import "github.com/blackcode-ch/tmx-go/internal/model"

// SaveMessagePin is idempotent for repeated pin requests. Only a re-pin changes its age.
func (s *Store) SaveMessagePin(p model.MessagePin) error {
	_, err := s.db.Exec(`INSERT INTO message_pins
		(id, source, label, href, agent_id, text, message_at, pinned_at, state)
		VALUES (?,?,?,?,?,?,?,?, 'active') ON CONFLICT(source) DO UPDATE SET
		state='active', pinned_at=CASE WHEN message_pins.state='active' THEN message_pins.pinned_at ELSE excluded.pinned_at END`,
		p.ID, p.Source, p.Label, p.Href, p.AgentID, p.Text, p.MessageAt, p.PinnedAt)
	return err
}

func (s *Store) SetMessagePinState(id, state string) error {
	r, err := s.db.Exec(`UPDATE message_pins SET state=? WHERE id=?`, state, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) MessagePins(agent string, owner bool) ([]model.MessagePin, error) {
	q := `SELECT id, source, label, href, agent_id, text, message_at, pinned_at, state FROM message_pins WHERE state != 'unpinned'`
	args := []any{}
	if !owner {
		q += ` AND agent_id=?`
		args = append(args, agent)
	}
	rows, err := s.db.Query(q+` ORDER BY pinned_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MessagePin{}
	for rows.Next() {
		var p model.MessagePin
		if err := rows.Scan(&p.ID, &p.Source, &p.Label, &p.Href, &p.AgentID, &p.Text, &p.MessageAt, &p.PinnedAt, &p.State); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

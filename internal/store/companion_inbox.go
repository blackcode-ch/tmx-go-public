package store

import "github.com/blackcode-ch/tmx-go/internal/model"

// CompanionInboxPage counts and pages the recipient's mail in one read snapshot.
// Filtering unread before LIMIT keeps older unread replies reachable.
func (s *Store) CompanionInboxPage(unread bool, limit, offset int) ([]model.Message, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	where := " WHERE to_id=?"
	args := []any{model.CompanionParty}
	if unread {
		where += " AND state<>?"
		args = append(args, model.MsgRead)
	}
	var total int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query("SELECT "+messageCols+" FROM messages"+where+" ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]model.Message, 0)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

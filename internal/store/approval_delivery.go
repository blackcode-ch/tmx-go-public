package store

// ApprovalDelivery is diagnostic metadata only; it never authorizes a decision.
func (s *Store) ApprovalDelivery(id string) (notified string, seen int64) {
	notified = "not_attempted"
	_ = s.db.QueryRow(`SELECT notified, seen_at FROM approval_delivery WHERE id = ?`, id).Scan(&notified, &seen)
	return
}

func (s *Store) CreateApprovalDelivery(id string) (bool, error) {
	r, err := s.db.Exec(`INSERT OR IGNORE INTO approval_delivery(id) VALUES (?)`, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (s *Store) SetApprovalNotified(id, status string) error {
	_, err := s.db.Exec(`UPDATE approval_delivery SET notified = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) MarkApprovalSeen(id string, at int64) (bool, error) {
	r, err := s.db.Exec(`UPDATE approval_delivery SET seen_at = ? WHERE id = ? AND seen_at = 0`, at, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

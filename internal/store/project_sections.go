package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Store) ProjectSection(id string) (model.ProjectSection, error) {
	var out model.ProjectSection
	var data string
	err := s.db.QueryRow("SELECT data FROM project_sections WHERE project_id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(data), &out)
	return out, err
}

func (s *Store) PutProjectSection(sec model.ProjectSection) error {
	data, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO project_sections(project_id,data) VALUES (?,?) ON CONFLICT(project_id) DO UPDATE SET data=excluded.data", sec.ProjectID, string(data))
	return err
}

func (s *Store) ProjectFeedback(project, id string) (model.ProjectFeedback, error) {
	var out model.ProjectFeedback
	var data string
	err := s.db.QueryRow("SELECT data FROM project_feedback WHERE project_id=? AND id=?", project, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(data), &out)
	return out, err
}

// CommitProjectFeedback saves the receipt, ticks and queued messages together.
// A retry cannot overwrite a delivery state or create another message.
func (s *Store) CommitProjectFeedback(sec model.ProjectSection, rec model.ProjectFeedback) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	section, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO project_feedback(project_id,id,data) VALUES (?,?,?)", rec.ProjectID, rec.ID, string(data)); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO project_sections(project_id,data) VALUES (?,?) ON CONFLICT(project_id) DO UPDATE SET data=excluded.data", sec.ProjectID, string(section)); err != nil {
		return err
	}
	for _, m := range rec.Messages {
		if _, err = tx.Exec("INSERT INTO messages ("+messageCols+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)", m.ID, m.From, m.To, m.Body, m.State, nullable(m.ReplyTo), m.CreatedAt, m.DeliveredAt, m.ThreadID, m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

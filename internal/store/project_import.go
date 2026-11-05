package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Store) ProjectImport(project, source, id string) (model.ProjectImportReceipt, error) {
	var rec model.ProjectImportReceipt
	var data string
	err := s.db.QueryRow(`SELECT data FROM project_imports WHERE project_id=? AND source=? AND id=?`, project, source, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, ErrNotFound
	}
	if err != nil {
		return rec, err
	}
	err = json.Unmarshal([]byte(data), &rec)
	return rec, err
}

// CommitProjectImport creates the section, cards and source receipt together.
// It never updates an existing card, especially one answered after migration.
func (s *Store) CommitProjectImport(sec *model.ProjectSection, expected *int64, cards []model.Approval, rec model.ProjectImportReceipt) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if sec != nil {
		var oldData string
		err := tx.QueryRow(`SELECT data FROM project_sections WHERE project_id=?`, rec.ProjectID).Scan(&oldData)
		if err == nil {
			var old model.ProjectSection
			if err := json.Unmarshal([]byte(oldData), &old); err != nil {
				return err
			}
			if expected == nil || old.UpdatedAt != *expected {
				return fmt.Errorf("project section changed before import")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if expected != nil {
			return fmt.Errorf("project section no longer exists")
		}
		data, err := json.Marshal(sec)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO project_sections(project_id,data) VALUES (?,?) ON CONFLICT(project_id) DO UPDATE SET data=excluded.data`, sec.ProjectID, string(data)); err != nil {
			return err
		}
	}
	for _, card := range cards {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM approvals WHERE id=?`, card.ID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return fmt.Errorf("source card already exists")
		}
		if err := putApproval(tx, card); err != nil {
			return err
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO project_imports(project_id,source,id,data) VALUES (?,?,?,?)`, rec.ProjectID, rec.Source, rec.ID, string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

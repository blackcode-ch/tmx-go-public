package store

import (
	"database/sql"
	"encoding/json"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Profiles are stored as JSON (the settings change more often than the schema should).

// ListProfiles returns every stored profile, in slug order.
func (s *Store) ListProfiles() ([]model.Profile, error) {
	rows, err := s.db.Query(`SELECT data, created_at, updated_at FROM profiles ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Profile
	for rows.Next() {
		var data string
		var p model.Profile
		var created, updated int64
		if err := rows.Scan(&data, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, err
		}
		p.CreatedAt, p.UpdatedAt = created, updated
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutProfile inserts or replaces a profile (by slug).
func (s *Store) PutProfile(p model.Profile) error {
	p.BuiltIn, p.Modified = false, false
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO profiles (slug, data, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		p.Slug, string(b), p.CreatedAt, p.UpdatedAt)
	return err
}

// DeleteProfile removes a profile.
func (s *Store) DeleteProfile(slug string) error {
	_, err := s.db.Exec(`DELETE FROM profiles WHERE slug = ?`, slug)
	return err
}

// UpgradeModelDefaults changes only untouched pre-ladder defaults, once per store.
// The marker and profile writes commit together, so an owner can later restore
// Sonnet-only settings without the next daemon start changing them again.
func (s *Store) UpgradeModelDefaults() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT OR IGNORE INTO profile_default_upgrades(name) VALUES ('model-ladder-2026-11')`)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	for _, slug := range []string{"lead", "worker", "reviewer"} {
		var data string
		err := tx.QueryRow(`SELECT data FROM profiles WHERE slug = ?`, slug).Scan(&data)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		var current model.Profile
		if err := json.Unmarshal([]byte(data), &current); err != nil {
			return err
		}
		updated, _ := model.DefaultProfile(slug)
		previous := updated.Clone()
		previous.Model = "claude-sonnet-5-5"
		previous.AllowedModels = []string{"claude-sonnet-5-5"}
		if !current.SameSettings(previous) {
			continue
		}
		current.Model, current.AllowedModels = updated.Model, updated.AllowedModels
		current.UpdatedAt = model.Now()
		b, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE profiles SET data = ?, updated_at = ? WHERE slug = ?`, string(b), current.UpdatedAt, slug); err != nil {
			return err
		}
	}
	return tx.Commit()
}

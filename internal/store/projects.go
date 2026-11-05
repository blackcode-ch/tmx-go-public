package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// projectWriteCols is what PutProject writes: every non-progress field. Progress columns are
// deliberately excluded (and so never touched by this generic upsert, on either an insert or
// an update) — SaveProjectProgress is their only writer, so a rename/status/etc. save can
// never clobber a concurrently-newer progress write with stale (or merely zero-value) data.
const projectWriteCols = `id, name, slug, folder, brief, lead_id, status, created_at, updated_at`

// projectCols is the full column list for reads: every field, including progress.
const projectCols = projectWriteCols + `,
	progress, progress_basis, progress_state, progress_version, progress_updated_at, progress_run`

// progressHistoryKeep: samples kept per project (newest N), independent of the 30-day prune.
const progressHistoryKeep = 100

// progressHistoryMaxAge: a sample older than this is pruned even if the project is still active.
const progressHistoryMaxAge = 30 * 24 * time.Hour

func scanProject(r scanner) (model.Project, error) {
	var p model.Project
	var percent sql.NullInt64
	var basis, state, run string
	err := r.Scan(&p.ID, &p.Name, &p.Slug, &p.Folder, &p.Brief, &p.LeadID, &p.Status, &p.CreatedAt, &p.UpdatedAt,
		&percent, &basis, &state, &p.ProgressVersion, &p.ProgressUpdatedAt, &run)
	if err != nil {
		return p, err
	}
	if percent.Valid {
		v := int(percent.Int64)
		p.Progress = &v
	}
	if basis != "" {
		if err := json.Unmarshal([]byte(basis), &p.ProgressBasis); err != nil {
			return p, err
		}
	}
	p.ProgressState = model.ProgressState(state)
	p.ProgressRun = run
	return p, nil
}

// PutProject writes every field except progress (see projectWriteCols): on a new project this
// leaves progress at its schema defaults (unreported); on an existing one it leaves whatever
// SaveProjectProgress last wrote untouched, whatever p.Progress* happens to hold.
func (s *Store) PutProject(p model.Project) error {
	_, err := s.db.Exec(upsert("projects", projectWriteCols),
		p.ID, p.Name, p.Slug, p.Folder, p.Brief, p.LeadID, p.Status, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetProject(id string) (model.Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListProjects() ([]model.Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

func (s *Store) PutProjectContribution(c model.ProjectContribution) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO project_contributions(project_id,agent_id,data) VALUES (?,?,?)
		ON CONFLICT(project_id,agent_id) DO UPDATE SET data=excluded.data`, c.ProjectID, c.AgentID, string(data))
	return err
}

func (s *Store) ProjectContributions(project string) ([]model.ProjectContribution, error) {
	rows, err := s.db.Query(`SELECT data FROM project_contributions WHERE project_id=? ORDER BY agent_id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProjectContribution{}
	for rows.Next() {
		var data string
		var c model.ProjectContribution
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// progressFieldsEqual reports whether a and b's stored progress fields are identical — the
// "nothing to do" case, same as the Python reference comparing STORED fields before even
// looking at versions.
func progressFieldsEqual(a, b model.Project) bool {
	if (a.Progress == nil) != (b.Progress == nil) {
		return false
	}
	if a.Progress != nil && *a.Progress != *b.Progress {
		return false
	}
	return a.ProgressState == b.ProgressState && a.ProgressVersion == b.ProgressVersion &&
		a.ProgressUpdatedAt == b.ProgressUpdatedAt && a.ProgressRun == b.ProgressRun &&
		reflect.DeepEqual(a.ProgressBasis, b.ProgressBasis)
}

// SaveProjectProgress persists p's progress fields and (if they actually changed) a history
// sample, atomically: both commit together or neither does. If p's stored fields already
// exactly match what's in the database (the no-op result of UpdateProgress, or an idempotent
// retry), this succeeds without writing anything. Otherwise p.ProgressVersion must be exactly
// one past the version currently stored for p.ID, or this returns model.ErrProgressConflict —
// a second, authoritative check beyond whatever model.UpdateProgress already did against the
// (possibly now-stale) record its caller read, so two writers racing from the same version can
// never both win: an equal version number alone does not make a write safe, since the second
// writer's content still differs from what the first already committed.
//
// Mirrors the Python reference's _save_progress_record: latest checklist, sample history and
// its retention (bounded to progressHistoryKeep per project, pruned past progressHistoryMaxAge)
// commit as one transaction.
func (s *Store) SaveProjectProgress(p model.Project) error {
	basis, err := json.Marshal(p.ProgressBasis)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := scanProject(tx.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id = ?`, p.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if progressFieldsEqual(old, p) {
		return tx.Commit() // identical to what's already stored: nothing to persist
	}
	if p.ProgressVersion != old.ProgressVersion+1 {
		return model.ErrProgressConflict
	}
	var percent any
	if p.Progress != nil {
		percent = *p.Progress
	}
	if _, err = tx.Exec(`UPDATE projects SET progress=?, progress_basis=?, progress_state=?,
		progress_version=?, progress_updated_at=?, progress_run=? WHERE id=?`,
		percent, string(basis), p.ProgressState, p.ProgressVersion, p.ProgressUpdatedAt, p.ProgressRun, p.ID); err != nil {
		return err
	}
	if p.Progress == nil {
		if _, err = tx.Exec(`DELETE FROM progress_samples WHERE project_id=?`, p.ID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.Exec(`INSERT INTO progress_samples(project_id,version,at,run,percent,state) VALUES (?,?,?,?,?,?)`,
		p.ID, p.ProgressVersion, p.ProgressUpdatedAt, p.ProgressRun, *p.Progress, p.ProgressState); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM progress_samples WHERE project_id=? AND version NOT IN
		(SELECT version FROM progress_samples WHERE project_id=? ORDER BY version DESC LIMIT ?)`,
		p.ID, p.ID, progressHistoryKeep); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM progress_samples WHERE at < ?`,
		time.Now().Add(-progressHistoryMaxAge).UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// ProjectProgressSamples returns p's progress history (oldest first), for model.ViewProgress's
// ETA. Pass to ViewProgress unfiltered; it only uses the samples matching the project's
// current progress_run.
func (s *Store) ProjectProgressSamples(projectID string) ([]model.ProgressSample, error) {
	rows, err := s.db.Query(`SELECT version, at, run, percent FROM progress_samples
		WHERE project_id = ? ORDER BY at, version`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProgressSample
	for rows.Next() {
		var sm model.ProgressSample
		if err := rows.Scan(&sm.Version, &sm.At, &sm.Run, &sm.Percent); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// PruneProgress deletes progress history older than progressHistoryMaxAge. Retention also
// applies when no project updates its progress, same as the Python reference's prune_progress.
func (s *Store) PruneProgress() error {
	_, err := s.db.Exec(`DELETE FROM progress_samples WHERE at < ?`,
		time.Now().Add(-progressHistoryMaxAge).UnixMilli())
	return err
}

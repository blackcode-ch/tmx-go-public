package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ---- recordings (DESIGN.md "Recorder"). Segment files are stored by name only (seg-0001.mp4);
// the recorder service turns them into absolute paths inside the recording's folder.

const recordingCols = `id, title, kind, state, mime, mic, bytes, duration_ms, resumed_at, error, started_at, ended_at, updated_at,
	system_audio, source, sensitivity`

func scanRecording(r scanner) (model.Recording, error) {
	var x model.Recording
	var mic, sys int
	err := r.Scan(&x.ID, &x.Title, &x.Kind, &x.State, &x.Mime, &mic, &x.Bytes, &x.DurationMS, &x.ResumedAt, &x.Error,
		&x.StartedAt, &x.EndedAt, &x.UpdatedAt, &sys, &x.Source, &x.Sensitivity)
	x.Mic, x.SystemAudio = mic != 0, sys != 0
	return x, err
}

// InsertRecording stores a new recording (its segments separately).
func (s *Store) InsertRecording(x model.Recording) error {
	_, err := s.db.Exec(`INSERT INTO recordings (`+recordingCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.ID, x.Title, x.Kind, x.State, x.Mime, boolInt(x.Mic), x.Bytes, x.DurationMS, x.ResumedAt, x.Error,
		x.StartedAt, x.EndedAt, x.UpdatedAt, boolInt(x.SystemAudio), recordingSource(x.Source), x.Sensitivity)
	return err
}

// UpdateRecording writes everything but the id, kind and start time.
func (s *Store) UpdateRecording(x model.Recording) error {
	res, err := s.db.Exec(`UPDATE recordings SET title = ?, state = ?, mime = ?, mic = ?, bytes = ?, duration_ms = ?,
		resumed_at = ?, error = ?, ended_at = ?, updated_at = ?, system_audio = ?, sensitivity = ? WHERE id = ?`,
		x.Title, x.State, x.Mime, boolInt(x.Mic), x.Bytes, x.DurationMS, x.ResumedAt, x.Error, x.EndedAt, x.UpdatedAt,
		boolInt(x.SystemAudio), x.Sensitivity, x.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRecording returns one recording with its segments.
func (s *Store) GetRecording(id string) (model.Recording, error) {
	x, err := scanRecording(s.db.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	list := []model.Recording{x}
	if err := s.withSegments(list); err != nil {
		return x, err
	}
	return list[0], nil
}

// ListRecordings returns recordings newest first (limit <= 0: all), with their segments.
func (s *Store) ListRecordings(limit int) ([]model.Recording, error) {
	q := `SELECT ` + recordingCols + ` FROM recordings ORDER BY started_at DESC, id`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.queryRecordings(q, args...)
}

// LiveRecordings returns the recordings still being captured (recording or paused).
func (s *Store) LiveRecordings() ([]model.Recording, error) {
	return s.queryRecordings(`SELECT `+recordingCols+` FROM recordings WHERE state IN (?, ?) ORDER BY started_at DESC`,
		model.RecordingActive, model.RecordingPaused)
}

func (s *Store) queryRecordings(q string, args ...any) ([]model.Recording, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var out []model.Recording
	for rows.Next() {
		x, err := scanRecording(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.withSegments(out)
}

const segmentCols = `recording_id, n, file, bytes, wav, wav_bytes, duration_ms, sha256, started_at`

// withSegments fills the Segments of list, in order.
func (s *Store) withSegments(list []model.Recording) error {
	if len(list) == 0 {
		return nil
	}
	ix := map[string]int{}
	args := make([]any, 0, len(list))
	for i := range list {
		ix[list[i].ID] = i
		list[i].Segments = []model.RecordingSegment{}
		args = append(args, list[i].ID)
	}
	rows, err := s.db.Query(`SELECT `+segmentCols+` FROM recording_segments WHERE recording_id IN (?`+
		strings.Repeat(",?", len(list)-1)+`) ORDER BY recording_id, n`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var g model.RecordingSegment
		if err := rows.Scan(&id, &g.N, &g.File, &g.Bytes, &g.WAV, &g.WAVBytes, &g.DurationMS, &g.SHA256, &g.StartedAt); err != nil {
			return err
		}
		if i, ok := ix[id]; ok {
			list[i].Segments = append(list[i].Segments, g)
		}
	}
	return rows.Err()
}

// PutRecordingSegment inserts or replaces one segment of a recording.
func (s *Store) PutRecordingSegment(id string, g model.RecordingSegment) error {
	_, err := s.db.Exec(`INSERT INTO recording_segments (`+segmentCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(recording_id, n) DO UPDATE SET file = excluded.file, bytes = excluded.bytes, wav = excluded.wav,
		wav_bytes = excluded.wav_bytes, duration_ms = excluded.duration_ms, sha256 = excluded.sha256`,
		id, g.N, g.File, g.Bytes, g.WAV, g.WAVBytes, g.DurationMS, g.SHA256, g.StartedAt)
	return err
}

// recordingSource is how a recording's source is stored: "" is web.
func recordingSource(s model.RecordingSource) model.RecordingSource {
	if s == "" {
		return model.RecordingSourceWeb
	}
	return s
}

// DeleteRecording removes a recording and its segments' rows (not the files).
func (s *Store) DeleteRecording(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recording_segments WHERE recording_id = ?`, id); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recordings WHERE id = ?`, id); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

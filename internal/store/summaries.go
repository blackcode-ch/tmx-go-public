package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ---- summaries of recordings (DESIGN.md "Summaries"): one row per recording, the lists as
// JSON arrays. The recording's row and files are the recorder's; this is only what Claude wrote.

const summaryCols = `recording_id, state, model, summary, key_points, decisions, action_items, chapters, transcripts,
	frames, parts, note, error, requested, created_at, started_at, finished_at, updated_at`

func scanSummary(r scanner) (model.RecordingSummary, error) {
	var x model.RecordingSummary
	var kp, dec, act, ch string
	var req int
	err := r.Scan(&x.RecordingID, &x.State, &x.Model, &x.Summary, &kp, &dec, &act, &ch, &x.Transcripts,
		&x.Frames, &x.Parts, &x.Note, &x.Error, &req, &x.CreatedAt, &x.StartedAt, &x.FinishedAt, &x.UpdatedAt)
	if err != nil {
		return x, err
	}
	x.Requested = req != 0
	_ = json.Unmarshal([]byte(kp), &x.KeyPoints)
	_ = json.Unmarshal([]byte(dec), &x.Decisions)
	_ = json.Unmarshal([]byte(act), &x.ActionItems)
	_ = json.Unmarshal([]byte(ch), &x.Chapters)
	FillSummaryLists(&x)
	return x, nil
}

// FillSummaryLists makes the summary's nil lists empty (never null in JSON).
func FillSummaryLists(x *model.RecordingSummary) {
	if x.KeyPoints == nil {
		x.KeyPoints = []string{}
	}
	if x.Decisions == nil {
		x.Decisions = []string{}
	}
	if x.ActionItems == nil {
		x.ActionItems = []model.SummaryItem{}
	}
	if x.Chapters == nil {
		x.Chapters = []model.SummaryChapter{}
	}
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// PutSummary inserts or replaces a recording's summary.
func (s *Store) PutSummary(x model.RecordingSummary) error {
	FillSummaryLists(&x)
	_, err := s.db.Exec(`INSERT INTO recording_summaries (`+summaryCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(recording_id) DO UPDATE SET state = excluded.state, model = excluded.model, summary = excluded.summary,
		key_points = excluded.key_points, decisions = excluded.decisions, action_items = excluded.action_items,
		chapters = excluded.chapters, transcripts = excluded.transcripts, frames = excluded.frames, parts = excluded.parts,
		note = excluded.note, error = excluded.error, requested = excluded.requested, started_at = excluded.started_at,
		finished_at = excluded.finished_at, updated_at = excluded.updated_at`,
		x.RecordingID, string(x.State), x.Model, x.Summary, jsonText(x.KeyPoints), jsonText(x.Decisions), jsonText(x.ActionItems),
		jsonText(x.Chapters), x.Transcripts, x.Frames, x.Parts, x.Note, x.Error, boolInt(x.Requested), x.CreatedAt, x.StartedAt,
		x.FinishedAt, x.UpdatedAt)
	return err
}

// GetSummary returns a recording's summary (ErrNotFound: none).
func (s *Store) GetSummary(recordingID string) (model.RecordingSummary, error) {
	x, err := scanSummary(s.db.QueryRow(`SELECT `+summaryCols+` FROM recording_summaries WHERE recording_id = ?`, recordingID))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// SummariesIn returns the summaries in one of the states, least recently changed first.
func (s *Store) SummariesIn(states ...model.SummaryState) ([]model.RecordingSummary, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = string(st)
	}
	rows, err := s.db.Query(`SELECT `+summaryCols+` FROM recording_summaries WHERE state IN (?`+strings.Repeat(",?", len(states)-1)+
		`) ORDER BY updated_at, recording_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.RecordingSummary
	for rows.Next() {
		x, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SummaryBrief is what a list of recordings shows of each one's summary.
type SummaryBrief struct {
	State   model.SummaryState
	Summary string
}

// SummaryBriefs returns the state and text of the summaries of the recordings ids (those that
// have one).
func (s *Store) SummaryBriefs(ids []string) (map[string]SummaryBrief, error) {
	out := map[string]SummaryBrief{}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		part := ids[:n]
		ids = ids[n:]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT recording_id, state, summary FROM recording_summaries WHERE recording_id IN (?`+
			strings.Repeat(",?", len(part)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var b SummaryBrief
			if err := rows.Scan(&id, &b.State, &b.Summary); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = b
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteSummary removes a recording's summary (none is fine).
func (s *Store) DeleteSummary(recordingID string) error {
	_, err := s.db.Exec(`DELETE FROM recording_summaries WHERE recording_id = ?`, recordingID)
	return err
}

// DeleteOrphanSummaries removes the summaries whose recording is gone and returns how many.
func (s *Store) DeleteOrphanSummaries() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM recording_summaries WHERE recording_id NOT IN (SELECT id FROM recordings)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

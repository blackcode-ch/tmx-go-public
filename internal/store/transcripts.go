package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Voice transcripts (DESIGN.md "Voice"). One row per source; the text is indexed by FTS5
// (transcripts_fts, kept in step by triggers), so search finds words regardless of accents
// and case. A query with no words falls back to a plain substring match.

const transcriptCols = `id, source, state, model, language, text, segments, error, created_at, updated_at`

func scanTranscript(r scanner) (model.Transcript, error) {
	var t model.Transcript
	var segs string
	err := r.Scan(&t.ID, &t.Source, &t.State, &t.Model, &t.Language, &t.Text, &segs, &t.Error, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	if json.Unmarshal([]byte(segs), &t.Segments) != nil || t.Segments == nil {
		t.Segments = []model.TranscriptSegment{}
	}
	return t, nil
}

// PutTranscript inserts or replaces a transcript (by id); audio is the file it is made from
// (kept so a job cut off by a restart can run again; never in the API).
func (s *Store) PutTranscript(t model.Transcript, audio string) error {
	segs := t.Segments
	if segs == nil {
		segs = []model.TranscriptSegment{}
	}
	b, err := json.Marshal(segs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO transcripts (`+transcriptCols+`, audio) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET source = excluded.source, state = excluded.state, model = excluded.model,
		language = excluded.language, text = excluded.text, segments = excluded.segments, error = excluded.error,
		audio = excluded.audio, updated_at = excluded.updated_at`,
		t.ID, t.Source, string(t.State), t.Model, t.Language, t.Text, string(b), t.Error, t.CreatedAt, t.UpdatedAt, audio)
	return err
}

func (s *Store) GetTranscript(id string) (model.Transcript, error) {
	t, err := scanTranscript(s.db.QueryRow(`SELECT `+transcriptCols+` FROM transcripts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// TranscriptBySource returns the transcript of source (ErrNotFound: none yet).
func (s *Store) TranscriptBySource(source string) (model.Transcript, error) {
	t, err := scanTranscript(s.db.QueryRow(`SELECT `+transcriptCols+` FROM transcripts WHERE source = ?`, source))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// UnfinishedTranscript is a transcript still queued or running, with its audio file.
type UnfinishedTranscript struct {
	Transcript model.Transcript
	Audio      string
}

// UnfinishedTranscripts lists the queued and running transcripts, oldest first (at start they
// are what a restart cut off).
func (s *Store) UnfinishedTranscripts() ([]UnfinishedTranscript, error) {
	rows, err := s.db.Query(`SELECT `+transcriptCols+`, audio FROM transcripts WHERE state IN (?, ?) ORDER BY created_at, n`,
		string(model.TranscriptQueued), string(model.TranscriptRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnfinishedTranscript
	for rows.Next() {
		var u UnfinishedTranscript
		var segs string
		t := &u.Transcript
		if err := rows.Scan(&t.ID, &t.Source, &t.State, &t.Model, &t.Language, &t.Text, &segs, &t.Error, &t.CreatedAt, &t.UpdatedAt, &u.Audio); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(segs), &t.Segments) != nil || t.Segments == nil {
			t.Segments = []model.TranscriptSegment{}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TranscriptQuery filters ListTranscripts. Q: words that must all occur (a word may be the
// start of a longer one); SourcePrefix: e.g. "recording:" or "recording:<id>/".
type TranscriptQuery struct {
	Q            string
	SourcePrefix string
	Limit        int // default 50, at most 500
}

// ListTranscripts returns the matching transcripts, newest first.
func (s *Store) ListTranscripts(q TranscriptQuery) ([]model.Transcript, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	var where []string
	var args []any
	if q.SourcePrefix != "" {
		where = append(where, `substr(source, 1, ?) = ?`)
		args = append(args, len(q.SourcePrefix), q.SourcePrefix)
	}
	query := strings.TrimSpace(q.Q)
	match := FTSQuery(query)
	var sqlText string
	switch {
	case match != "":
		where = append(where, `n IN (SELECT rowid FROM transcripts_fts WHERE transcripts_fts MATCH ?)`)
		args = append(args, match)
	case query != "":
		where = append(where, `text LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(query)+"%")
	}
	sqlText = `SELECT ` + transcriptCols + ` FROM transcripts`
	if len(where) > 0 {
		sqlText += ` WHERE ` + strings.Join(where, " AND ")
	}
	sqlText += ` ORDER BY created_at DESC, n DESC LIMIT ?`
	args = append(args, q.Limit)
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Transcript{}
	for rows.Next() {
		t, err := scanTranscript(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTranscript removes a transcript (ErrNotFound: there is none).
func (s *Store) DeleteTranscript(id string) error {
	res, err := s.db.Exec(`DELETE FROM transcripts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FTSQuery turns what someone typed into an FTS5 query: each word quoted (so no FTS syntax
// gets through) and matched as a prefix, all of them required. "" = no words in it.
func FTSQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > 16 {
		words = words[:16]
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " ")
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

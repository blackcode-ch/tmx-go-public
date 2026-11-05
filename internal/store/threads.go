package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const threadCols = `id, title, kind, project_id, chat_id, color, archived, created_at, updated_at, position,
	parent_id, status, lifecycle, owner, story, intent_text, intent_source, canvas_x, canvas_y`

func scanThread(r scanner) (model.Thread, error) {
	var t model.Thread
	var archived int
	err := r.Scan(&t.ID, &t.Title, &t.Kind, &t.ProjectID, &t.ChatID, &t.Color, &archived, &t.CreatedAt, &t.UpdatedAt, &t.Position,
		&t.ParentID, &t.Status, &t.Lifecycle, &t.Owner, &t.Story, &t.IntentText, &t.IntentSource, &t.CanvasX, &t.CanvasY)
	t.Archived = archived != 0
	return t, err
}

func (s *Store) PutThread(t model.Thread) error {
	_, err := s.db.Exec(upsert("threads", threadCols),
		t.ID, t.Title, t.Kind, t.ProjectID, t.ChatID, t.Color, boolInt(t.Archived), t.CreatedAt, t.UpdatedAt, t.Position,
		t.ParentID, t.Status, t.Lifecycle, t.Owner, t.Story, t.IntentText, t.IntentSource, t.CanvasX, t.CanvasY)
	return err
}

func (s *Store) GetThread(id string) (model.Thread, error) {
	t, err := scanThread(s.db.QueryRow(`SELECT `+threadCols+` FROM threads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) ListThreads() ([]model.Thread, error) {
	rows, err := s.db.Query(`SELECT ` + threadCols + ` FROM threads ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteThread removes a thread; its entries and the mail waiting in it go to General, its open
// links end (they stay, under the old thread's ID, as history).
func (s *Store) DeleteThread(id string, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE timeline SET thread_id = 'general' WHERE thread_id = ?`,
		`UPDATE messages SET thread_id = '' WHERE thread_id = ?`,
		`UPDATE approvals SET thread_id = '' WHERE thread_id = ?`,
		`UPDATE thread_entries SET thread_id = 'general' WHERE thread_id = ?`,
		`DELETE FROM threads WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE thread_links SET ended_at = ? WHERE thread_id = ? AND ended_at = 0`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetTimelineThread files an agent's entries from..to (to <= 0: to the end) under a thread.
func (s *Store) SetTimelineThread(agentID string, from, to int64, thread string) error {
	if to <= 0 {
		_, err := s.db.Exec(`UPDATE timeline SET thread_id = ? WHERE agent_id = ? AND seq >= ?`, thread, agentID, from)
		return err
	}
	_, err := s.db.Exec(`UPDATE timeline SET thread_id = ? WHERE agent_id = ? AND seq BETWEEN ? AND ?`, thread, agentID, from, to)
	return err
}

// boardTools are the tools whose input a board card summarises.
var boardTools = []any{"mcp__tmxg__send", "mcp__tmxg__report", "mcp__tmxg__reply", "mcp__tmxg__agent_launch",
	"mcp__tmxg__project_create", "mcp__tmxg__ask_owner", "mcp__tmxg__thread_set"}

// BoardEntries returns what the board needs of an agent's timeline from seq `from` on, in
// seq order: the entries that start, make up or end turns, with text cut to 2000 characters
// and tool input only for the tools a card summarises.
func (s *Store) BoardEntries(agentID string, from int64) ([]model.TimelineEntry, error) {
	args := append([]any{}, boardTools...)
	args = append(args, agentID, from, model.EntryUser, model.EntryMessageIn, model.EntryAssistant, model.EntryToolUse,
		model.EntryResult, model.EntryError)
	rows, err := s.db.Query(`SELECT seq, at, kind, substr(text, 1, 2000), tool,
		CASE WHEN tool IN (?,?,?,?,?,?,?) THEN substr(input, 1, 4000) END, is_error, meta, thread_id
		FROM timeline WHERE agent_id = ? AND seq >= ? AND kind IN (?,?,?,?,?,?) ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TimelineEntry
	for rows.Next() {
		e := model.TimelineEntry{AgentID: agentID}
		var input, meta sql.NullString
		var isErr int
		if err := rows.Scan(&e.Seq, &e.At, &e.Kind, &e.Text, &e.Tool, &input, &isErr, &meta, &e.ThreadID); err != nil {
			return nil, err
		}
		e.IsError = isErr != 0
		if input.Valid && input.String != "" {
			e.Input = json.RawMessage(input.String)
		}
		if meta.Valid && meta.String != "" {
			_ = json.Unmarshal([]byte(meta.String), &e.Meta)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FillTimelineThread files the entries from..to (to <= 0: to the end) that have no thread yet.
func (s *Store) FillTimelineThread(agentID string, from, to int64, thread string) error {
	if to <= 0 {
		to = 1 << 62
	}
	_, err := s.db.Exec(`UPDATE timeline SET thread_id = ? WHERE agent_id = ? AND seq BETWEEN ? AND ? AND thread_id = ''`,
		thread, agentID, from, to)
	return err
}

// LastThread is the thread of an agent's newest entry ("" if none).
func (s *Store) LastThread(agentID string) (string, error) {
	var t string
	err := s.db.QueryRow(`SELECT thread_id FROM timeline WHERE agent_id = ? ORDER BY seq DESC LIMIT 1`, agentID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return t, err
}

// HasUnthreaded reports whether an agent has entries filed under no thread.
func (s *Store) HasUnthreaded(agentID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM timeline WHERE agent_id = ? AND thread_id = '' LIMIT 1)`, agentID).Scan(&n)
	return n > 0, err
}

// SetThreadOrder gives the threads positions 1..n in the order given; the others keep theirs.
func (s *Store) SetThreadOrder(ids []string, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE threads SET position = ?, updated_at = ? WHERE id = ?`, i+1, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const entryCols = `id, thread_id, kind, text, link, card, by_name, by_id, at, corrects`

func scanEntry(r scanner) (model.ThreadEntry, error) {
	var e model.ThreadEntry
	err := r.Scan(&e.ID, &e.ThreadID, &e.Kind, &e.Text, &e.Link, &e.Card, &e.By, &e.ByID, &e.At, &e.Corrects)
	return e, err
}

// AddEntry stores a log entry unless one with its ID exists; it reports whether it was new.
func (s *Store) AddEntry(e model.ThreadEntry) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO thread_entries (`+entryCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ThreadID, e.Kind, e.Text, e.Link, e.Card, e.By, e.ByID, e.At, e.Corrects)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) GetEntry(id string) (model.ThreadEntry, error) {
	e, err := scanEntry(s.db.QueryRow(`SELECT `+entryCols+` FROM thread_entries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// Entries lists a thread's log oldest first ("" = every thread's); entries of the same
// millisecond in the order they were filed (rowid: their IDs are hashes).
func (s *Store) Entries(threadID string) ([]model.ThreadEntry, error) {
	q, args := `SELECT `+entryCols+` FROM thread_entries ORDER BY at, rowid`, []any{}
	if threadID != "" {
		q, args = `SELECT `+entryCols+` FROM thread_entries WHERE thread_id = ? ORDER BY at, rowid`, []any{threadID}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ThreadEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

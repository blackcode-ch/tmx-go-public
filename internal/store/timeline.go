package store

import (
	"database/sql"
	"encoding/json"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const timelineCols = `agent_id, seq, at, kind, text, tool, tool_use_id, input, is_error, meta, thread_id`

// trimEvery: the timeline is trimmed to TimelineKeep every this many appends per agent.
const trimEvery = 100

// AppendTimeline stores an entry whose Seq/AgentID/At the caller has assigned, and trims
// the agent's timeline to the last TimelineKeep entries now and then.
func (s *Store) AppendTimeline(e model.TimelineEntry) error {
	var input, meta any
	if len(e.Input) > 0 {
		input = string(e.Input)
	}
	if len(e.Meta) > 0 {
		b, err := json.Marshal(e.Meta)
		if err != nil {
			return err
		}
		meta = string(b)
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO timeline (`+timelineCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.AgentID, e.Seq, e.At, e.Kind, e.Text, e.Tool, e.ToolUseID, input, boolInt(e.IsError), meta, e.ThreadID)
	if err != nil {
		return err
	}
	if e.Seq%trimEvery == 0 {
		return s.TrimTimeline(e.AgentID, TimelineKeep)
	}
	return nil
}

// TrimTimeline keeps the newest keep entries of an agent.
func (s *Store) TrimTimeline(agentID string, keep int) error {
	_, err := s.db.Exec(`DELETE FROM timeline WHERE agent_id = ? AND seq <=
		(SELECT COALESCE(MAX(seq), 0) FROM timeline WHERE agent_id = ?) - ?`, agentID, agentID, keep)
	return err
}

// LastSeq returns the highest seq stored for an agent (0 if none).
func (s *Store) LastSeq(agentID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM timeline WHERE agent_id = ?`, agentID).Scan(&n)
	return n, err
}

// Timeline returns entries in seq order: those after `after` (first limit of them), or,
// with after <= 0, the last limit entries. limit <= 0 means 200.
func (s *Store) Timeline(agentID string, after int64, limit int) ([]model.TimelineEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	if after > 0 {
		rows, err = s.db.Query(`SELECT `+timelineCols+` FROM timeline WHERE agent_id = ? AND seq > ?
			ORDER BY seq LIMIT ?`, agentID, after, limit)
	} else {
		rows, err = s.db.Query(`SELECT * FROM (SELECT `+timelineCols+` FROM timeline WHERE agent_id = ?
			ORDER BY seq DESC LIMIT ?) ORDER BY seq`, agentID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TimelineEntry{}
	for rows.Next() {
		var e model.TimelineEntry
		var input, meta sql.NullString
		var isErr int
		if err := rows.Scan(&e.AgentID, &e.Seq, &e.At, &e.Kind, &e.Text, &e.Tool, &e.ToolUseID,
			&input, &isErr, &meta, &e.ThreadID); err != nil {
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

// HasConversation reports whether the agent's timeline shows the harness ever answered
// (so its session exists and can be resumed).
func (s *Store) HasConversation(agentID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM timeline WHERE agent_id = ? AND kind IN (?,?,?,?) LIMIT 1)`,
		agentID, model.EntryAssistant, model.EntryResult, model.EntryToolUse, model.EntryThinking).Scan(&n)
	return n > 0, err
}

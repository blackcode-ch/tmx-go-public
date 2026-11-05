package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Chat boards (schema 8; DESIGN.md "Chat boards"): topics, the topic of each message, the
// topics' logs and this owner's order of a board's strings. The chat layer merges changes
// (last writer wins); this file only stores them.

// ---- topics

const chatTopicCols = `chat_id, id, title, title_at, title_by, color, color_at, color_by, archived, archived_at, archived_by, created_by, created_at`

func scanChatTopic(r scanner) (model.ChatTopic, error) {
	var t model.ChatTopic
	var archived int
	err := r.Scan(&t.ChatID, &t.ID, &t.Title, &t.TitleAt, &t.TitleBy, &t.Color, &t.ColorAt, &t.ColorBy,
		&archived, &t.ArchivedAt, &t.ArchivedBy, &t.CreatedBy, &t.CreatedAt)
	t.Archived = archived != 0
	t.Kind = "topic"
	t.UpdatedAt = max(t.CreatedAt, t.TitleAt, t.ColorAt, t.ArchivedAt)
	return t, err
}

// PutChatTopic stores a topic with its clocks (insert or replace).
func (s *Store) PutChatTopic(t model.ChatTopic) error {
	_, err := s.db.Exec(`INSERT INTO chat_topics (`+chatTopicCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(chat_id, id) DO UPDATE SET title = excluded.title, title_at = excluded.title_at,
		title_by = excluded.title_by, color = excluded.color, color_at = excluded.color_at, color_by = excluded.color_by,
		archived = excluded.archived, archived_at = excluded.archived_at, archived_by = excluded.archived_by`,
		t.ChatID, t.ID, t.Title, t.TitleAt, t.TitleBy, t.Color, t.ColorAt, t.ColorBy, boolInt(t.Archived), t.ArchivedAt,
		t.ArchivedBy, t.CreatedBy, t.CreatedAt)
	return err
}

func (s *Store) GetChatTopic(chatID, id string) (model.ChatTopic, error) {
	t, err := scanChatTopic(s.db.QueryRow(`SELECT `+chatTopicCols+` FROM chat_topics WHERE chat_id = ? AND id = ?`, chatID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// ChatTopics lists a chat's topics (archived ones too), oldest first.
func (s *Store) ChatTopics(chatID string) ([]model.ChatTopic, error) {
	rows, err := s.db.Query(`SELECT `+chatTopicCols+` FROM chat_topics WHERE chat_id = ? ORDER BY created_at, id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ChatTopic{}
	for rows.Next() {
		t, err := scanChatTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CountChatTopics(chatID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_topics WHERE chat_id = ?`, chatID).Scan(&n)
	return n, err
}

// ---- the topic of each message (a register: the newest (at, by) wins)

// MessageTopic is a message's topic register (ErrNotFound when it has none).
func (s *Store) MessageTopic(chatID, messageID string) (topic string, at int64, by string, err error) {
	err = s.db.QueryRow(`SELECT topic_id, at, by_peer FROM chat_message_topics WHERE chat_id = ? AND message_id = ?`,
		chatID, messageID).Scan(&topic, &at, &by)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// SetMessageTopic files a message under a topic unless the register holds a newer change
// (later at; the same at and a greater peer ID). It reports whether it changed.
func (s *Store) SetMessageTopic(chatID, messageID, topic string, at int64, by string) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO chat_message_topics (chat_id, message_id, topic_id, at, by_peer) VALUES (?,?,?,?,?)
		ON CONFLICT(chat_id, message_id) DO UPDATE SET topic_id = excluded.topic_id, at = excluded.at, by_peer = excluded.by_peer
		WHERE excluded.at > chat_message_topics.at
			OR (excluded.at = chat_message_topics.at AND excluded.by_peer > chat_message_topics.by_peer)`,
		chatID, messageID, topic, at, by)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MessageTopics returns the topic of each of the given messages that has one.
func (s *Store) MessageTopics(chatID string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{chatID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT message_id, topic_id FROM chat_message_topics WHERE chat_id = ? AND message_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m, t string
		if err := rows.Scan(&m, &t); err != nil {
			return nil, err
		}
		out[m] = t
	}
	return out, rows.Err()
}

// TopicMessageCounts counts a chat's messages (not notices) per topic ("" = none).
func (s *Store) TopicMessageCounts(chatID string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT COALESCE(t.topic_id, ''), COUNT(*) FROM chat_messages m
		LEFT JOIN chat_message_topics t ON t.chat_id = m.chat_id AND t.message_id = m.id
		WHERE m.chat_id = ? AND m.sender != ? GROUP BY 1`, chatID, model.ChatSystem)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// plusScanner scans the extra columns after the ones its caller asks for.
type plusScanner struct {
	r     scanner
	extra []any
}

func (p plusScanner) Scan(dest ...any) error { return p.r.Scan(append(dest, p.extra...)...) }

// BoardMessages returns the newest perTopic messages (not notices) of each topic of a chat,
// oldest first, with Topic set.
func (s *Store) BoardMessages(chatID string, perTopic int) ([]model.ChatMessage, error) {
	inner := "m." + strings.ReplaceAll(chatMsgCols, ", ", ", m.")
	rows, err := s.db.Query(`SELECT `+chatMsgCols+`, topic FROM (
		SELECT `+inner+`, COALESCE(t.topic_id, '') AS topic,
			ROW_NUMBER() OVER (PARTITION BY COALESCE(t.topic_id, '') ORDER BY m.seq DESC) AS rn
		FROM chat_messages m LEFT JOIN chat_message_topics t ON t.chat_id = m.chat_id AND t.message_id = m.id
		WHERE m.chat_id = ? AND m.sender != ?
	) WHERE rn <= ? ORDER BY seq`, chatID, model.ChatSystem, perTopic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ChatMessage{}
	for rows.Next() {
		var topic string
		m, err := scanChatMessage(plusScanner{rows, []any{&topic}})
		if err != nil {
			return nil, err
		}
		m.Topic = topic
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- logs

const chatEntryCols = `chat_id, id, topic_id, kind, text, link, by_peer, by_name, by_agent, at, corrects`

func scanChatEntry(r scanner) (model.ChatTopicEntry, error) {
	var e model.ChatTopicEntry
	err := r.Scan(&e.ChatID, &e.ID, &e.TopicID, &e.Kind, &e.Text, &e.Link, &e.By, &e.ByName, &e.ByAgent, &e.At, &e.Corrects)
	return e, err
}

// AddChatTopicEntry stores a log entry unless the chat has one with its ID; it reports
// whether it was new.
func (s *Store) AddChatTopicEntry(e model.ChatTopicEntry) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO chat_topic_entries (`+chatEntryCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.ChatID, e.ID, e.TopicID, e.Kind, e.Text, e.Link, e.By, e.ByName, e.ByAgent, e.At, e.Corrects)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) GetChatTopicEntry(chatID, id string) (model.ChatTopicEntry, error) {
	e, err := scanChatEntry(s.db.QueryRow(`SELECT `+chatEntryCols+` FROM chat_topic_entries WHERE chat_id = ? AND id = ?`, chatID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// ChatTopicEntries lists a chat's log entries (every topic), oldest first.
func (s *Store) ChatTopicEntries(chatID string) ([]model.ChatTopicEntry, error) {
	rows, err := s.db.Query(`SELECT `+chatEntryCols+` FROM chat_topic_entries WHERE chat_id = ? ORDER BY at, id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ChatTopicEntry{}
	for rows.Next() {
		e, err := scanChatEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CountChatTopicEntries(chatID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_topic_entries WHERE chat_id = ?`, chatID).Scan(&n)
	return n, err
}

// ---- this owner's order of a board's strings (never sent to anyone)

// ChatBoardOrder is the saved order of a chat's topics, most urgent first (nil if none).
func (s *Store) ChatBoardOrder(chatID string) ([]string, error) {
	var raw string
	err := s.db.QueryRow(`SELECT topic_order FROM chat_boards WHERE chat_id = ?`, chatID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, nil // unreadable: as if never ordered
	}
	return ids, nil
}

func (s *Store) SetChatBoardOrder(chatID string, ids []string, now int64) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO chat_boards (chat_id, topic_order, updated_at) VALUES (?,?,?)
		ON CONFLICT(chat_id) DO UPDATE SET topic_order = excluded.topic_order, updated_at = excluded.updated_at`,
		chatID, string(b), now)
	return err
}

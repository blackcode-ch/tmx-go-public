package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ---- chat files (DESIGN.md "Chats": files)

const chatFileCols = `chat_id, id, message_id, sender, name, type, size, sha256, state, error, tries, next_at, created_at`

func scanChatFile(r scanner) (model.ChatFile, error) {
	var f model.ChatFile
	err := r.Scan(&f.ChatID, &f.ID, &f.MessageID, &f.Sender, &f.Name, &f.Type, &f.Size, &f.SHA256, &f.State,
		&f.Error, &f.Tries, &f.NextAt, &f.CreatedAt)
	return f, err
}

// InsertChatFile stores f unless (chat, id) exists; it reports whether it was new.
func (s *Store) InsertChatFile(f model.ChatFile) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO chat_files (`+chatFileCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ChatID, f.ID, f.MessageID, f.Sender, f.Name, f.Type, f.Size, f.SHA256, f.State, f.Error, f.Tries, f.NextAt, f.CreatedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) GetChatFile(chatID, id string) (model.ChatFile, error) {
	f, err := scanChatFile(s.db.QueryRow(`SELECT `+chatFileCols+` FROM chat_files WHERE chat_id = ? AND id = ?`, chatID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// UpdateChatFile writes a file's message, state, error and retry fields.
func (s *Store) UpdateChatFile(f model.ChatFile) error {
	_, err := s.db.Exec(`UPDATE chat_files SET message_id = ?, state = ?, error = ?, tries = ?, next_at = ?
		WHERE chat_id = ? AND id = ?`, f.MessageID, f.State, f.Error, f.Tries, f.NextAt, f.ChatID, f.ID)
	return err
}

// ChatFilesOf returns the files of messages (by message ID), in upload order.
func (s *Store) ChatFilesOf(chatID string, messageIDs []string) (map[string][]model.ChatFile, error) {
	out := map[string][]model.ChatFile{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	args := []any{chatID}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT `+chatFileCols+` FROM chat_files WHERE chat_id = ? AND message_id IN (?`+
		strings.Repeat(",?", len(messageIDs)-1)+`) ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanChatFile(rows)
		if err != nil {
			return nil, err
		}
		out[f.MessageID] = append(out[f.MessageID], f)
	}
	return out, rows.Err()
}

// DueChatFiles lists pending files whose next attempt is due (next_at <= now).
func (s *Store) DueChatFiles(now int64, limit int) ([]model.ChatFile, error) {
	rows, err := s.db.Query(`SELECT `+chatFileCols+` FROM chat_files WHERE state = ? AND message_id != '' AND next_at <= ?
		ORDER BY next_at, created_at LIMIT ?`, model.ChatFilePending, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ChatFile
	for rows.Next() {
		f, err := scanChatFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UnsentChatFiles lists uploads never attached to a message, created before the given time.
func (s *Store) UnsentChatFiles(before int64) ([]model.ChatFile, error) {
	rows, err := s.db.Query(`SELECT `+chatFileCols+` FROM chat_files WHERE message_id = '' AND created_at < ?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ChatFile
	for rows.Next() {
		f, err := scanChatFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) DeleteChatFile(chatID, id string) error {
	_, err := s.db.Exec(`DELETE FROM chat_files WHERE chat_id = ? AND id = ?`, chatID, id)
	return err
}

// ChatMentionsUnread counts unread messages (after readSeq) that @mention the owner.
func (s *Store) ChatMentionsUnread(chatID string, readSeq int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE chat_id = ? AND seq > ? AND mentions_me = 1`,
		chatID, readSeq).Scan(&n)
	return n, err
}

// ---- each member install's chat agent (learned from its messages; for @mentions)

// SetChatAgent records peerID's chat agent in a chat ("" removes it).
func (s *Store) SetChatAgent(chatID, peerID, agent string, at int64) error {
	if agent == "" {
		_, err := s.db.Exec(`DELETE FROM chat_agents WHERE chat_id = ? AND peer_id = ?`, chatID, peerID)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO chat_agents (chat_id, peer_id, agent, updated_at) VALUES (?,?,?,?)
		ON CONFLICT (chat_id, peer_id) DO UPDATE SET agent = excluded.agent, updated_at = excluded.updated_at`,
		chatID, peerID, agent, at)
	return err
}

// ChatAgents maps peer ID → chat agent name for a chat.
func (s *Store) ChatAgents(chatID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT peer_id, agent FROM chat_agents WHERE chat_id = ?`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, a string
		if err := rows.Scan(&p, &a); err != nil {
			return nil, err
		}
		out[p] = a
	}
	return out, rows.Err()
}

// RetryChatFilesFrom makes pending files announced by peerID due now (it came online).
func (s *Store) RetryChatFilesFrom(peerID string) (int64, error) {
	res, err := s.db.Exec(`UPDATE chat_files SET next_at = 0 WHERE state = ? AND sender = ?`, model.ChatFilePending, peerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

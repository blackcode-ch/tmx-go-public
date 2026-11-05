package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Chats between tmx installs (schema 3; DESIGN.md "Chats").

// ---- contacts

const contactCols = `peer_id, name, remote_name, note, safety_code, added_at, updated_at`

func scanContact(r scanner) (model.Contact, error) {
	var c model.Contact
	err := r.Scan(&c.PeerID, &c.Name, &c.RemoteName, &c.Note, &c.SafetyCode, &c.AddedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) PutContact(c model.Contact) error {
	_, err := s.db.Exec(`INSERT INTO contacts (`+contactCols+`) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(peer_id) DO UPDATE SET name = excluded.name, remote_name = excluded.remote_name,
		note = excluded.note, safety_code = excluded.safety_code, updated_at = excluded.updated_at`,
		c.PeerID, c.Name, c.RemoteName, c.Note, c.SafetyCode, c.AddedAt, c.UpdatedAt)
	return err
}

func (s *Store) GetContact(peerID string) (model.Contact, error) {
	c, err := scanContact(s.db.QueryRow(`SELECT `+contactCols+` FROM contacts WHERE peer_id = ?`, peerID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ListContacts() ([]model.Contact, error) {
	rows, err := s.db.Query(`SELECT ` + contactCols + ` FROM contacts ORDER BY name COLLATE NOCASE, peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteContact(peerID string) error {
	_, err := s.db.Exec(`DELETE FROM contacts WHERE peer_id = ?`, peerID)
	return err
}

// ---- chats

const chatCols = `id, kind, name, peer_id, created_by, agent_id, status, version, read_seq, created_at, updated_at, last_at`

func scanChat(r scanner) (model.Chat, error) {
	var c model.Chat
	err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.PeerID, &c.CreatedBy, &c.AgentID, &c.Status, &c.Version,
		&c.ReadSeq, &c.CreatedAt, &c.UpdatedAt, &c.LastAt)
	return c, err
}

func (s *Store) PutChat(c model.Chat) error {
	_, err := s.db.Exec(upsert("chats", chatCols), c.ID, c.Kind, c.Name, c.PeerID, c.CreatedBy, c.AgentID,
		c.Status, c.Version, c.ReadSeq, c.CreatedAt, c.UpdatedAt, c.LastAt)
	return err
}

func (s *Store) GetChat(id string) (model.Chat, error) {
	c, err := scanChat(s.db.QueryRow(`SELECT `+chatCols+` FROM chats WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListChats returns every chat, most recent activity first.
func (s *Store) ListChats() ([]model.Chat, error) {
	rows, err := s.db.Query(`SELECT ` + chatCols + ` FROM chats ORDER BY MAX(last_at, created_at) DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- members

const memberCols = `chat_id, peer_id, name, role, joined_at, left_at`

func (s *Store) PutChatMember(m model.ChatMember) error {
	_, err := s.db.Exec(`INSERT INTO chat_members (`+memberCols+`) VALUES (?,?,?,?,?,?)
		ON CONFLICT(chat_id, peer_id) DO UPDATE SET name = excluded.name, role = excluded.role,
		joined_at = excluded.joined_at, left_at = excluded.left_at`,
		m.ChatID, m.PeerID, m.Name, m.Role, m.JoinedAt, m.LeftAt)
	return err
}

// ChatMembers returns a chat's members, current ones first (by join time), then former ones.
func (s *Store) ChatMembers(chatID string) ([]model.ChatMember, error) {
	rows, err := s.db.Query(`SELECT `+memberCols+` FROM chat_members WHERE chat_id = ?
		ORDER BY left_at > 0, joined_at, peer_id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ChatMember{}
	for rows.Next() {
		var m model.ChatMember
		if err := rows.Scan(&m.ChatID, &m.PeerID, &m.Name, &m.Role, &m.JoinedAt, &m.LeftAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- messages

const chatMsgCols = `seq, chat_id, id, sender, sender_name, sender_agent, body, at, state, received_at, recipients, mentions, mentions_me`

func scanChatMessage(r scanner) (model.ChatMessage, error) {
	var m model.ChatMessage
	var mentions string
	err := r.Scan(&m.Seq, &m.ChatID, &m.ID, &m.Sender, &m.SenderName, &m.SenderAgent, &m.Body, &m.At,
		&m.State, &m.ReceivedAt, &m.Recipients, &mentions, &m.MentionsMe)
	if err == nil && mentions != "" {
		_ = json.Unmarshal([]byte(mentions), &m.Mentions)
	}
	return m, err
}

func mentionsJSON(ms []model.ChatMention) string {
	if len(ms) == 0 {
		return ""
	}
	b, _ := json.Marshal(ms)
	return string(b)
}

// InsertChatMessage stores m unless a message with the same (chat, id) exists; it reports
// whether it was new and returns the stored row (with seq).
func (s *Store) InsertChatMessage(m model.ChatMessage) (model.ChatMessage, bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO chat_messages
		(chat_id, id, sender, sender_name, sender_agent, body, at, state, received_at, recipients, mentions, mentions_me)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ChatID, m.ID, m.Sender, m.SenderName, m.SenderAgent, m.Body, m.At, m.State, m.ReceivedAt, m.Recipients,
		mentionsJSON(m.Mentions), m.MentionsMe)
	if err != nil {
		return m, false, err
	}
	n, _ := res.RowsAffected()
	got, err := s.GetChatMessage(m.ChatID, m.ID)
	return got, n > 0, err
}

func (s *Store) GetChatMessage(chatID, id string) (model.ChatMessage, error) {
	m, err := scanChatMessage(s.db.QueryRow(`SELECT `+chatMsgCols+` FROM chat_messages WHERE chat_id = ? AND id = ?`, chatID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err == nil {
		m.Delivered, err = s.CountReceipts(chatID, id)
	}
	return m, err
}

// SetChatMessageRecipients changes how many receipts an outgoing message waits for (a
// group member was removed before it got the message).
func (s *Store) SetChatMessageRecipients(chatID, id string, n int) error {
	_, err := s.db.Exec(`UPDATE chat_messages SET recipients = ? WHERE chat_id = ? AND id = ?`, n, chatID, id)
	return err
}

func (s *Store) SetChatMessageState(chatID, id string, st model.ChatMessageState) error {
	_, err := s.db.Exec(`UPDATE chat_messages SET state = ? WHERE chat_id = ? AND id = ?`, st, chatID, id)
	return err
}

// ChatMessages returns up to limit messages of a chat with seq < before (0 = newest),
// oldest first, and whether older ones exist.
func (s *Store) ChatMessages(chatID string, before int64, limit int) ([]model.ChatMessage, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + chatMsgCols + ` FROM chat_messages WHERE chat_id = ?`
	args := []any{chatID}
	if before > 0 {
		q += ` AND seq < ?`
		args = append(args, before)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	var out []model.ChatMessage
	for rows.Next() {
		m, err := scanChatMessage(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	for i := range out {
		if out[i].Sender == model.ChatMe {
			if out[i].Delivered, err = s.CountReceipts(chatID, out[i].ID); err != nil {
				return nil, false, err
			}
		}
	}
	if out == nil {
		out = []model.ChatMessage{}
	}
	return out, more, nil
}

// LastChatMessage returns a chat's newest message (ErrNotFound when it has none).
func (s *Store) LastChatMessage(chatID string) (model.ChatMessage, error) {
	m, err := scanChatMessage(s.db.QueryRow(`SELECT `+chatMsgCols+` FROM chat_messages WHERE chat_id = ?
		ORDER BY seq DESC LIMIT 1`, chatID))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// ChatUnread counts messages from others (not "me", not system notices) after readSeq.
func (s *Store) ChatUnread(chatID string, readSeq int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE chat_id = ? AND seq > ? AND sender NOT IN (?, ?)`,
		chatID, readSeq, model.ChatMe, model.ChatSystem).Scan(&n)
	return n, err
}

// MaxChatSeq is the newest seq of a chat (0 when empty).
func (s *Store) MaxChatSeq(chatID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM chat_messages WHERE chat_id = ?`, chatID).Scan(&n)
	return n, err
}

// TrailingAgentMessages counts the newest run of messages written by agents (on any side)
// with no person's message in between, looking at most limit messages back.
func (s *Store) TrailingAgentMessages(chatID string, limit int) (int, error) {
	rows, err := s.db.Query(`SELECT sender, sender_agent FROM chat_messages WHERE chat_id = ? ORDER BY seq DESC LIMIT ?`,
		chatID, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var sender, agent string
		if err := rows.Scan(&sender, &agent); err != nil {
			return 0, err
		}
		if sender == model.ChatSystem {
			continue
		}
		if agent == "" {
			break
		}
		n++
	}
	return n, rows.Err()
}

// CountSentBy counts messages of a sender in a chat received at or after since (ms).
func (s *Store) CountSentBy(chatID, sender string, since int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE chat_id = ? AND sender = ? AND received_at >= ?`,
		chatID, sender, since).Scan(&n)
	return n, err
}

// ---- receipts

// AddReceipt records that peerID stored a message; it reports whether it was new.
func (s *Store) AddReceipt(chatID, messageID, peerID string, at int64) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO chat_receipts (chat_id, message_id, peer_id, at) VALUES (?,?,?,?)`,
		chatID, messageID, peerID, at)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) CountReceipts(chatID, messageID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_receipts WHERE chat_id = ? AND message_id = ?`, chatID, messageID).Scan(&n)
	return n, err
}

// ---- outbox

// OutboxItem is one envelope waiting for (or being sent to) one peer.
type OutboxItem struct {
	Seq       int64
	PeerID    string
	ChatID    string
	MessageID string // the chat message it carries, if any ("" for receipts and group state)
	Envelope  []byte // JSON p2p.Envelope (unsigned)
	Attempts  int
	NextAt    int64
	CreatedAt int64
}

const outboxCols = `seq, peer_id, chat_id, message_id, envelope, attempts, next_at, created_at`

func scanOutbox(r scanner) (OutboxItem, error) {
	var o OutboxItem
	var env string
	err := r.Scan(&o.Seq, &o.PeerID, &o.ChatID, &o.MessageID, &env, &o.Attempts, &o.NextAt, &o.CreatedAt)
	o.Envelope = []byte(env)
	return o, err
}

func (s *Store) AddOutbox(o OutboxItem) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO chat_outbox (peer_id, chat_id, message_id, envelope, attempts, next_at, created_at)
		VALUES (?,?,?,?,?,?,?)`, o.PeerID, o.ChatID, o.MessageID, string(o.Envelope), o.Attempts, o.NextAt, o.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// NextOutbox returns the oldest item for a peer (ErrNotFound when none).
func (s *Store) NextOutbox(peerID string) (OutboxItem, error) {
	o, err := scanOutbox(s.db.QueryRow(`SELECT `+outboxCols+` FROM chat_outbox WHERE peer_id = ? ORDER BY seq LIMIT 1`, peerID))
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

func (s *Store) DeleteOutbox(seq int64) error {
	_, err := s.db.Exec(`DELETE FROM chat_outbox WHERE seq = ?`, seq)
	return err
}

// RetryOutboxLater records a failed attempt on a peer's queue head.
func (s *Store) RetryOutboxLater(seq int64, attempts int, nextAt int64) error {
	_, err := s.db.Exec(`UPDATE chat_outbox SET attempts = ?, next_at = ? WHERE seq = ?`, attempts, nextAt, seq)
	return err
}

// OutboxPeers lists the peers that have queued envelopes.
func (s *Store) OutboxPeers() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT peer_id FROM chat_outbox ORDER BY peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountOutboxFor counts queued envelopes carrying a chat message.
func (s *Store) CountOutboxFor(chatID, messageID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_outbox WHERE chat_id = ? AND message_id = ?`, chatID, messageID).Scan(&n)
	return n, err
}

// TakeOutbox removes and returns a peer's queued items (all of them, or only those created
// before `before` when it is > 0), oldest first.
func (s *Store) TakeOutbox(peerID string, before int64) ([]OutboxItem, error) {
	q := `SELECT ` + outboxCols + ` FROM chat_outbox WHERE peer_id = ?`
	args := []any{peerID}
	if before > 0 {
		q += ` AND created_at < ?`
		args = append(args, before)
	}
	rows, err := s.db.Query(q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	var out []OutboxItem
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, o := range out {
		if err := s.DeleteOutbox(o.Seq); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TakeChatOutbox removes and returns the queued items of one chat, for one peer (or every
// peer when peerID is ""), oldest first; other chats' items keep their places.
func (s *Store) TakeChatOutbox(chatID, peerID string) ([]OutboxItem, error) {
	q := `SELECT ` + outboxCols + ` FROM chat_outbox WHERE chat_id = ?`
	args := []any{chatID}
	if peerID != "" {
		q += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	rows, err := s.db.Query(q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	var out []OutboxItem
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, o := range out {
		if err := s.DeleteOutbox(o.Seq); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteChat removes a chat with its members, messages, receipts and queued envelopes.
func (s *Store) DeleteChat(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM chat_outbox WHERE chat_id = ?`,
		`DELETE FROM chat_receipts WHERE chat_id = ?`,
		`DELETE FROM chat_messages WHERE chat_id = ?`,
		`DELETE FROM chat_files WHERE chat_id = ?`,
		`DELETE FROM chat_agents WHERE chat_id = ?`,
		`DELETE FROM chat_members WHERE chat_id = ?`,
		`DELETE FROM chat_topics WHERE chat_id = ?`,
		`DELETE FROM chat_message_topics WHERE chat_id = ?`,
		`DELETE FROM chat_topic_entries WHERE chat_id = ?`,
		`DELETE FROM chat_boards WHERE chat_id = ?`,
		`DELETE FROM chats WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ---- admin handover (DESIGN.md "Chats", Groups): beside the chat row, so PutChat leaves
// them alone. proof is the JSON certificate chain, offer our open offer (both "" = none).

func (s *Store) ChatAdmin(chatID string) (proof, offer string, err error) {
	err = s.db.QueryRow(`SELECT admin_proof, admin_offer FROM chats WHERE id = ?`, chatID).Scan(&proof, &offer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return proof, offer, err
}

func (s *Store) SetChatAdminProof(chatID, proof string) error {
	_, err := s.db.Exec(`UPDATE chats SET admin_proof = ? WHERE id = ?`, proof, chatID)
	return err
}

func (s *Store) SetChatAdminOffer(chatID, offer string) error {
	_, err := s.db.Exec(`UPDATE chats SET admin_offer = ? WHERE id = ?`, offer, chatID)
	return err
}

// PeerCaps lists what other installs advertised they understand (peer ID → space-separated
// capabilities).
func (s *Store) PeerCaps() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT peer_id, caps FROM peer_caps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		out[p] = c
	}
	return out, rows.Err()
}

func (s *Store) SetPeerCaps(peerID, caps string, at int64) error {
	_, err := s.db.Exec(`INSERT INTO peer_caps (peer_id, caps, updated_at) VALUES (?,?,?)
		ON CONFLICT(peer_id) DO UPDATE SET caps = excluded.caps, updated_at = excluded.updated_at`, peerID, caps, at)
	return err
}

// DropOutboxTagged removes what waits in a chat's outbox under tag (the message_id column:
// our message's ID, or another envelope's own handle such as an admin offer's ID) and
// reports how many items it removed.
func (s *Store) DropOutboxTagged(chatID, tag string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM chat_outbox WHERE chat_id = ? AND message_id = ?`, chatID, tag)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

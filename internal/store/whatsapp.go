package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// ---- WhatsApp (DESIGN.md "WhatsApp"): the linked account's chats, messages, reactions and
// contacts. Names are resolved when read: a contact's address-book name, else the name they
// chose, else what the message carried. Nothing here is the device's keys (secrets/whatsapp.db).

// WaMessageRow is a stored message with what only the daemon sees: how to fetch the attachment
// from WhatsApp (MediaRef, JSON with its keys) and the downloaded file's name (MediaFile).
type WaMessageRow struct {
	model.WaMessage
	MediaRef  string
	MediaFile string
}

const waChatCols = `jid, name, kind, unread, pinned, muted, archived, avatar, last_at, updated_at`

func scanWaChat(r scanner) (model.WaChat, error) {
	var c model.WaChat
	var pinned, muted, archived int
	var avatar string
	var updated int64
	err := r.Scan(&c.JID, &c.Name, &c.Kind, &c.Unread, &pinned, &muted, &archived, &avatar, &c.LastAt, &updated)
	c.Pinned, c.Muted, c.Archived = pinned != 0, muted != 0, archived != 0
	if avatar != "" {
		c.Avatar = &avatar
	}
	return c, err
}

// WaPutChat inserts or replaces a chat (its last message is read separately).
func (s *Store) WaPutChat(c model.WaChat) error {
	avatar := ""
	if c.Avatar != nil {
		avatar = *c.Avatar
	}
	_, err := s.db.Exec(`INSERT INTO wa_chats (`+waChatCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(jid) DO UPDATE SET name = excluded.name, kind = excluded.kind, unread = excluded.unread,
		pinned = excluded.pinned, muted = excluded.muted, archived = excluded.archived, avatar = excluded.avatar,
		last_at = excluded.last_at, updated_at = excluded.updated_at`,
		c.JID, c.Name, c.Kind, c.Unread, boolInt(c.Pinned), boolInt(c.Muted), boolInt(c.Archived), avatar, c.LastAt, model.Now())
	return err
}

// WaChatShown reports whether a chat belongs in the chat list (waShown).
func (s *Store) WaChatShown(jid string) bool {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM wa_chats WHERE jid = ? AND `+waShown, jid).Scan(&n)
	return err == nil && n > 0
}

// WaGetChat returns one chat with its last message.
func (s *Store) WaGetChat(jid string) (model.WaChat, error) {
	c, err := scanWaChat(s.db.QueryRow(`SELECT `+waChatCols+` FROM wa_chats WHERE jid = ?`, jid))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if c.Last, err = s.waLast(jid); err != nil {
		return c, err
	}
	return c, nil
}

// waShown is the SQL condition for a chat that belongs in the chat list, as on the phone: a
// group, a pinned or unread chat, or a chat with at least one message that is not a system
// notice. WhatsApp's history sync also carries a conversation for nearly everyone the account
// ever had an encrypted session with (group members, businesses), holding only "messages are
// end-to-end encrypted" or a business privacy notice; the phone hides those, and so does tmx.
const waShown = `(kind = 'group' OR pinned != 0 OR unread > 0 OR EXISTS (SELECT 1 FROM wa_messages m
	WHERE m.chat_jid = wa_chats.jid AND m.type != 'system'))`

// WaListChats lists chats, newest activity first; q matches the chat's name or its JID (every
// word, case-insensitive). shownOnly leaves out the chats the phone hides (waShown). limit <= 0
// means 100.
func (s *Store) WaListChats(q string, limit int, shownOnly bool) ([]model.WaChat, error) {
	if limit <= 0 {
		limit = 100
	}
	where, args := "", []any{}
	if shownOnly {
		where = " AND " + waShown
	}
	for _, w := range strings.Fields(strings.ToLower(q)) {
		where += ` AND (lower(name) LIKE ? ESCAPE '\' OR jid LIKE ? ESCAPE '\')`
		like := "%" + likeEscape(w) + "%"
		args = append(args, like, like)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+waChatCols+` FROM wa_chats WHERE 1 = 1`+where+
		` ORDER BY last_at DESC, jid LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []model.WaChat
	for rows.Next() {
		c, err := scanWaChat(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Last, err = s.waLast(out[i].JID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) waLast(jid string) (*model.WaMessage, error) {
	rows, err := s.waQuery(`m.chat_jid = ? ORDER BY m.at DESC, m.id DESC LIMIT 1`, jid)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0].WaMessage, nil
}

const waMsgCols = `m.chat_jid, m.id, m.sender_jid, COALESCE(NULLIF(c.name, ''), NULLIF(c.push_name, ''), m.sender_name),
	m.from_me, m.at, m.type, m.text, m.caption, m.media, m.media_ref, m.media_file, m.quoted, m.status, m.error,
	m.edited, m.deleted, m.origin`

// waQuery runs a SELECT over messages (with the sender's name resolved) and attaches reactions.
func (s *Store) waQuery(where string, args ...any) ([]WaMessageRow, error) {
	rows, err := s.db.Query(`SELECT `+waMsgCols+` FROM wa_messages m LEFT JOIN wa_contacts c ON c.jid = m.sender_jid WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WaMessageRow
	for rows.Next() {
		var m WaMessageRow
		var fromMe, edited, deleted int
		var media, quoted string
		if err := rows.Scan(&m.ChatJID, &m.ID, &m.SenderJID, &m.SenderName, &fromMe, &m.At, &m.Type, &m.Text, &m.Caption,
			&media, &m.MediaRef, &m.MediaFile, &quoted, &m.Status, &m.Error, &edited, &deleted, &m.Origin); err != nil {
			return nil, err
		}
		m.FromMe, m.Edited, m.Deleted = fromMe != 0, edited != 0, deleted != 0
		if media != "" {
			m.Media = &model.WaMedia{}
			if json.Unmarshal([]byte(media), m.Media) != nil {
				m.Media = nil
			} else {
				m.Media.Downloaded = m.MediaFile != ""
			}
		}
		if quoted != "" {
			m.Quoted = &model.WaQuoted{}
			if json.Unmarshal([]byte(quoted), m.Quoted) != nil {
				m.Quoted = nil
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.waAttachReactions(out)
}

func (s *Store) waAttachReactions(list []WaMessageRow) error {
	if len(list) == 0 {
		return nil
	}
	idx := map[[2]string]int{}
	byChat := map[string][]any{}
	for i, m := range list {
		idx[[2]string{m.ChatJID, m.ID}] = i
		byChat[m.ChatJID] = append(byChat[m.ChatJID], m.ID)
	}
	for chat, ids := range byChat {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		rows, err := s.db.Query(`SELECT r.message_id, r.emoji, r.by_jid, COALESCE(NULLIF(c.name, ''), NULLIF(c.push_name, ''), r.by_name)
			FROM wa_reactions r LEFT JOIN wa_contacts c ON c.jid = r.by_jid
			WHERE r.chat_jid = ? AND r.message_id IN (`+marks+`) ORDER BY r.at`, append([]any{chat}, ids...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var r model.WaReact
			if err := rows.Scan(&id, &r.Emoji, &r.ByJID, &r.By); err != nil {
				rows.Close()
				return err
			}
			if i, ok := idx[[2]string{chat, id}]; ok {
				list[i].Reactions = append(list[i].Reactions, r)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// WaPutMessage inserts or replaces a message (keyed by chat and id).
func (s *Store) WaPutMessage(m WaMessageRow) error {
	media, quoted := "", ""
	if m.Media != nil {
		mm := *m.Media
		mm.Downloaded = false // derived from media_file
		b, _ := json.Marshal(mm)
		media = string(b)
	}
	if m.Quoted != nil {
		b, _ := json.Marshal(m.Quoted)
		quoted = string(b)
	}
	_, err := s.db.Exec(`INSERT INTO wa_messages (chat_jid, id, sender_jid, sender_name, from_me, at, type, text, caption, media,
		media_ref, media_file, quoted, status, error, edited, deleted, origin) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(chat_jid, id) DO UPDATE SET sender_jid = excluded.sender_jid, sender_name = excluded.sender_name,
		from_me = excluded.from_me, at = excluded.at, type = excluded.type, text = excluded.text, caption = excluded.caption,
		media = excluded.media, media_ref = excluded.media_ref, media_file = excluded.media_file, quoted = excluded.quoted,
		status = excluded.status, error = excluded.error, edited = excluded.edited, deleted = excluded.deleted,
		origin = excluded.origin`,
		m.ChatJID, m.ID, m.SenderJID, m.SenderName, boolInt(m.FromMe), m.At, m.Type, m.Text, m.Caption, media,
		m.MediaRef, m.MediaFile, quoted, m.Status, m.Error, boolInt(m.Edited), boolInt(m.Deleted), m.Origin)
	return err
}

// WaGetMessage returns one message.
func (s *Store) WaGetMessage(chat, id string) (WaMessageRow, error) {
	rows, err := s.waQuery(`m.chat_jid = ? AND m.id = ?`, chat, id)
	if err != nil {
		return WaMessageRow{}, err
	}
	if len(rows) == 0 {
		return WaMessageRow{}, ErrNotFound
	}
	return rows[0], nil
}

// WaMessages returns up to limit messages of a chat before the message before (empty: the
// newest), oldest first.
func (s *Store) WaMessages(chat, before string, limit int) ([]WaMessageRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var rows []WaMessageRow
	var err error
	if before == "" {
		rows, err = s.waQuery(`m.chat_jid = ? ORDER BY m.at DESC, m.id DESC LIMIT ?`, chat, limit)
	} else {
		var at int64
		if err := s.db.QueryRow(`SELECT at FROM wa_messages WHERE chat_jid = ? AND id = ?`, chat, before).Scan(&at); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		rows, err = s.waQuery(`m.chat_jid = ? AND (m.at < ? OR (m.at = ? AND m.id < ?)) ORDER BY m.at DESC, m.id DESC LIMIT ?`,
			chat, at, at, before, limit)
	}
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// WaSetStatus raises the delivery state of own messages (a late "delivered" never undoes
// "read") and returns the ones that changed.
func (s *Store) WaSetStatus(chat string, ids []string, st model.WaMessageStatus) ([]WaMessageRow, error) {
	var changed []WaMessageRow
	for _, id := range ids {
		m, err := s.WaGetMessage(chat, id)
		if err != nil {
			continue // not ours, or not synced (yet)
		}
		if model.WaStatusRank(st) <= model.WaStatusRank(m.Status) && m.Status != model.WaFailed {
			continue
		}
		if _, err := s.db.Exec(`UPDATE wa_messages SET status = ?, error = '' WHERE chat_jid = ? AND id = ?`, st, chat, id); err != nil {
			return changed, err
		}
		m.Status, m.Error = st, ""
		changed = append(changed, m)
	}
	return changed, nil
}

// WaSetMediaFile records the downloaded file of a message's attachment ("" forgets it).
func (s *Store) WaSetMediaFile(chat, id, file string) error {
	_, err := s.db.Exec(`UPDATE wa_messages SET media_file = ? WHERE chat_jid = ? AND id = ?`, file, chat, id)
	return err
}

// WaPutReaction stores one person's reaction to a message; an empty emoji removes it.
func (s *Store) WaPutReaction(chat, msgID, byJID, byName, emoji string, at int64) error {
	if emoji == "" {
		_, err := s.db.Exec(`DELETE FROM wa_reactions WHERE chat_jid = ? AND message_id = ? AND by_jid = ?`, chat, msgID, byJID)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO wa_reactions (chat_jid, message_id, by_jid, by_name, emoji, at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(chat_jid, message_id, by_jid) DO UPDATE SET by_name = excluded.by_name, emoji = excluded.emoji, at = excluded.at
		WHERE excluded.at >= wa_reactions.at`, chat, msgID, byJID, byName, emoji, at)
	return err
}

// WaPutContact stores a contact's names; an empty name or push name keeps the stored one.
func (s *Store) WaPutContact(jid, name, pushName string) error {
	_, err := s.db.Exec(`INSERT INTO wa_contacts (jid, name, push_name, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(jid) DO UPDATE SET name = COALESCE(NULLIF(excluded.name, ''), wa_contacts.name),
		push_name = COALESCE(NULLIF(excluded.push_name, ''), wa_contacts.push_name), updated_at = excluded.updated_at`,
		jid, name, pushName, model.Now())
	return err
}

// WaContactName returns a contact's best name ("" when unknown).
func (s *Store) WaContactName(jid string) string {
	var n string
	_ = s.db.QueryRow(`SELECT COALESCE(NULLIF(name, ''), push_name) FROM wa_contacts WHERE jid = ?`, jid).Scan(&n)
	return n
}

// WaSentSince lists the times of messages sent through tmx since (the overall rate limit).
func (s *Store) WaSentSince(since int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT at FROM wa_messages WHERE origin != '' AND at >= ? ORDER BY at`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// WaPending lists own messages tmx had not finished sending (a restart cut them off).
func (s *Store) WaPending() ([]WaMessageRow, error) {
	return s.waQuery(`m.origin != '' AND m.status = ? ORDER BY m.at`, model.WaPending)
}

// WaMediaFiles lists every downloaded attachment's file name (to clean up the folder).
func (s *Store) WaMediaFiles() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT media_file FROM wa_messages WHERE media_file != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out[f] = true
	}
	return out, rows.Err()
}

// WaDeleteChat removes a chat with its messages and reactions (deleted on the phone).
func (s *Store) WaDeleteChat(jid string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM wa_reactions WHERE chat_jid = ?`, `DELETE FROM wa_messages WHERE chat_jid = ?`,
		`DELETE FROM wa_chats WHERE jid = ?`} {
		if _, err := tx.Exec(q, jid); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// WaMeta reads a value about the linked account ("" when unset).
func (s *Store) WaMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM wa_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// WaSetMeta writes a value about the linked account ("" removes it).
func (s *Store) WaSetMeta(key, value string) error {
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM wa_meta WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO wa_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// WaClear removes every chat, message, reaction and contact (unlinking).
func (s *Store) WaClear() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, t := range []string{"wa_reactions", "wa_messages", "wa_chats", "wa_contacts"} {
		if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

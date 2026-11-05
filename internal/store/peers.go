package store

import (
	"database/sql"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Peer mail directions.
const (
	PeerIn  = "in"
	PeerOut = "out"
)

// PeerMessage maps a local message to its wire ID on a peer machine (fleet peer mail).
// NextAttemptAt > 0: an outbound message still to be sent (at or after that time).
type PeerMessage struct {
	LocalID       string
	Peer          string
	WireID        string
	Direction     string // PeerIn or PeerOut
	SHA256        string // of the body, lowercase hex
	Attempts      int
	NextAttemptAt int64
	LastError     string
}

const peerCols = `local_id, peer, wire_id, direction, sha256, attempts, next_attempt_at, last_error`

func scanPeer(r scanner) (PeerMessage, error) {
	var p PeerMessage
	err := r.Scan(&p.LocalID, &p.Peer, &p.WireID, &p.Direction, &p.SHA256, &p.Attempts, &p.NextAttemptAt, &p.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}

// PutPeerMessage stores a new message and its peer mapping in one transaction. A clash on
// (peer, wire ID) is an error and stores nothing.
func (s *Store) PutPeerMessage(m model.Message, p PeerMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO peer_messages (`+peerCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		p.LocalID, p.Peer, p.WireID, p.Direction, p.SHA256, p.Attempts, p.NextAttemptAt, p.LastError); err != nil {
		return err
	}
	if _, err := tx.Exec(upsert("messages", messageCols),
		m.ID, m.From, m.To, m.Body, m.State, nullable(m.ReplyTo), m.CreatedAt, m.DeliveredAt, m.ThreadID, m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt); err != nil {
		return err
	}
	return tx.Commit()
}

// PeerByWire returns the mapping of a peer's wire ID (ErrNotFound when unknown).
func (s *Store) PeerByWire(peer, wireID string) (PeerMessage, error) {
	return scanPeer(s.db.QueryRow(`SELECT `+peerCols+` FROM peer_messages WHERE peer=? AND wire_id=?`, peer, wireID))
}

// PeerByLocal returns the mapping of a local message (ErrNotFound when it is not peer mail).
func (s *Store) PeerByLocal(localID string) (PeerMessage, error) {
	return scanPeer(s.db.QueryRow(`SELECT `+peerCols+` FROM peer_messages WHERE local_id=?`, localID))
}

// DuePeerMessages returns outbound peer mail due at now, oldest due first.
func (s *Store) DuePeerMessages(now int64, limit int) ([]PeerMessage, error) {
	rows, err := s.db.Query(`SELECT `+peerCols+` FROM peer_messages WHERE next_attempt_at > 0 AND next_attempt_at <= ?
		ORDER BY next_attempt_at LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerMessage
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NextPeerAttempt returns the earliest pending send time (0 = none).
func (s *Store) NextPeerAttempt() (int64, error) {
	var at sql.NullInt64
	err := s.db.QueryRow(`SELECT MIN(next_attempt_at) FROM peer_messages WHERE next_attempt_at > 0`).Scan(&at)
	return at.Int64, err
}

// SetPeerAttempt records a send attempt: next = 0 ends the retry.
func (s *Store) SetPeerAttempt(localID string, attempts int, next int64, lastErr string) error {
	_, err := s.db.Exec(`UPDATE peer_messages SET attempts=?, next_attempt_at=?, last_error=? WHERE local_id=?`,
		attempts, next, lastErr, localID)
	return err
}

// putPeerMapping participates in the caller's transaction so owner decisions
// cannot commit without the durable peer outbox entry needed to deliver them.
func putPeerMapping(tx *sql.Tx, p PeerMessage) error {
	_, err := tx.Exec(`INSERT INTO peer_messages (`+peerCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		p.LocalID, p.Peer, p.WireID, p.Direction, p.SHA256, p.Attempts, p.NextAttemptAt, p.LastError)
	return err
}

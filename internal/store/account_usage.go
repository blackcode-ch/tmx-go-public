package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

type AccountTurnUsage struct {
	AgentID, Name, Model string
	Turns                int
}

func (s *Store) RecordAccountTurns(harness, account string, a AccountTurnUsage, at int64) error {
	_, err := s.db.Exec(`INSERT INTO account_turn_usage(harness, account, model, agent_id, name, at, turns)
		VALUES (?,?,?,?,?,?,?)`, harness, account, a.Model, a.AgentID, a.Name, at, a.Turns)
	return err
}

func (s *Store) AccountTurns(harness, account string, after, through int64) ([]AccountTurnUsage, error) {
	rows, err := s.db.Query(`SELECT agent_id, MAX(name), model, SUM(turns)
		FROM account_turn_usage WHERE harness=? AND account=? AND at>? AND at<=?
		GROUP BY agent_id, model`, harness, account, after, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountTurnUsage
	for rows.Next() {
		var a AccountTurnUsage
		if err := rows.Scan(&a.AgentID, &a.Name, &a.Model, &a.Turns); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) PruneAccountTurns(before int64) error {
	_, err := s.db.Exec(`DELETE FROM account_turn_usage WHERE at<=?`, before)
	return err
}

// ObserveAccountQuotaWindow updates the provider window before claiming either
// threshold. A spent reset can clear usage without moving the displayed end.
func (s *Store) ObserveAccountQuotaWindow(key string, observedAt, resetAt int64, used float64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var priorAt, priorReset int64
	var peak float64
	err = tx.QueryRow(`SELECT observed_at, reset_at, peak_used FROM account_quota_windows WHERE key=?`, key).
		Scan(&priorAt, &priorReset, &peak)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if observedAt < priorAt {
		return false, nil
	}
	natural := priorReset > 0 && observedAt >= priorReset && resetAt > observedAt
	tolerance := time.Minute.Milliseconds()
	early := priorReset > observedAt && resetAt > priorReset+tolerance && peak >= 80 && used < 80
	nearPriorEnd := resetAt >= priorReset-tolerance && resetAt <= priorReset+tolerance
	if observedAt > priorAt && priorReset > observedAt && resetAt > observedAt && nearPriorEnd && used <= 10 && peak-used >= 70 {
		// A near-empty reading after a claimed peak distinguishes a refill
		// from ordinary corrections within the deadline's display precision.
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM account_usage_notices
			WHERE key IN (?,?) AND expires_at>? AND expires_at BETWEEN ? AND ?)`,
			key+"/80", key+"/90", observedAt, priorReset-tolerance, priorReset+tolerance).Scan(&early)
		if err != nil {
			return false, err
		}
	}
	if natural || early {
		// At a natural boundary, an undated notice may already have been
		// sent for the new window. Keep it and bind its expiry on this read.
		if _, err := tx.Exec(`DELETE FROM account_usage_notices WHERE key IN (?,?) AND (expires_at>0 OR ?)`,
			key+"/80", key+"/90", early); err != nil {
			return false, err
		}
		peak = used
	} else {
		resetAt = max(resetAt, priorReset)
		peak = max(used, peak)
	}
	if _, err := tx.Exec(`INSERT INTO account_quota_windows(key, observed_at, reset_at, peak_used) VALUES (?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET observed_at=excluded.observed_at, reset_at=excluded.reset_at, peak_used=excluded.peak_used`,
		key, observedAt, resetAt, peak); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ObserveAccountUsageWindow extends a claimed window without claiming a threshold.
func (s *Store) ObserveAccountUsageWindow(key string, now, expires int64) error {
	_, err := s.db.Exec(`UPDATE account_usage_notices SET expires_at=? WHERE key=? AND expires_at<?
		AND (expires_at=0 OR expires_at>?)`, expires, key, expires, now)
	return err
}

// QueueAccountUsageNotice claims the window and enqueues the head's mail together.
func (s *Store) QueueAccountUsageNotice(key string, now, expires int64, m model.Message) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO account_usage_notices(key, expires_at) VALUES (?,?)
		ON CONFLICT (key) DO UPDATE SET expires_at=excluded.expires_at
		WHERE account_usage_notices.expires_at>0 AND account_usage_notices.expires_at<=?
		AND excluded.expires_at>?`, key, expires, now, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		// Renew the active window without alerting if the provider adjusts its end.
		if _, err := tx.Exec(`UPDATE account_usage_notices SET expires_at=MAX(expires_at,?) WHERE key=?`, expires, key); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if _, err := tx.Exec(`INSERT INTO messages (`+messageCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.From, m.To, m.Body, m.State, nullable(m.ReplyTo), m.CreatedAt, m.DeliveredAt, m.ThreadID,
		m.RetryID, m.RetryHash, m.Receipt, m.ReceiptAt, m.Urgent); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

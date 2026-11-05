package store

import (
	"database/sql"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

const linkCols = `id, thread_id, axis, ref, role, label, why, by_name, how, at, ended_at`

func scanLink(r scanner) (model.ThreadLink, error) {
	var l model.ThreadLink
	err := r.Scan(&l.ID, &l.ThreadID, &l.Axis, &l.Ref, &l.Role, &l.Label, &l.Why, &l.By, &l.How, &l.At, &l.EndedAt)
	return l, err
}

// AddLink stores a link unless the thread already has the same one open (thread, axis, ref and
// role); it returns the open link, and whether it is new.
func (s *Store) AddLink(l model.ThreadLink) (model.ThreadLink, bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO thread_links (`+linkCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,0)`,
		l.ID, l.ThreadID, l.Axis, l.Ref, l.Role, l.Label, l.Why, l.By, l.How, l.At)
	if err != nil {
		return model.ThreadLink{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return model.ThreadLink{}, false, err
	}
	got, err := scanLink(s.db.QueryRow(`SELECT `+linkCols+` FROM thread_links
		WHERE thread_id = ? AND axis = ? AND ref = ? AND role = ? AND ended_at = 0`, l.ThreadID, l.Axis, l.Ref, l.Role))
	return got, n > 0, err
}

func (s *Store) GetLink(id string) (model.ThreadLink, error) {
	l, err := scanLink(s.db.QueryRow(`SELECT `+linkCols+` FROM thread_links WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// EndLink detaches a link (it stays, with its end time); ending an ended link changes nothing.
func (s *Store) EndLink(id string, at int64) error {
	res, err := s.db.Exec(`UPDATE thread_links SET ended_at = ? WHERE id = ? AND ended_at = 0`, at, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetLink(id); err != nil {
			return err
		}
	}
	return nil
}

// EndLinksTo detaches every open link to one thing (an agent that stopped, a shell that ended)
// and reports how many it ended.
func (s *Store) EndLinksTo(axis, ref string, at int64) (int, error) {
	res, err := s.db.Exec(`UPDATE thread_links SET ended_at = ? WHERE axis = ? AND ref = ? AND ended_at = 0`, at, axis, ref)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Links lists a thread's links, oldest first; ended ones only with past.
func (s *Store) Links(threadID string, past bool) ([]model.ThreadLink, error) {
	q := `SELECT ` + linkCols + ` FROM thread_links WHERE thread_id = ?`
	if !past {
		q += ` AND ended_at = 0`
	}
	return s.queryLinks(q+` ORDER BY at, rowid`, threadID)
}

// LinksTo lists the open links to one thing, across threads (the Matrix's "see by").
func (s *Store) LinksTo(axis, ref string) ([]model.ThreadLink, error) {
	return s.queryLinks(`SELECT `+linkCols+` FROM thread_links WHERE axis = ? AND ref = ? AND ended_at = 0 ORDER BY at, rowid`, axis, ref)
}

func (s *Store) queryLinks(q string, args ...any) ([]model.ThreadLink, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ThreadLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LinkCounts counts the open links of every thread per axis: thread ID -> axis -> count.
func (s *Store) LinkCounts() (map[string]map[string]int, error) {
	rows, err := s.db.Query(`SELECT thread_id, axis, COUNT(*) FROM thread_links WHERE ended_at = 0 GROUP BY thread_id, axis`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var thread, axis string
		var n int
		if err := rows.Scan(&thread, &axis, &n); err != nil {
			return nil, err
		}
		if out[thread] == nil {
			out[thread] = map[string]int{}
		}
		out[thread][axis] = n
	}
	return out, rows.Err()
}

// LastEntryAt is when each thread's log last got an entry: thread ID -> time.
func (s *Store) LastEntryAt() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT thread_id, MAX(at) FROM thread_entries GROUP BY thread_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var thread string
		var at int64
		if err := rows.Scan(&thread, &at); err != nil {
			return nil, err
		}
		out[thread] = at
	}
	return out, rows.Err()
}

// LinksOn lists every open link of one axis, oldest first (the Matrix's "see by").
func (s *Store) LinksOn(axis string) ([]model.ThreadLink, error) {
	return s.queryLinks(`SELECT `+linkCols+` FROM thread_links WHERE axis = ? AND ended_at = 0 ORDER BY at, rowid`, axis)
}

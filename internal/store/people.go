package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func (s *Store) PutPerson(p model.Person) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO people(key,data,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`, p.Key, string(b), p.UpdatedAt)
	return err
}

func (s *Store) GetPerson(key string) (model.Person, error) {
	var raw string
	err := s.db.QueryRow(`SELECT data FROM people WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Person{}, ErrNotFound
	}
	if err != nil {
		return model.Person{}, err
	}
	var p model.Person
	return p, json.Unmarshal([]byte(raw), &p)
}

func (s *Store) ListPeople() ([]model.Person, error) {
	rows, err := s.db.Query(`SELECT data FROM people ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Person{}
	for rows.Next() {
		var raw string
		var p model.Person
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePerson removes the person and their promises (and those promises' pending reminders).
func (s *Store) DeletePerson(key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE reminders SET state='cancelled' WHERE state='pending' AND promise_id IN (SELECT id FROM promises WHERE person_key=?)`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM promises WHERE person_key=?`, key); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM people WHERE key=?`, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) PutPromise(p model.Promise) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO promises(id,person_key,status,due,data,created_at,updated_at) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET person_key=excluded.person_key, status=excluded.status, due=excluded.due, data=excluded.data, updated_at=excluded.updated_at`,
		p.ID, p.PersonKey, p.Status, p.Due, string(b), p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetPromise(id string) (model.Promise, error) {
	var raw string
	err := s.db.QueryRow(`SELECT data FROM promises WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Promise{}, ErrNotFound
	}
	if err != nil {
		return model.Promise{}, err
	}
	var p model.Promise
	return p, json.Unmarshal([]byte(raw), &p)
}

// ListPromises filters by person ("" = all) and status ("" = all); open first, then by due date.
func (s *Store) ListPromises(person, status string) ([]model.Promise, error) {
	rows, err := s.db.Query(`SELECT data FROM promises WHERE (?='' OR person_key=?) AND (?='' OR status=?)
ORDER BY status='open' DESC, due='' , due, created_at`, person, person, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Promise{}
	for rows.Next() {
		var raw string
		var p model.Promise
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const reminderCols = `id,fire_at,target,text,promise_id,state,attempts,last_error,fired_at,created_at`

func scanReminder(sc interface{ Scan(...any) error }) (model.Reminder, error) {
	var r model.Reminder
	err := sc.Scan(&r.ID, &r.FireAt, &r.Target, &r.Text, &r.PromiseID, &r.State, &r.Attempts, &r.LastError, &r.FiredAt, &r.CreatedAt)
	return r, err
}

// AddReminder inserts a reminder; an existing ID is left as it is (idempotent imports).
func (s *Store) AddReminder(r model.Reminder) error {
	_, err := s.db.Exec(`INSERT INTO reminders(`+reminderCols+`) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		r.ID, r.FireAt, r.Target, r.Text, r.PromiseID, r.State, r.Attempts, r.LastError, r.FiredAt, r.CreatedAt)
	return err
}

func (s *Store) GetReminder(id string) (model.Reminder, error) {
	r, err := scanReminder(s.db.QueryRow(`SELECT `+reminderCols+` FROM reminders WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ListReminders: state "" = all; newest fire time last.
func (s *Store) ListReminders(state, target string) ([]model.Reminder, error) {
	rows, err := s.db.Query(`SELECT `+reminderCols+` FROM reminders WHERE (?='' OR state=?) AND (?='' OR target=?) ORDER BY fire_at`, state, state, target, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Reminder{}
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DueReminders are pending reminders whose time has come (at <= now, unix ms).
func (s *Store) DueReminders(now int64) ([]model.Reminder, error) {
	rows, err := s.db.Query(`SELECT `+reminderCols+` FROM reminders WHERE state='pending' AND fire_at<=? ORDER BY fire_at LIMIT 100`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NextReminderAt is the earliest pending fire time (0 = none).
func (s *Store) NextReminderAt() (int64, error) {
	var at sql.NullInt64
	err := s.db.QueryRow(`SELECT MIN(fire_at) FROM reminders WHERE state='pending'`).Scan(&at)
	return at.Int64, err
}

// MarkReminderFired moves pending -> fired; false if it was not pending any more.
func (s *Store) MarkReminderFired(id string, at int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE reminders SET state='fired', fired_at=?, attempts=attempts+1, last_error='' WHERE id=? AND state='pending'`, at, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReminderFailed records a failed attempt and moves the reminder to retryAt.
func (s *Store) ReminderFailed(id, msg string, retryAt int64) error {
	_, err := s.db.Exec(`UPDATE reminders SET attempts=attempts+1, last_error=?, fire_at=? WHERE id=? AND state='pending'`, msg, retryAt, id)
	return err
}

func (s *Store) CancelReminder(id string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reminders SET state='cancelled' WHERE id=? AND state='pending'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelPromiseReminders cancels the pending reminders of a promise (done/dropped/rescheduled).
func (s *Store) CancelPromiseReminders(promiseID string) error {
	_, err := s.db.Exec(`UPDATE reminders SET state='cancelled' WHERE promise_id=? AND state='pending'`, promiseID)
	return err
}

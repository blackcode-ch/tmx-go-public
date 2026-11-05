// Package store persists the fleet in SQLite (modernc.org/sqlite, pure Go; WAL; busy
// timeout). Schema changes are forward-only migrations in migrations.go.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by the Get* methods.
var ErrNotFound = errors.New("not found")

// TimelineKeep is how many timeline entries are kept per agent.
const TimelineKeep = 5000

type Store struct {
	db              *sql.DB
	rollbackWarning string // immutable after OpenRecoverable returns
}

// RollbackWarning describes degraded startup recovery protection, if any.
func (s *Store) RollbackWarning() string { return s.rollbackWarning }

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func openDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Set("_txlock", "immediate")
	// A URI filename: '%', '?' and '#' in the path must be escaped, or SQLite reads them as an
	// escape, the query or a fragment (a path with '#' opened the file named up to it).
	esc := escapeURIPath(abs)
	dsn := "file:" + esc + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A few connections let reads proceed under WAL; writes serialize on busy_timeout.
	db.SetMaxOpenConns(4)
	return db, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the applied schema version.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}

// upsert builds "INSERT ... ON CONFLICT(id) DO UPDATE" for a column list whose first column
// is id. Unlike INSERT OR REPLACE, a clash on another UNIQUE column (agent name, project
// slug) is an error instead of silently deleting the other row.
func upsert(table, cols string) string {
	names := strings.Split(cols, ",")
	marks := make([]string, len(names))
	var sets []string
	for i, n := range names {
		n = strings.TrimSpace(n)
		names[i] = n
		marks[i] = "?"
		if i > 0 {
			sets = append(sets, n+" = excluded."+n)
		}
	}
	return "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ",") +
		") ON CONFLICT(id) DO UPDATE SET " + strings.Join(sets, ", ")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// escapeURIPath escapes filename characters that SQLite treats as URI syntax.
func escapeURIPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(filepath.ToSlash(path))
}

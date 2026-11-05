package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

var probationNow = time.Now // tests move the clock

// probationVersion is the running binary's version, which a probation record must name (tests set it).
var probationVersion = func() string { return config.Version }

// OpenRecoverable is the daemon entrypoint. The caller holds its single-instance
// lock and has not opened any other database users. A failed migration restores
// the retained installation and database, and returns an error (never runs a
// process whose executable has just been replaced). So does an update that failed
// its health probation (rollback.ErrRolledBack).
func OpenRecoverable(path, binary, checkpoint string) (*Store, error) {
	if rollback.Pending(checkpoint) {
		unproven, err := rollback.Unproven(checkpoint, binary, path)
		if err != nil {
			return nil, err
		}
		if err := rollback.Restore(checkpoint, binary, path); err != nil {
			return nil, err
		}
		if unproven {
			return nil, errors.New("interrupted database rollback completed; no proven previous binary available; install a compatible binary before restarting")
		}
		return nil, errors.New("interrupted rollback completed; start tmx again")
	}
	// An updater install on probation: count this start; past the deadline or the start limit,
	// restore the retained pair instead of running the new binary (rollback.ErrRolledBack).
	if err := rollback.StartProbation(checkpoint, binary, path, probationVersion(), probationNow()); err != nil {
		return nil, err
	}
	_, onProbation, err := rollback.OnProbation(checkpoint)
	if err != nil {
		onProbation = true // unknown: keep the retained pair as it is, but start
	}
	// A fresh installation has no N-1 state to preserve.
	protected := rollback.AwaitingMigration(checkpoint)
	// Until the new binary is confirmed, the retained pair is the one to go back to: no refresh.
	cleanBoot := !protected && !onProbation
	_, statErr := os.Stat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if statErr == nil {
		db, err := openDB(path)
		if err != nil {
			return nil, err
		}
		var current int
		var exists int
		err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'").Scan(&exists)
		if err == nil && exists != 0 {
			err = db.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&current)
		}
		cerr := db.Close()
		if err != nil {
			return nil, err
		}
		if cerr != nil {
			return nil, cerr
		}
		if current < len(migrations) {
			cleanBoot = false
		}
		if current < len(migrations) && !protected {
			// The running binary may already be a manual replacement. Never
			// overwrite the proven-good pair with these unproven bytes.
			if !rollback.Available(checkpoint) {
				if err := rollback.PrepareUnproven(context.Background(), checkpoint, binary, path); err != nil {
					return nil, err
				}
			}
			if err := rollback.MarkUpdate(checkpoint); err != nil {
				return nil, err
			}
			protected = true
		}
	}
	s, err := Open(path)
	if err != nil {
		if protected {
			unproven, uerr := rollback.Unproven(checkpoint, binary, path)
			if uerr != nil {
				return nil, errors.Join(err, uerr)
			}
			if rerr := rollback.Restore(checkpoint, binary, path); rerr != nil {
				return nil, errors.Join(err, fmt.Errorf("automatic rollback failed: %w", rerr))
			}
			if unproven {
				return nil, fmt.Errorf("migration failed; database restored; no proven previous binary available; install a compatible binary before restarting: %w", err)
			}
			return nil, fmt.Errorf("migration failed; previous binary and database restored; start tmx again: %w", err)
		}
		return nil, err
	}
	if err := rollback.CompleteUpdate(checkpoint); err != nil {
		s.Close()
		return nil, err
	}
	if cleanBoot {
		if err := rollback.Refresh(context.Background(), checkpoint, binary, path); err != nil {
			s.rollbackWarning = fmt.Sprintf("rollback checkpoint stale: %v", err)
			slog.Warn(s.rollbackWarning)
		}
	}
	return s, nil
}

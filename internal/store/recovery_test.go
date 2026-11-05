package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

func TestFailedMigrationRestoresWholePair(t *testing.T) {
	for _, updater := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "updater"}[updater], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "tmx.db")
			binary := filepath.Join(dir, "tmx.exe")
			checkpoint := filepath.Join(dir, "rollback")
			os.WriteFile(binary, []byte("old binary"), 0700)
			s, err := OpenRecoverable(path, binary, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			version, _ := s.SchemaVersion()
			s.Close()
			if updater {
				if err := rollback.Prepare(context.Background(), checkpoint, binary, path); err != nil {
					t.Fatal(err)
				}
				if err := rollback.MarkUpdate(checkpoint); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(binary, []byte("new binary"), 0700)
			saved := migrations
			defer func() { migrations = saved }()
			migrations = append(append([]string{}, migrations...), "CREATE TABLE should_disappear(n)", "INVALID SQL")
			_, err = OpenRecoverable(path, binary, checkpoint)
			if err == nil || !strings.Contains(err.Error(), "previous binary and database restored") {
				t.Fatalf("error %v", err)
			}
			b, _ := os.ReadFile(binary)
			if string(b) != "old binary" {
				t.Fatalf("binary %q", b)
			}
			migrations = saved
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got, _ := s.SchemaVersion()
			if got != version {
				t.Fatalf("schema %d, want %d", got, version)
			}
			var n int
			s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='should_disappear'").Scan(&n)
			if n != 0 {
				t.Fatal("partial migration survived")
			}
		})
	}
}

func TestCleanBootRefreshesOnlyChangedBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tmx.db")
	binary := filepath.Join(dir, "tmx.exe")
	checkpoint := filepath.Join(dir, "rollback")
	os.WriteFile(binary, []byte("manual binary"), 0700)
	s, err := OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	after, err := os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("unchanged binary churned checkpoint")
	}
	if err := os.WriteFile(binary, []byte("new proven binary"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err = OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	after, err = os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("changed binary did not refresh checkpoint")
	}
	pairs, err := filepath.Glob(filepath.Join(checkpoint, "pair-*"))
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs %v: %v", pairs, err)
	}
}

func TestSuccessfulUpdaterPreservesPairUntilNextCleanBoot(t *testing.T) {
	dir := t.TempDir()
	path, binary, checkpoint := filepath.Join(dir, "tmx.db"), filepath.Join(dir, "tmx.exe"), filepath.Join(dir, "rollback")
	if err := os.WriteFile(binary, []byte("old binary"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := rollback.Prepare(context.Background(), checkpoint, binary, path); err != nil {
		t.Fatal(err)
	}
	if err := rollback.MarkUpdate(checkpoint); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("new binary"), 0700); err != nil {
		t.Fatal(err)
	}
	saved := migrations
	defer func() { migrations = saved }()
	migrations = append(append([]string{}, migrations...), "CREATE TABLE update_succeeded(n)")
	s, err = OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if rollback.AwaitingMigration(checkpoint) {
		t.Fatal("update marker survived successful startup")
	}
	after, err := os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("updater pair overwritten during migration")
	}
	s, err = OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	after, err = os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("next clean boot did not checkpoint new binary")
	}
}

func TestCheckpointFailureDoesNotBlockCleanBoot(t *testing.T) {
	dir := t.TempDir()
	path, binary, checkpoint := filepath.Join(dir, "tmx.db"), filepath.Join(dir, "tmx.exe"), filepath.Join(dir, "rollback")
	if err := os.WriteFile(binary, []byte("old binary"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	// A missing source binary causes refresh to fail without altering the pair.
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	s, err = OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.SchemaVersion(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.RollbackWarning(), "rollback checkpoint stale:") {
		t.Fatalf("warning %q", s.RollbackWarning())
	}
	after, err := os.ReadFile(filepath.Join(checkpoint, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed refresh changed pair")
	}
	b, err := os.ReadFile(filepath.Join(checkpoint, string(before), "binary"))
	if err != nil || string(b) != "old binary" {
		t.Fatalf("retained binary %q: %v", b, err)
	}
}

func TestFirstMigrationWithoutPairSnapshotsDB(t *testing.T) {
	dir := t.TempDir()
	path, binary, checkpoint := filepath.Join(dir, "tmx.db"), filepath.Join(dir, "tmx.exe"), filepath.Join(dir, "rollback")
	if err := os.WriteFile(binary, []byte("unproven replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path) // simulate a pre-checkpoint release
	if err != nil {
		t.Fatal(err)
	}
	version, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	saved := migrations
	defer func() { migrations = saved }()
	migrations = append(append([]string{}, migrations...), "CREATE TABLE should_disappear(n)", "INVALID SQL")
	_, err = OpenRecoverable(path, binary, checkpoint)
	if err == nil || !strings.Contains(err.Error(), "database restored; no proven previous binary") {
		t.Fatalf("error %v", err)
	}
	unproven, err := rollback.Unproven(checkpoint, binary, path)
	if err != nil || !unproven {
		t.Fatalf("unproven %v: %v", unproven, err)
	}
	migrations = saved
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SchemaVersion()
	if err != nil || got != version {
		t.Fatalf("schema %d, want %d: %v", got, version, err)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='should_disappear'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial migration %d: %v", n, err)
	}
	s.Close()
	// Even a later explicit restore must leave a manually repaired binary alone.
	if err := os.WriteFile(binary, []byte("compatible binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Restore(checkpoint, binary, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(binary)
	if err != nil || string(b) != "compatible binary" {
		t.Fatalf("binary %q: %v", b, err)
	}
	s, err = OpenRecoverable(path, binary, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	unproven, err = rollback.Unproven(checkpoint, binary, path)
	if err != nil || unproven {
		t.Fatalf("clean boot did not promote proven pair: %v, %v", unproven, err)
	}
}

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func TestRollbackCommandRestoresPairAndRequiresStoppedDaemon(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	exe := filepath.Join(paths.Home, "tmx.exe")
	if err := os.WriteFile(exe, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	// Match the updater's canonical installation path (Windows temp paths may
	// contain short names that EvalSymlinks expands).
	if err := rollback.Prepare(context.Background(), filepath.Join(paths.Home, "rollback"), installationPath(exe), paths.DB()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(exe, []byte("new"), 0700)
	var out bytes.Buffer
	e := &env{paths: paths, loaded: true, out: &out}
	lock, err := config.AcquireLock(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackInstallation(e, exe); err == nil {
		t.Fatal("live daemon allowed")
	}
	lock.Release()
	b, _ := os.ReadFile(exe)
	if string(b) != "new" {
		t.Fatal("locked command changed binary")
	}
	if err := rollbackInstallation(e, exe); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(exe)
	if string(b) != "old" {
		t.Fatalf("binary %q", b)
	}
	if out.Len() == 0 {
		t.Fatal("missing success output")
	}
}

func TestRollbackRejectsArguments(t *testing.T) {
	if err := cmdRollback(&env{}, []string{"unexpected"}); err == nil {
		t.Fatal("arguments accepted")
	}
}

func TestRollbackUnprovenCheckpointExplainsDatabaseOnlyRecovery(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	exe := filepath.Join(paths.Home, "tmx.exe")
	if err := os.WriteFile(exe, []byte("unproven"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := rollback.PrepareUnproven(context.Background(), filepath.Join(paths.Home, "rollback"), installationPath(exe), paths.DB()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("repaired"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := &env{paths: paths, loaded: true, out: &out}
	if err := rollbackInstallation(e, exe); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No proven previous binary") {
		t.Fatalf("output %q", out.String())
	}
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != "repaired" {
		t.Fatalf("binary %q: %v", b, err)
	}
}

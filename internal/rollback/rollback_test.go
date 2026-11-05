package rollback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSizeLimitPreservesPreviousPair(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{1, 4096} {
		err := prepareWithLimit(context.Background(), dir, binary, path, limit, false)
		if err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("oversized preparation replaced checkpoint")
	}
	if _, err := load(dir, binary, path); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "tmx.exe")
	dbPath := filepath.Join(dir, "state #%.db")
	if err := os.WriteFile(binary, []byte("old binary"), 0700); err != nil {
		t.Fatal(err)
	}
	// Create the database explicitly; production snapshots must never create a missing DB.
	if err := os.WriteFile(dbPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE value(n); INSERT INTO value VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "rollback"), binary, dbPath
}

func TestPairRestoresCommittedWALAndBinary(t *testing.T) {
	dir, binary, path := fixture(t)
	db, err := database(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; INSERT INTO value VALUES(2)"); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO value VALUES(3)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(binary, []byte("new binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dir, binary, path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(binary)
	if string(b) != "old binary" {
		t.Fatalf("binary %q", b)
	}
	db, err = database(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT sum(n) FROM value").Scan(&n); err != nil || n != 3 {
		t.Fatalf("snapshot sum=%d err=%v", n, err)
	}
	if Pending(dir) {
		t.Fatal("restore marker left")
	}
}

func TestFailedPreparationKeepsPreviousPair(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "current"))
	if err := Prepare(context.Background(), dir, binary, path+"missing"); err == nil {
		t.Fatal("missing database accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "current"))
	if string(before) != string(after) {
		t.Fatal("checkpoint replaced")
	}
}

func TestCorruptCheckpointDoesNotTouchLivePair(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	name, _ := os.ReadFile(filepath.Join(dir, "current"))
	if err := os.WriteFile(filepath.Join(dir, string(name), "database"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(binary, []byte("new binary"), 0700)
	if err := Restore(dir, binary, path); err == nil {
		t.Fatal("corruption accepted")
	}
	b, _ := os.ReadFile(binary)
	if string(b) != "new binary" {
		t.Fatal("live binary changed")
	}
}

func TestInterruptedRestoreIsRepeatable(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	if err := writeSync(filepath.Join(dir, "restoring"), []byte("pending")); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), dir, binary, path); err == nil {
		t.Fatal("pending recovery overwritten")
	}
	if err := Restore(dir, binary, path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dir, binary, path); err != nil {
		t.Fatal(err)
	}
}

func TestWrongInstallationRefused(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dir, binary+"other", path); err == nil {
		t.Fatal("other target accepted")
	}
}

func TestBundleRetained(t *testing.T) {
	dir, binary, path := fixture(t)
	bundle := filepath.Join(filepath.Dir(binary), "tmx.app")
	exe := filepath.Join(bundle, "Contents", "MacOS", "tmx")
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("old bundle"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), dir, bundle, path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(exe, []byte("new bundle"), 0700)
	if err := Restore(dir, bundle, path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	if string(b) != "old bundle" {
		t.Fatalf("bundle %q", b)
	}
}

func TestFailureBetweenMembersRecoversOnRetry(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(binary, []byte("new binary"), 0700)
	err := restoreWithReplace(dir, binary, path, func(stage, target string) error {
		if target == binary {
			return errors.New("injected binary replacement failure")
		}
		return replace(stage, target)
	})
	if err == nil || !Pending(dir) {
		t.Fatalf("failure must retain marker: %v", err)
	}
	if err := Restore(dir, binary, path); err != nil {
		t.Fatal(err)
	}
	if Pending(dir) {
		t.Fatal("recovery still pending")
	}
}

func TestRestoreReplacesCorruptDatabaseAndStaleSidecars(t *testing.T) {
	dir, binary, path := fixture(t)
	if err := Prepare(context.Background(), dir, binary, path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("broken database"), 0600)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		os.WriteFile(path+suffix, []byte("stale"), 0600)
	}
	if err := Restore(dir, binary, path); err != nil {
		t.Fatal(err)
	}
	db, err := database(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT n FROM value").Scan(&n); err != nil || n != 1 {
		t.Fatalf("restored data %d: %v", n, err)
	}
}

func TestPrepareRetainsOnlyLatestPair(t *testing.T) {
	dir, binary, path := fixture(t)
	for i := 0; i < 3; i++ {
		if err := Prepare(context.Background(), dir, binary, path); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(dir, "pair-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("retained generations %v: %v", matches, err)
	}
}

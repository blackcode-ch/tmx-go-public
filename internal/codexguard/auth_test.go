package codexguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthCopiesOnlyFixtureLoginAndRefusesMissingSource(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"auth.json": `{"fixture":true}`, "config.toml": "notify=['must-not-copy']"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := SyncOwnerAuth(source, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("copied configuration")
	}
	if err := os.Remove(filepath.Join(source, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if SyncOwnerAuth(source, dest) == nil {
		t.Fatal("stale login admitted")
	}
	// A missing source (owner signed out or switched accounts) must not leave the
	// previous copy behind looking like a still-valid login.
	if _, err := os.Stat(filepath.Join(dest, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("stale isolated copy survived a missing owner login")
	}
}

// TestOwnerAuthDeletedOnStopAndRefreshedOnLaunch is go-reviewer-o's #143 follow-up, tightened by
// tmx-lead: a credential copy must not linger for an agent that is not running (stop or shelve),
// and the next launch/resume must make a fresh copy equal to the current owner login, not reuse
// whatever was left over.
func TestOwnerAuthDeletedOnStopAndRefreshedOnLaunch(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"login":"first"}`)
	if err := SyncOwnerAuth(source, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "auth.json"))
	if err != nil || string(got) != `{"login":"first"}` {
		t.Fatalf("launch copy = %q, %v", got, err)
	}

	// Stop (or shelve): the isolated copy must be gone.
	if err := DeleteOwnerAuth(dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("auth copy survived stop")
	}
	if DeleteOwnerAuth(dest) != nil {
		t.Fatal("deleting an already-absent copy must not be an error")
	}

	// Resume/launch after an owner account switch: the fresh copy matches the new source,
	// never the one from before the stop.
	write(`{"login":"second-account"}`)
	if err := SyncOwnerAuth(source, dest); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(dest, "auth.json"))
	if err != nil || string(got) != `{"login":"second-account"}` {
		t.Fatalf("resume copy = %q, %v", got, err)
	}
}

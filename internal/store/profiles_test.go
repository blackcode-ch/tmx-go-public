package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestProfilesCRUD(t *testing.T) {
	s := openTest(t)
	p := model.DefaultProfiles()[3]
	p.CreatedAt, p.UpdatedAt, p.BuiltIn = 5, 6, true
	if err := s.PutProfile(p); err != nil {
		t.Fatal(err)
	}
	p.Name, p.UpdatedAt = "Code reviewer", 7
	if err := s.PutProfile(p); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProfiles()
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	got := list[0]
	if !got.SameSettings(p) || got.CreatedAt != 5 || got.UpdatedAt != 7 || got.BuiltIn {
		t.Fatalf("got %+v", got)
	}
	if err := s.DeleteProfile(p.Slug); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListProfiles(); len(list) != 0 {
		t.Fatal("not deleted")
	}
}

// A database from before profiles (schema 1) gets the column, and its agents the built-in
// profile of their level.
func TestMigrationGivesAgentsTheirLevelProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmx.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		migrations[0],
		`INSERT INTO schema_version(version) VALUES (1)`,
		`INSERT INTO agents (id, name, level, harness, mode, state, desired, created_at, updated_at)
			VALUES ('a1', 'head', 'head', 'claude', 'chat', 'idle', 'running', 1, 1),
			       ('a2', 'w', 'worker', 'claude', 'chat', 'idle', 'running', 2, 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.ListAgents()
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	if list[0].Profile != "head" || list[1].Profile != "worker" {
		t.Fatalf("profiles %q %q", list[0].Profile, list[1].Profile)
	}
}

func TestModelDefaultsUpgradeOnceAndPreserveCustomProfiles(t *testing.T) {
	for _, customized := range []bool{false, true} {
		t.Run(fmt.Sprint(customized), func(t *testing.T) {
			s := openTest(t)
			for _, slug := range []string{"lead", "worker", "reviewer"} {
				old, _ := model.DefaultProfile(slug)
				old.Model, old.AllowedModels = "claude-sonnet-5-5", []string{"claude-sonnet-5-5"}
				old.CreatedAt = 123
				if customized {
					old.Description = "owner customized"
				}
				if err := s.PutProfile(old); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.UpgradeModelDefaults(); err != nil {
				t.Fatal(err)
			}
			profiles, err := s.ListProfiles()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range profiles {
				want, _ := model.DefaultProfile(p.Slug)
				if customized {
					want.Model, want.AllowedModels, want.Description = "claude-sonnet-5-5", []string{"claude-sonnet-5-5"}, "owner customized"
				}
				if !p.SameSettings(want) || p.CreatedAt != 123 {
					t.Fatalf("unexpected upgrade: %+v", p)
				}
				// An owner deliberately selecting the old settings after migration keeps them.
				p.Model, p.AllowedModels = "claude-sonnet-5-5", []string{"claude-sonnet-5-5"}
				if err := s.PutProfile(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.UpgradeModelDefaults(); err != nil {
				t.Fatal(err)
			}
			profiles, _ = s.ListProfiles()
			for _, p := range profiles {
				if len(p.AllowedModels) != 1 || p.Model != "claude-sonnet-5-5" {
					t.Fatalf("owner edit overwritten: %+v", p)
				}
			}
		})
	}
}

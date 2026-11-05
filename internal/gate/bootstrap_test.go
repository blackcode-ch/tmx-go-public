package gate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
)

func bootstrapPaths(t *testing.T) (config.Paths, FileMarker) {
	t.Helper()
	p := config.Paths{Home: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	return p, FileMarker(filepath.Join(p.Home, "test-marker"))
}

func TestFreshInstallNoMarkerGateOff(t *testing.T) {
	p, m := bootstrapPaths(t)
	g := New(p, &Fake{}, nil, m)
	if g.Enabled() {
		t.Fatal("fresh installation locked")
	}
	b, err := os.ReadFile(g.Path())
	if err != nil || string(b) == "" {
		t.Fatal("explicit disabled state not persisted")
	}
	if absent, err := m.Absent(); !absent || err != nil {
		t.Fatal("bootstrap created a marker")
	}
	// Subsequent starts load the explicit choice without rewriting it.
	if err := os.WriteFile(g.Path(), []byte(`{"enabled":false,"grace_minutes":15}`), 0600); err != nil {
		t.Fatal(err)
	}
	next := New(p, &Fake{}, nil, m)
	if next.Enabled() || next.GraceMinutes() != 15 {
		t.Fatal("bootstrap overwrote explicit state")
	}
}

func TestUpgradeNeverEnrolledStaysOff(t *testing.T) {
	p, m := bootstrapPaths(t)
	unrelated := filepath.Join(p.Home, "existing-settings")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if New(p, &Fake{}, nil, m).Enabled() {
			t.Fatal("never-enrolled upgrade locked")
		}
	}
	if b, err := os.ReadFile(unrelated); err != nil || string(b) != "keep" {
		t.Fatal("upgrade modified other state")
	}
	if absent, err := m.Absent(); !absent || err != nil {
		t.Fatal("upgrade enrolled the gate")
	}
}

func TestDamagedStateDoesNotPersistMarker(t *testing.T) {
	for _, state := range []string{"broken", "{}", "null", `{"enabled":true,"grace_minutes":"broken"}`, "unreadable"} {
		t.Run(state, func(t *testing.T) {
			p, m := bootstrapPaths(t)
			path := filepath.Join(p.SecretsDir(), "gate.json")
			if state == "unreadable" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(state), 0600); err != nil {
				t.Fatal(err)
			}
			g := New(p, &Fake{}, nil, m)
			if !g.Enabled() || g.GraceMinutes() != 0 {
				t.Fatal("damaged state failed open")
			}
			if absent, err := m.Absent(); !absent || err != nil {
				t.Fatal("inferred state persisted an enrollment marker")
			}
		})
	}
}

func TestNoAuthenticatorFreshInstallNotLocked(t *testing.T) {
	p, m := bootstrapPaths(t)
	f := &Fake{Unavail: errors.New("no authenticator")}
	g := New(p, f, nil, m)
	defer g.Close()
	if err := g.Require(Action{Key: "ordinary-gated-action"}, ""); err != nil {
		t.Fatalf("fresh install blocked: %v", err)
	}
	if len(f.Prompts()) != 0 {
		t.Fatal("bootstrap prompted")
	}
	if err := g.Require(Action{Key: "enrol", Always: true}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal("enrolment should still need a working authenticator")
	}
}

func TestMissingStateWithDisabledMarkerDoesNotBootstrap(t *testing.T) {
	p, m := bootstrapPaths(t)
	if err := m.Set(false); err != nil {
		t.Fatal(err)
	}
	if !New(p, &Fake{}, nil, m).Enabled() {
		t.Fatal("present marker was treated as never enrolled")
	}
	if on, err := m.Read(); on || err != nil {
		t.Fatal("inferred state modified the marker")
	}
}

func TestValidEnabledStateMigratesMarker(t *testing.T) {
	p, m := bootstrapPaths(t)
	if err := os.WriteFile(filepath.Join(p.SecretsDir(), "gate.json"), []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !New(p, &Fake{}, nil, m).Enabled() {
		t.Fatal("enabled migration failed open")
	}
	if on, err := m.Read(); !on || err != nil {
		t.Fatal("valid enabled state did not persist marker")
	}
}

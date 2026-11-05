package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestProfilesDefaultOnly(t *testing.T) {
	p, err := OpenProfiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"claude", "codex", "antigravity", "gemini"} {
		if got := p.Names(model.Harness(h)); !slices.Equal(got, []string{DefaultName}) {
			t.Errorf("%s: %v", h, got)
		}
		if p.Default(model.Harness(h)) != DefaultName || p.Env(model.Harness(h), DefaultName) != nil || p.Home(model.Harness(h), "") != "" {
			t.Errorf("%s: the default account must keep the harness's usual home", h)
		}
	}
}

func TestProfilesAddRemoveDefault(t *testing.T) {
	root := t.TempDir()
	p, _ := OpenProfiles(root)
	if err := p.Add(Codex, "work"); err != nil {
		t.Fatal(err)
	}
	home := p.Home(Codex, "work")
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		t.Fatalf("home not made: %v", err)
	}
	if rel, _ := filepath.Rel(root, home); rel != filepath.Join("homes", "codex", "work") {
		t.Errorf("home %s", home)
	}
	if err := p.Add(Codex, "work"); !errors.Is(err, ErrExists) {
		t.Errorf("dup: %v", err)
	}
	if err := p.Add(Codex, DefaultName); !errors.Is(err, ErrExists) {
		t.Errorf("default: %v", err)
	}
	if err := p.SetDefault(Codex, "work"); err != nil {
		t.Fatal(err)
	}
	if n, _ := p.Resolve(Codex, ""); n != "work" {
		t.Errorf("resolve default: %s", n)
	}
	if n, _ := p.Resolve(Claude, ""); n != DefaultName {
		t.Errorf("other harness: %s", n)
	}
	if _, err := p.Resolve(Codex, "nope"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("unknown: %v", err)
	}
	if env := p.Env(Codex, "work"); !slices.Equal(env, []string{"CODEX_HOME=" + home}) {
		t.Errorf("env %v", env)
	}

	// Persisted, and read back.
	q, err := OpenProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.Names(Codex), []string{DefaultName, "work"}) || q.Default(Codex) != "work" {
		t.Errorf("reread: %v %s", q.Names(Codex), q.Default(Codex))
	}

	// Removing the default account of launches falls back to "default"; the home stays.
	if err := q.Remove(Codex, "work"); err != nil {
		t.Fatal(err)
	}
	if q.Default(Codex) != DefaultName || q.Has(Codex, "work") {
		t.Error("not removed")
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("home must stay (credentials are not deleted): %v", err)
	}
	if err := q.Remove(Codex, DefaultName); !errors.Is(err, ErrIsDefault) {
		t.Errorf("remove default: %v", err)
	}
}

// A Claude account's folder skips Claude's first-run questions, as the daemon's own does.
func TestProfilesClaudeOnboarded(t *testing.T) {
	p, _ := OpenProfiles(t.TempDir())
	if err := p.Add(Claude, "second"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(p.Home(Claude, "second"), ".claude.json"))
	if err != nil || !strings.Contains(string(b), "hasCompletedOnboarding") {
		t.Fatalf("%s %v", b, err)
	}
}

func TestProfilesNames(t *testing.T) {
	p, _ := OpenProfiles(t.TempDir())
	for _, bad := range []string{"", "Work", "../x", "a/b", `a\b`, "-x", "x y", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if err := p.Add(Claude, bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := p.Add("nope", "x"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown harness: %v", err)
	}
	for i := 1; i < MaxPerHarness; i++ {
		if err := p.Add(Claude, string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Add(Claude, "z"); !errors.Is(err, ErrTooMany) {
		t.Errorf("too many: %v", err)
	}
}

func TestProfilesIgnoresBadFile(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "profiles.json"),
		[]byte(`{"names":{"codex":["ok","../evil","default","ok"],"nope":["x"]},"defaults":{"codex":"missing","claude":"x"}}`), 0o600)
	p, err := OpenProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Names(Codex), []string{DefaultName, "ok"}) || p.Default(Codex) != DefaultName || p.Default(Claude) != DefaultName {
		t.Errorf("%v %s %s", p.Names(Codex), p.Default(Codex), p.Default(Claude))
	}
}

func TestHomeEnv(t *testing.T) {
	cases := map[string][]string{
		"claude":      {"CLAUDE_CONFIG_DIR=/h"},
		"codex":       {"CODEX_HOME=/h"},
		"antigravity": {"HOME=/h", "USERPROFILE=/h"},
		"gemini":      {"GEMINI_CLI_HOME=/h", "GEMINI_FORCE_FILE_STORAGE=true"},
		"opencode":    nil,
	}
	for h, want := range cases {
		if got := HomeEnv(model.Harness(h), "/h"); !slices.Equal(got, want) {
			t.Errorf("%s: %v", h, got)
		}
	}
	if HomeEnv(Claude, "") != nil {
		t.Error("empty home")
	}
}

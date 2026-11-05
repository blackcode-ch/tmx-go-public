package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestCheckConfigDir(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(t.TempDir(), "tmx", "claude-config")
	link := filepath.Join(t.TempDir(), "link")
	linked := os.MkdirAll(filepath.Join(home, ".claude"), 0o700) == nil && os.Symlink(filepath.Join(home, ".claude"), link) == nil
	for _, c := range []struct {
		name, dir string
		ok        bool
	}{
		{"unset", "", false},
		{"relative", "claude-config", false},
		{"home", home, false},
		{"home .claude", filepath.Join(home, ".claude"), false},
		{"home .claude, trailing", filepath.Join(home, ".claude") + string(filepath.Separator), false},
		{"state dir", state, true},
	} {
		err := checkConfigDir(c.dir, home)
		if (err == nil) != c.ok {
			t.Fatalf("%s (%q): %v", c.name, c.dir, err)
		}
		if err != nil && !strings.Contains(err.Error(), ".claude.json") {
			t.Fatalf("%s: unclear error %v", c.name, err)
		}
	}
	if linked {
		if err := checkConfigDir(link, home); err == nil {
			t.Fatal("a link to ~/.claude must be refused")
		}
	}
}

func TestConfigDirLastWins(t *testing.T) {
	env := []string{EnvConfigDir + "=/a", "X=1", EnvConfigDir + "=/b"}
	if got := ConfigDir(env); got != "/b" {
		t.Fatalf("got %q", got)
	}
	if got := ConfigRoot(env); got != "/b" {
		t.Fatalf("root %q", got)
	}
}

// An owner override in the daemon's own environment pointing at ~/.claude is refused, and so
// is a launch with no CLAUDE_CONFIG_DIR at all: claude never starts on ~/.claude.json.
func TestStartRefusesSharedConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfigDir, "")
	for _, env := range [][]string{nil, {EnvConfigDir + "=" + filepath.Join(home, ".claude")}, {EnvConfigDir + "=" + home}} {
		_, err := New().Start(context.Background(), runner.Spec{Command: os.Args[0], Cwd: t.TempDir(),
			Env: append(env, "TMXG_FAKE_CLAUDE=1")})
		if err == nil || !strings.Contains(err.Error(), EnvConfigDir) && !(runner.Elevated() && strings.Contains(err.Error(), "elevated")) {
			t.Fatalf("env %v: want refusal, got %v", env, err)
		}
	}
	t.Setenv(EnvConfigDir, filepath.Join(home, ".claude")) // the daemon's own env, inherited
	if _, err := New().Start(context.Background(), runner.Spec{Command: os.Args[0], Cwd: t.TempDir(),
		Env: []string{"TMXG_FAKE_CLAUDE=1"}}); err == nil {
		t.Fatal("an inherited ~/.claude must be refused")
	}
}

package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// assertIsolatedClaude: spec's claude keeps its state in the daemon's own folder under the
// state dir, never the owner's ~/.claude / ~/.claude.json, and the runners' guard accepts it.
func assertIsolatedClaude(t *testing.T, e *env, spec runner.Spec) {
	t.Helper()
	dir := claude.ConfigDir(spec.Env)
	if want := (config.Paths{Home: e.home}).ClaudeConfigDir(); dir != want {
		t.Fatalf("%s CLAUDE_CONFIG_DIR = %q, want %q", spec.Name, dir, want)
	}
	if rel, err := filepath.Rel(e.home, dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("CLAUDE_CONFIG_DIR %q is outside the state dir %q", dir, e.home)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if strings.EqualFold(filepath.Clean(dir), filepath.Join(home, ".claude")) || strings.EqualFold(filepath.Clean(dir), home) {
			t.Fatalf("CLAUDE_CONFIG_DIR is the owner's default: %q", dir)
		}
	}
	if err := claude.CheckConfigDir(claude.MergeEnv(claude.BaseEnv(), spec.Env...)); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAgentsGetIsolatedConfigDir(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	eventually(t, "head started", func() bool { return e.chat.Last(head.ID) != nil })
	w, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "w-cfg"})
	if err != nil {
		t.Fatal(err)
	}
	tw, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "t-cfg", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "workers started", func() bool { return e.chat.Last(w.ID) != nil && e.term.Last(tw.ID) != nil })
	for _, spec := range []runner.Spec{e.chat.Last(head.ID).Spec, e.chat.Last(w.ID).Spec, e.term.Last(tw.ID).Spec} {
		assertIsolatedClaude(t, e, spec)
		// The tmx MCP server's env (mcp.json) is the agent identity only.
		if b, err := os.ReadFile(spec.MCPConfigPath); err != nil || strings.Contains(string(b), claude.EnvConfigDir) {
			t.Fatalf("mcp.json %v: %s", err, b)
		}
		// The guard hook is still installed (--settings) beside it.
		if spec.SettingsPath == "" {
			t.Fatalf("%s lost its guard hook", spec.Name)
		}
	}
	// Restart keeps it.
	if _, err := f.RestartAgent(OwnerCaller, "w-cfg"); err != nil {
		t.Fatal(err)
	}
	assertIsolatedClaude(t, e, e.chat.Last(w.ID).Spec)

	// The folder is made private, with onboarding marked done and nothing else.
	dir := (config.Paths{Home: e.home}).ClaudeConfigDir()
	b, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil || cfg["hasCompletedOnboarding"] != true || len(cfg) != 1 {
		t.Fatalf(".claude.json: %v %s", err, b)
	}
}

func TestClaudeConfigSeedKeepsExisting(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	dir, err := p.EnsureClaudeConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(own, []byte(`{"userID":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnsureClaudeConfigDir(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(own); string(b) != `{"userID":"x"}` {
		t.Fatalf("existing .claude.json rewritten: %s", b)
	}
}

func TestCarryClaudeTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cwd := t.TempDir()
	a := model.Agent{Cwd: cwd, SessionID: "11111111-2222-4333-8444-555555555555"}
	old := harness.ClaudeProjectDir(home, cwd)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, a.SessionID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "claude-config")
	if claudeConversationExists(root, a) {
		t.Fatal("exists before carry")
	}
	carryClaudeTranscript(root, a)
	if !claudeConversationExists(root, a) {
		t.Fatal("conversation not carried into the isolated folder")
	}
	if _, err := os.Stat(filepath.Join(old, a.SessionID+".jsonl")); err != nil {
		t.Fatal("the owner's copy must stay")
	}
}

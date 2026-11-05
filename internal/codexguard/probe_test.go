package codexguard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Explicit opt-in: config inspection only, no authentication or model turn.
func TestLiveCodexConfigLayers(t *testing.T) {
	exe := os.Getenv("TMXG_CODEX_PROBE_EXE")
	if exe == "" {
		t.Skip("set TMXG_CODEX_PROBE_EXE to inspect a local native Codex without a model turn")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".codex", "config.toml"), []byte("notify=['tmx_fixture_never_run']\n[mcp_servers.foreign]\ncommand='tmx_fixture_never_run'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := NewConfig(filepath.Join(root, "home"), work, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var env []string
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATH"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "CODEX_HOME="+l.Home)
	if err := ProbeEffective(context.Background(), exe, []string{"app-server", "--strict-config"}, env, work, ""); err != nil {
		t.Fatal(err)
	}
}

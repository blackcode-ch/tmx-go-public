package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/codexguard"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestLiveCodexManagedMCPConfig(t *testing.T) {
	exe := os.Getenv("TMXG_CODEX_PROBE_EXE")
	if exe == "" {
		t.Skip("explicit no-model Codex configuration probe")
	}
	root := t.TempDir()
	l, err := codexguard.NewConfig(filepath.Join(root, "home"), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	tmx := filepath.Join(root, "tmx-fixture.exe")
	spec := runner.Spec{Cwd: root, CodexGuardHome: l.Home, MCP: &runner.MCPServer{Command: tmx, Args: []string{"mcp"}, EnvKeys: []string{"TMXG_URL", "TMXG_AGENT_ID", "TMXG_AGENT_TOKEN"}}}
	var env []string
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATH"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "CODEX_HOME="+l.Home)
	if err := codexguard.ProbeEffective(context.Background(), exe, CodexAppServerArgs(spec), env, root, tmx); err != nil {
		t.Fatal(err)
	}
}

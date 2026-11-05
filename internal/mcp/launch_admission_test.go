package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestMCPLaunchAdmissionAcrossHarnesses(t *testing.T) {
	d := apitest.New(t)
	cs := session(t, d, "head")
	home, system := t.TempDir(), t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("SystemRoot", system)
	for _, h := range []model.Harness{model.HarnessClaude, model.HarnessCodex, model.HarnessOpenCode, model.HarnessPi, model.HarnessShell} {
		for _, cwd := range []string{home, system, filepath.Join(system, "new-child")} {
			_, isErr := call(t, cs, "agent_launch", map[string]any{"name": "unsafe", "harness": h, "cwd": cwd})
			if !isErr {
				t.Fatalf("%s %s: unsafe MCP launch accepted", h, cwd)
			}
		}
	}
	if _, err := d.Fleet.Agent("unsafe"); err == nil {
		t.Fatal("rejected agent persisted")
	}
}

// agent_launch's account reaches the launch (this test daemon has no second account).
func TestMCPLaunchAccount(t *testing.T) {
	d := apitest.New(t)
	cs := session(t, d, "head")
	if out, isErr := call(t, cs, "agent_launch", map[string]any{"name": "acct-bad", "account": "nope"}); !isErr || !strings.Contains(out, `no account "nope"`) {
		t.Fatalf("unknown account: %v %s", isErr, out)
	}
	if _, err := d.Fleet.Agent("acct-bad"); err == nil {
		t.Fatal("rejected agent persisted")
	}
}

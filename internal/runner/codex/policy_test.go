package codex

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestThreadSandboxPolicy(t *testing.T) {
	for _, test := range []struct {
		mode string
		full bool
		want string
	}{
		{"", false, "workspace-write"},
		{"bypassPermissions", false, "workspace-write"},
		{"", true, "danger-full-access"},
		{"plan", true, "read-only"},
	} {
		// CodexWindowsSandbox: the same mapping on every OS (on Windows the sandbox is otherwise
		// off: harness.TestCodexWindowsSandboxOffByDefault).
		params := threadParams(runner.Spec{PermissionMode: test.mode, CodexFullAccess: test.full, CodexWindowsSandbox: true, Cwd: "/work/agent"})
		if params["sandbox"] != test.want || params["cwd"] != "/work/agent" {
			t.Fatalf("wrong thread policy: %v", params)
		}
	}
}

//go:build !windows || tmx_ci

// On Windows only in CI (-tags tmx_ci), never on a dev PC: this file carries dangerous command
// strings (kill-by-name), the class Defender flagged on 3 Nov. Matched as data; nothing is run.

package guardhook

import (
	"context"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
)

// TestFromManifestIgnoresHostileEnv is go-reviewer-o's #143 follow-up: a Codex
// shim is the agent's own child process, so TMXG_GUARD_FOLDER in its
// environment can be anything the agent wants. FromManifest must still build
// the guard's Own/Workspace/State from the manifest tmx pinned at launch, not
// from that environment, so a shim run with a hostile, overly wide
// TMXG_GUARD_FOLDER still holds or blocks a delete outside the manifest's
// actual (narrow) agent folder.
func TestFromManifestIgnoresHostileEnv(t *testing.T) {
	t.Setenv(guard.EnvFolder, `C:\`)
	t.Setenv(guard.EnvWorkspace, `C:\`)
	t.Setenv(guard.EnvState, `C:\`)
	o := FromManifest(`C:\work\project`, `C:\work\project`, `C:\state`)
	if o.Context.Own != `C:\work\project` {
		t.Fatalf("Own leaked from env: got %q", o.Context.Own)
	}
	if err := AuthorizeExec(context.Background(), "rm", []string{"-rf", `C:\Users\owner\Documents`}, `C:\work\project`, o); err == nil {
		t.Fatal("delete outside the manifest's own folder was allowed despite a hostile TMXG_GUARD_FOLDER")
	}
}

func TestExecLiteralArgsAndFailClosedHold(t *testing.T) {
	o := Options{Context: guard.Context{OS: "windows", Own: `C:\work\project`}}
	for _, test := range []struct {
		name    string
		command string
		args    []string
		denied  bool
	}{
		{"ordinary", "git", []string{"status"}, false},
		{"literal", "git", []string{"commit", "-m", "do not run taskkill /IM node.exe"}, false},
		{"block", "taskkill", []string{"/IM", "node.exe"}, true},
		{"full path block", `C:\Windows\System32\taskkill.exe`, []string{"/IM", "node.exe"}, true},
		{"wrapped full path block", `C:\Windows\System32\cmd.exe`, []string{"/c", "taskkill /IM node.exe"}, true},
		{"hold without daemon", "sudo", []string{"true"}, true},
		{"missing command", "", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := AuthorizeExec(context.Background(), test.command, test.args, o.Context.Own, o)
			if (err != nil) != test.denied {
				t.Fatalf("err=%v, denied=%v", err, test.denied)
			}
		})
	}
}

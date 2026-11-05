//go:build !windows || tmx_ci

// On Windows only in CI (-tags tmx_ci), never on a dev PC: this file carries a permission-change
// command string. It is matched as a string only; nothing is run.

package fleet

import (
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
)

// go-reviewer-o4, #241: a composite call whose holds include a non-rememberable rule is decided
// by that rule, so an Allow of it is never remembered and the same call brings a card again.
func TestCompositeCallIsNotRemembered(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	me := callerFor(t, f, e.chat, head.ID)

	const cmd = "cp build.zip ../out/ && chmod 700 /etc/x"
	ctx := guard.Context{OS: "linux", Home: "/home/dev", Own: "/home/dev/work/app", Workspace: "/home/dev/work",
		StateDir: "/home/dev/.local/state/tmx-go", Temp: []string{"/tmp"}, States: f.GuardStates()}
	v := guard.CheckCommand(ctx, guard.ShellPOSIX, cmd)
	if v.Outcome != guard.Hold || v.Rule != guard.RulePermsOutside {
		t.Fatalf("verdict: %+v", v)
	}
	req := HoldRequest{Tool: "Bash", Command: cmd, Rule: v.Rule, Cwd: "/home/dev/work/app"}
	first, err := f.Hold(me, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Decide(OwnerCaller, first.ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.Hold(me, req); again.State != model.ApprovalPending {
		t.Fatalf("the composite call was remembered: %+v", again)
	}
}

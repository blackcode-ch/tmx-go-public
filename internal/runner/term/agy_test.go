package term

import (
	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/term/screen"
	"testing"
)

func TestAgyComposerPreservesDraft(t *testing.T) {
	for _, tc := range []struct {
		text  string
		empty bool
	}{
		{"╭────────╮\n│ >      │\n╰────────╯\n? for shortcuts", true},
		{"╭────────╮\n│ >      │\n│ draft  │\n╰────────╯\n? for shortcuts", false},
		{"past >\n? for shortcuts", false},
	} {
		if got := emptyComposer(tc.text, "agy"); got != tc.empty {
			t.Fatalf("%q: %v", tc.text, got)
		}
	}
}

// This is a synthetic permission-screen fixture using the Python adapter's
// verified numbered menu shape. It proves tmx's routing, not agy's auto policy.
func TestAgyDestructiveDialogReachesApprovalPath(t *testing.T) {
	p, bp := newGuardedTestProcess(t)
	p.w.info, _ = harness.Get("agy")
	p.w.scr.Write([]byte("Allow deletion of the selected folder?\r\n> 1. Yes, allow access\r\n  2. No, deny access\r\n"))
	d := p.w.classify()
	if d.State != screen.Approval {
		t.Fatalf("state: %s", d.State)
	}
	p.mu.Lock()
	for i := 0; i < menuSettle; i++ {
		p.w.menuLocked(d)
	}
	p.mu.Unlock()
	var approval *runner.ApprovalRequest
	for len(p.events) > 0 {
		e := <-p.events
		if e.Kind == runner.EvApproval {
			approval = e.Approval
		}
	}
	if approval == nil || approval.Kind != model.ApprovalPermission || approval.Tool != model.ToolTerminalMenu || p.state != model.StateWaiting {
		t.Fatalf("not held for owner: %+v", approval)
	}
	close(bp.release)
	if err := p.Write([]byte("1\r"), false); err != runner.ErrApprovalPending && err != runner.ErrOwnerOnly {
		t.Fatalf("agent answered its own prompt: %v", err)
	}
	select {
	case <-bp.writes:
		t.Fatal("permission keys reached process without owner")
	default:
	}
	if keys, err := screen.PlanAnswer(d, "deny", p.w.info.Synonyms); err != nil || keys != "2 enter" {
		t.Fatalf("deny: %q %v", keys, err)
	}
}

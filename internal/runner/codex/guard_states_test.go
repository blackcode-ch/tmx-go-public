package codex

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// A running Codex agent applies the owner's CURRENT rule switches (reviewer, #232): the parser
// reads them on every check, so a flip to Blocked applies without a restart.
func TestCodexParserFollowsRuleSwitches(t *testing.T) {
	state := "hold"
	check := func() (blocked, held bool) {
		p := parser{log: slog.New(slog.NewTextHandler(io.Discard, nil)), pending: map[string]pendingReq{}, items: map[string]wireItem{},
			guardContext: &guard.Context{OS: "windows", Cwd: `C:\work\project`, Own: `C:\work\project`},
			guardStates:  func() map[string]string { return map[string]string{guard.RuleElevation: state} }}
		params, _ := json.Marshal(map[string]string{"command": "sudo true", "cwd": `C:\work\project`})
		var o out
		p.serverRequest(&o, wireMsg{ID: json.RawMessage("17"), Method: mCommandApproval, Params: params})
		for _, ev := range o.events {
			if ev.Kind == runner.EvApproval {
				held = true
			}
		}
		return len(o.replies) == 1 && !held, held
	}
	if blocked, held := check(); blocked || !held {
		t.Fatalf("held state: blocked=%v held=%v", blocked, held)
	}
	state = "block"
	if blocked, _ := check(); !blocked {
		t.Fatal("a flip to Blocked did not apply to the running agent")
	}
}

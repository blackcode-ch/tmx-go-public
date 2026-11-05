package codex

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestWindowsGuardApprovalRequests(t *testing.T) {
	for _, test := range []struct {
		name, command string
		blocked       bool
	}{
		{"block", `C:\Windows\System32\taskkill.exe /IM node.exe`, true},
		{"held", "sudo true", false},
		{"allow", "git status", false},
		{"empty", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := parser{log: slog.New(slog.NewTextHandler(io.Discard, nil)), pending: map[string]pendingReq{}, items: map[string]wireItem{}, guardContext: &guard.Context{OS: "windows", Cwd: `C:\work\project`, Own: `C:\work\project`}}
			params, _ := json.Marshal(map[string]string{"command": test.command, "cwd": `C:\work\project`})
			var out out
			p.serverRequest(&out, wireMsg{ID: json.RawMessage("17"), Method: mCommandApproval, Params: params})
			if test.blocked {
				if len(out.replies) != 1 || len(p.pending) != 0 {
					t.Fatal("blocked command was not denied")
				}
				for _, event := range out.events {
					if event.Kind == runner.EvApproval {
						t.Fatal("block became approvable")
					}
				}
			} else {
				found := false
				for _, event := range out.events {
					if event.Kind == runner.EvApproval {
						found = true
					}
				}
				if !found || p.state != model.StateWaiting {
					t.Fatal("hold did not reach owner approval path")
				}
			}
		})
	}
}

func TestGuardConfigMustPassBeforeThreadStarts(t *testing.T) {
	p := parser{log: slog.New(slog.NewTextHandler(io.Discard, nil)), initID: "init", guardExecutable: "tmx.exe", nextID: func() string { return "config" }, threadParams: map[string]any{"cwd": "work"}}
	var result out
	p.response(&result, wireMsg{ID: json.RawMessage(`"init"`), Result: json.RawMessage(`{}`)})
	if len(result.replies) != 2 || p.threadReqID != "" {
		t.Fatal("thread started before config check")
	}
	result = out{}
	p.response(&result, wireMsg{ID: json.RawMessage(`"config"`), Result: json.RawMessage(`{"config":{}}`)})
	if !result.close || p.threadReqID != "" {
		t.Fatal("unverified config started thread")
	}
}

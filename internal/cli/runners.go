package cli

import (
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
	"github.com/blackcode-ch/tmx-go/internal/runner/codex"
	"github.com/blackcode-ch/tmx-go/internal/runner/term"
)

// runners wires the real runners into the daemon's fleet. Chat mode dispatches by harness
// (Claude's stream-json, Codex's app-server); terminal mode runs any harness's TUI or a shell.
func runners() map[model.Mode]runner.Runner {
	return map[model.Mode]runner.Runner{
		model.ModeChat:     runner.ByHarness{model.HarnessClaude: claude.New(), model.HarnessCodex: codex.New()},
		model.ModeTerminal: term.New(),
	}
}

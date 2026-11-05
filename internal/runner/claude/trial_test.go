package claude

import (
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"testing"
)

func TestTrialDoesNotInheritPythonHooks(t *testing.T) {
	args := TerminalArgs(runner.Spec{SettingsPath: "go-settings.json"})
	for i, s := range args {
		if s == "--setting-sources" && i+1 < len(args) && args[i+1] == "" {
			return
		}
	}
	t.Fatalf("user settings not disabled: %v", args)
}

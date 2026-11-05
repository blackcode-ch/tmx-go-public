package harness

import (
	"slices"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

func TestGeminiTerminalCommand(t *testing.T) {
	info, ok := Get(model.HarnessGemini)
	if !ok || info.Label != "Gemini CLI" || !slices.Equal(info.commands, []string{"gemini"}) {
		t.Fatalf("%+v", info)
	}
	for _, tc := range []struct {
		spec runner.Spec
		want []string
	}{
		{runner.Spec{InitialPrompt: "hello", Model: "gemini-3-pro"}, []string{"--model", "gemini-3-pro", "-i", "hello"}},
		{runner.Spec{PermissionMode: "plan"}, []string{"--approval-mode", "plan"}},
		{runner.Spec{PermissionMode: "bypassPermissions"}, []string{"--approval-mode", "auto_edit"}},
		{runner.Spec{}, nil},
	} {
		tc.spec.Harness, tc.spec.Command = model.HarnessGemini, "/test/gemini"
		c, err := TerminalCommand(tc.spec)
		if err != nil || !slices.Equal(c.Args, tc.want) {
			t.Errorf("%+v: %v %v", tc.spec, c.Args, err)
		}
	}
}

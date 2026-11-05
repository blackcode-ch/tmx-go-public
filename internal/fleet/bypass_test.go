package fleet

import (
	"encoding/json"
	"os"
	"testing"
)

// The owner, 3 Nov 13:54: no per-tool "allow once" prompts from Claude agents. A Claude agent
// with tmx's guard hook runs in bypass mode (like the Python fleet), the hook alone decides
// what reaches the owner; config.json "agent_bypass": false keeps Claude's own prompts.
func TestGuardedClaudeAgentsRunInBypassMode(t *testing.T) {
	old := launchGOOS
	launchGOOS = "linux"
	t.Cleanup(func() { launchGOOS = old })

	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	for _, name := range []string{"head", "alpha-lead"} {
		spec := e.chat.Last(mustAgent(t, f, name).ID).Spec
		if spec.PermissionMode != "bypassPermissions" || spec.SettingsPath == "" {
			t.Fatalf("%s: mode %q, settings %q", name, spec.PermissionMode, spec.SettingsPath)
		}
		b, err := os.ReadFile(spec.SettingsPath)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if json.Unmarshal(b, &cfg) != nil || cfg["skipDangerousModePermissionPrompt"] != true || cfg["hooks"] == nil {
			t.Fatalf("%s settings: %s", name, b)
		}
	}

	off := false
	e2 := newEnv(t)
	e2.bypass = &off
	f2 := e2.started()
	spec := e2.chat.Last(mustAgent(t, f2, "head").ID).Spec
	if spec.PermissionMode != "auto" || spec.SettingsPath == "" {
		t.Fatalf("agent_bypass off: mode %q", spec.PermissionMode)
	}
	b, _ := os.ReadFile(spec.SettingsPath)
	var cfg map[string]any
	if json.Unmarshal(b, &cfg) != nil || cfg["skipDangerousModePermissionPrompt"] != nil {
		t.Fatalf("off still skips the prompt: %s", b)
	}
}

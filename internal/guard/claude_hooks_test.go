package guard

import (
	"encoding/json"
	"testing"
)

// tmx-lead, 3 Nov 22:37: Claude Code settings stay Allowed, but an agent can't switch off its own
// guard. The file tools are judged by their change; shell writes are in claude_hooks_cmd_test.go.
func TestClaudeSettingsCannotSwitchOffTheGuard(t *testing.T) {
	ctx := Context{OS: "linux", Home: "/home/dev", Own: "/home/dev/work/app", Workspace: "/home/dev/work",
		StateDir: "/home/dev/.local/state/tmx-go", Temp: []string{"/tmp"}, States: DefaultStates()}
	call := func(tool string, in map[string]any) Verdict {
		b, _ := json.Marshal(in)
		return Check(ctx, Call{Tool: tool, Input: b})
	}
	proj := "/home/dev/work/app/.claude/settings.local.json"
	user := "/home/dev/.claude/settings.json"
	agentCfg := "/home/dev/.local/state/tmx-go/claude-config/settings.json"
	acctCfg := "/home/dev/.local/state/tmx-go/ai-accounts/homes/claude/second/settings.json"
	for _, tc := range []struct {
		name, tool string
		in         map[string]any
		blocked    bool
	}{
		{"disableAllHooks by Edit", "Edit", map[string]any{"file_path": proj, "old_string": `"model": "x"`, "new_string": `"model": "x", "disableAllHooks": true`}, true},
		{"delete the hooks block", "Edit", map[string]any{"file_path": user, "old_string": `"hooks": {"PreToolUse": []},`, "new_string": ""}, true},
		{"alter a PreToolUse entry", "Edit", map[string]any{"file_path": user, "old_string": `"matcher": ".*"`, "new_string": `"matcher": "Read", "PreToolUse": 1`}, true},
		{"tmx endpoint in MultiEdit", "MultiEdit", map[string]any{"file_path": proj, "edits": []map[string]string{{"old_string": "a", "new_string": "http://127.0.0.1:8787/api/guard/holds"}}}, true},
		{"Write with disableAllHooks", "Write", map[string]any{"file_path": user, "content": `{"disableAllHooks": true}`}, true},
		{"the agents' CLAUDE_CONFIG_DIR", "Edit", map[string]any{"file_path": agentCfg, "old_string": "{", "new_string": `{"disableAllHooks": true,`}, true},
		{"a Claude account's CLAUDE_CONFIG_DIR", "Edit", map[string]any{"file_path": acctCfg, "old_string": "{", "new_string": `{"disableAllHooks": true,`}, true},
		{"a permissions change", "Edit", map[string]any{"file_path": proj, "old_string": `"allow": []`, "new_string": `"allow": ["Bash(ls)"]`}, false},
		{"a whole new file without hooks", "Write", map[string]any{"file_path": user, "content": `{"model": "opus"}`}, false},
	} {
		v := call(tc.tool, tc.in)
		if tc.blocked && (v.Outcome != Block || v.Rule != RuleClaudeHooks) {
			t.Errorf("%s: got %s/%s, want the claude-hooks block", tc.name, v.Outcome, v.Rule)
		}
		if !tc.blocked && v.Outcome == Block {
			t.Errorf("%s: blocked (%s)", tc.name, v.Rule)
		}
	}
	if r, _ := RuleByID(RuleClaudeHooks); !r.Fixed {
		t.Fatal("the guard-hook carve-out has a switch")
	}
}

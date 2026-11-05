package guardhook

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Shared by every test file here, including browser_test.go, which builds everywhere.

func input(tool string, in map[string]any) string {
	ti, _ := json.Marshal(in)
	b, _ := json.Marshal(Input{SessionID: "s", Cwd: "", HookEventName: "PreToolUse", PermissionMode: "bypassPermissions",
		ToolName: tool, ToolInput: ti, ToolUseID: "toolu_1"})
	return string(b)
}

func run(t *testing.T, o Options, stdin string) *Decision {
	t.Helper()
	var out bytes.Buffer
	if err := Run(context.Background(), strings.NewReader(stdin), &out, o); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return nil
	}
	var res Output
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("hook output is not JSON: %q", out.String())
	}
	if res.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("event name: %+v", res)
	}
	return &res.HookSpecificOutput
}

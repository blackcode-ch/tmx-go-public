package guardhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// AuthorizeExec reuses the hook's exact-command owner hold protocol for native
// argv. It does not execute a child or turn argv into shell source.
func AuthorizeExec(ctx context.Context, command string, args []string, cwd string, o Options) error {
	input, err := json.Marshal(map[string]any{"command": command, "args": args})
	if err != nil {
		return err
	}
	call, err := json.Marshal(Input{Cwd: cwd, ToolName: "tmx_exec", ToolInput: input})
	if err != nil {
		return err
	}
	var response bytes.Buffer
	if err := Run(ctx, bytes.NewReader(call), &response, o); err != nil {
		return err
	}
	if response.Len() == 0 {
		return nil
	}
	var result Output
	if err := json.Unmarshal(response.Bytes(), &result); err != nil {
		return fmt.Errorf("invalid guard response")
	}
	if result.HookSpecificOutput.PermissionDecision != "allow" {
		return fmt.Errorf("%s", result.HookSpecificOutput.PermissionDecisionReason)
	}
	return nil
}

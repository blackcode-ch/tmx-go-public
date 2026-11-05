package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Fleet operations for the head and the leads (api/fleetops.go): what an order from the
// fleet's leadership needs, without the owner's token.

type logArgs struct {
	Lines int `json:"lines,omitempty" jsonschema:"how many of the last lines (default 100, max 500)"`
}

func (t *Tools) registerFleetOps(s *sdk.Server) {
	sdk.AddTool(s, &sdk.Tool{Name: "owner_status", Description: "Head and leads: this machine's paired browser devices (id, name, created, expires), whether an owner PIN is set (never the PIN), and the owner gate's status. Read only."}, t.ownerStatus)
	sdk.AddTool(s, &sdk.Tool{Name: "daemon_log", Description: "Head and leads: the last lines of this machine's tmx daemon log, secrets redacted. Read only."}, t.daemonLog)
	sdk.AddTool(s, &sdk.Tool{Name: "daemon_restart", Description: "Head and leads: restart this machine's tmx daemon gracefully (it starts itself again, about 15 seconds; every agent's process restarts with it). Use it when the fleet's leadership orders it or the daemon misbehaves."}, t.daemonRestart)
}

func (t *Tools) ownerStatus(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	var v json.RawMessage
	if err := t.c.Get(ctx, "/api/fleet-ops/status", &v); err != nil {
		return nil, nil, err
	}
	return text(string(v)), nil, nil
}

func (t *Tools) daemonLog(ctx context.Context, _ *sdk.CallToolRequest, a logArgs) (*sdk.CallToolResult, any, error) {
	var v struct {
		Lines []string `json:"lines"`
	}
	if err := t.c.Get(ctx, fmt.Sprintf("/api/fleet-ops/log?lines=%d", a.Lines), &v); err != nil {
		return nil, nil, err
	}
	if len(v.Lines) == 0 {
		return text("The daemon log is empty."), nil, nil
	}
	return text(strings.Join(v.Lines, "\n")), nil, nil
}

func (t *Tools) daemonRestart(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	if err := t.c.Post(ctx, "/api/fleet-ops/restart", map[string]any{}, nil); err != nil {
		return nil, nil, err
	}
	return text("The daemon is restarting; it answers again in about 15 seconds. Your own process restarts with it."), nil, nil
}

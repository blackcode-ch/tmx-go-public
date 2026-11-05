package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (t *Tools) registerProcesses(s *sdk.Server) {
	sdk.AddTool(s, &sdk.Tool{Name: "process_start", Description: "Start an owned background command in your workspace. Ordinary commands start immediately; existing guard holds require owner approval. It stops with you unless explicitly promoted."}, t.processStart)
	sdk.AddTool(s, &sdk.Tool{Name: "processes", Description: "List your owned tools and current harness child processes, including purpose, PID, executable summary and lifetime owner. Owned tools stop with you unless the owner explicitly promotes them to a project."}, t.processes)
	sdk.AddTool(s, &sdk.Tool{Name: "process_stop", Description: "Stop one of your registered tools by its registry ID. Observed harness children and System tools cannot be stopped here."}, t.processStop)
}

func (t *Tools) processStart(ctx context.Context, _ *sdk.CallToolRequest, in fleet.ProcessRequest) (*sdk.CallToolResult, any, error) {
	var ap fleet.ProcessResult
	if err := t.c.Post(ctx, "/api/processes/requests", in, &ap); err != nil {
		return nil, nil, err
	}
	if ap.Approval != nil {
		return text("Waiting for owner approval " + ap.Approval.ID + "; no process has started."), nil, nil
	}
	if ap.Process == nil {
		return nil, nil, fmt.Errorf("missing process result")
	}
	return text("Started process " + ap.Process.ID + "; use processes to inspect it."), nil, nil
}
func (t *Tools) processes(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
	var rows []model.ProcessInfo
	if err := t.c.Get(ctx, "/api/agents/"+client.Seg(t.agentID)+"/processes", &rows); err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return nil, nil, err
	}
	return untrusted(string(b)), nil, nil
}

type processStopArgs struct {
	ID string `json:"id" jsonschema:"registered tool ID from processes"`
}

func (t *Tools) processStop(ctx context.Context, _ *sdk.CallToolRequest, in processStopArgs) (*sdk.CallToolResult, any, error) {
	if err := t.c.Post(ctx, "/api/processes/"+client.Seg(in.ID)+"/stop", nil, nil); err != nil {
		return nil, nil, err
	}
	return text("Process stopped."), nil, nil
}

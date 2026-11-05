package api

import (
	"encoding/json"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestProcessArgumentsStayWithRequester(t *testing.T) {
	ap := model.Approval{AgentID: "a", Tool: fleet.ToolProcessStart, Input: json.RawMessage(`{"args":["private-value"]}`)}
	snap := model.Snapshot{Approvals: []model.Approval{ap}}
	if len(agentSnapshot(fleet.AgentCaller("b"), snap).Approvals) != 0 {
		t.Fatal("another agent sees command approval")
	}
	if len(agentSnapshot(fleet.AgentCaller("a"), snap).Approvals) != 1 {
		t.Fatal("requester lost approval")
	}
	if len(agentSnapshot(fleet.OwnerCaller, snap).Approvals) != 1 {
		t.Fatal("owner lost approval")
	}
	if _, ok := agentEvent(fleet.AgentCaller("b"), model.BusEvent{Data: ap}); ok {
		t.Fatal("another agent receives command approval event")
	}
	e := model.TimelineEntry{AgentID: "a", Kind: model.EntryToolUse, Tool: "mcp__tmxg__process_start", Input: ap.Input}
	ev, ok := agentEvent(fleet.AgentCaller("b"), model.BusEvent{Data: e})
	if !ok {
		t.Fatal("timeline event lost")
	}
	if string(ev.Data.(model.TimelineEntry).Input) != "{}" {
		t.Fatal("timeline leaked command arguments")
	}
}

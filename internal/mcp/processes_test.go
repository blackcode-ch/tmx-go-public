package mcp

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMCPProcessHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "mcp-process-helper" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestProcessToolsStartWithoutApproval(t *testing.T) {
	d := apitest.New(t)
	cs := session(t, d, "head")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out := mustOK(t, cs, "process_start", map[string]any{"purpose": "owned test", "command": exe, "args": []string{"-test.run=^TestMCPProcessHelper$", "--", "mcp-process-helper"}})
		if !strings.Contains(out, "Started process") {
			t.Fatal(out)
		}
	}
	aps, err := d.Fleet.Approvals(model.ApprovalPending)
	if err != nil || len(aps) != 0 {
		t.Fatalf("approval cards: %v %v", aps, err)
	}
	rows := d.Fleet.ProcessTools()
	if len(rows) != 2 || rows[0].PID == 0 || rows[1].PID == 0 {
		t.Fatalf("missing running tools: %+v", rows)
	}
	if _, err := d.Fleet.StopAgent(fleet.OwnerCaller, "head"); err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Fleet.ProcessTools() {
		if p.PID != 0 {
			t.Fatalf("surviving tool: %+v", p)
		}
	}
}

func TestProcessToolsRequestWithoutExecution(t *testing.T) {
	d := apitest.New(t)
	cs := session(t, d, "head")
	mustOK(t, cs, "processes", nil)
	out := mustOK(t, cs, "process_start", map[string]any{"purpose": "test command", "command": "sudo", "args": []string{"true"}})
	if !strings.Contains(out, "no process has started") {
		t.Fatal(out)
	}
	aps, err := d.Fleet.Approvals(model.ApprovalPending)
	if err != nil || len(aps) != 1 {
		t.Fatalf("approval missing: %v %v", aps, err)
	}
	if _, failed := call(t, cs, "process_stop", map[string]any{"id": "missing"}); !failed {
		t.Fatal("unknown tool stopped")
	}
}

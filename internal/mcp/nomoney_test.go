package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
)

// Dollar amounts are never shown to an agent (or by `tmx ls`): plan usage is the accounts page.
func TestNoMoneyInFleetOrStatus(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")
	d.Proc(t, "head").Turn("done", 1.23, 2) // a turn that cost $1.23
	deadline := time.Now().Add(5 * time.Second)
	for {
		if a, _ := d.Fleet.Agent("head"); a.CostUSD > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a, _ := d.Fleet.Agent("head"); a.CostUSD != 1.23 {
		t.Fatalf("the cost is still kept: %v", a.CostUSD)
	}
	snap, err := d.Fleet.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"fleet":        mustOK(t, head, "fleet", nil),
		"agent_status": mustOK(t, head, "agent_status", map[string]any{"name": "head"}),
		"FormatTree":   FormatTree(snap),
	} {
		if strings.Contains(out, "$") || strings.Contains(out, "1.23") {
			t.Errorf("%s shows money: %s", name, out)
		}
	}
}

package mcp

import (
	"strings"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
)

func TestProjectSectionTool(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")
	mustOK(t, head, "project_create", map[string]any{"name": "Daily report"})
	lead := session(t, d, "daily-report-lead")
	out := mustOK(t, lead, "announce_set", map[string]any{"project": "daily-report", "status": "Ready", "needs": []string{"Review"}, "links": []string{"Guide | https://example.test/guide"}})
	if !strings.Contains(out, "Ready") || !strings.Contains(out, "Guide") {
		t.Fatal(out)
	}
	if _, bad := call(t, lead, "announce_set", map[string]any{"project": "daily-report", "status": "1\n2\n3\n4"}); !bad {
		t.Fatal("accepted fourth line")
	}
	// Earlier names (private build) are the same tool.
	for _, old := range LegacyToolNames["announce_set"] {
		if out := mustOK(t, lead, old, map[string]any{"project": "daily-report", "status": "Via " + old}); !strings.Contains(out, "Via "+old) {
			t.Fatalf("%s: %s", old, out)
		}
	}
}

func TestContributionAndArtifactsTools(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")
	mustOK(t, head, "project_create", map[string]any{"name": "Artifacts"})
	lead := session(t, d, "artifacts-lead")
	outputs := []map[string]string{{"path": "https://example.test/result", "what": "Result"}}
	mustOK(t, lead, "my_part", map[string]any{"project": "artifacts", "status": "Ready", "final_output": outputs})
	mustOK(t, lead, "announce_set", map[string]any{"project": "artifacts", "final_output": outputs})
	out := mustOK(t, lead, "project_artifacts", map[string]any{"project": "artifacts"})
	if !strings.Contains(out, `"kind":"link"`) || !strings.Contains(out, "Ready") {
		t.Fatal(out)
	}
	if _, bad := call(t, head, "my_part", map[string]any{"project": "artifacts", "status": "Overwrite"}); !bad {
		t.Fatal("nonmember wrote contribution")
	}
}

func TestProjectCreateSelectsExistingLeadOrNone(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")
	mustOK(t, head, "project_create", map[string]any{"name": "Original"})
	out := mustOK(t, head, "project_create", map[string]any{"name": "Shared", "lead": "original-lead"})
	if !strings.Contains(out, "assigned lead original-lead") {
		t.Fatalf("existing lead: %s", out)
	}
	out = mustOK(t, head, "project_create", map[string]any{"name": "Unassigned", "lead": ""})
	if !strings.Contains(out, "no lead assigned") {
		t.Fatalf("no lead: %s", out)
	}
	if _, err := d.Fleet.Agent("shared-lead"); err == nil {
		t.Fatal("launched replacement lead")
	}
	if _, err := d.Fleet.Agent("unassigned-lead"); err == nil {
		t.Fatal("launched unassigned lead")
	}
}

package mcp

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"strings"
	"testing"
)

func TestProjectImportTool(t *testing.T) {
	d := apitest.New(t)
	head := session(t, d, "head")
	mustOK(t, head, "project_create", map[string]any{"name": "Imported"})
	lead := session(t, d, "imported-lead")
	args := map[string]any{"project": "imported", "import": map[string]any{"id": "batch", "source": "python@hostb", "section": map[string]any{"status": "Ready", "needs": []any{map[string]any{"text": "Checked", "done": true}}, "updated_at": 1000, "updated_by": "legacy"}, "cards": []any{map[string]any{"id": "old-card", "question": "Choose?", "created_at": 900}}}}
	out := mustOK(t, lead, "project_import", args)
	if !strings.Contains(out, "import-") || !strings.Contains(out, "legacy") {
		t.Fatal(out)
	}
	mustOK(t, lead, "project_import", args)
	if _, bad := call(t, head, "project_import", args); !bad {
		t.Fatal("unrelated head imported")
	}
}

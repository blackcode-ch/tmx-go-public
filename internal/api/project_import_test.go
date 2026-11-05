package api_test

import (
	"github.com/blackcode-ch/tmx-go/internal/api/apitest"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"testing"
)

func TestProjectImportHTTP(t *testing.T) {
	d := apitest.New(t)
	p, err := d.Fleet.CreateProject(fleet.OwnerCaller, fleet.ProjectRequest{Name: "Imported"})
	if err != nil {
		t.Fatal(err)
	}
	owner := client{t: t, base: d.URL, token: d.OwnerToken}
	lead := client{t: t, base: d.URL, token: d.AgentToken(t, "imported-lead")}
	foreign := client{t: t, base: d.URL, token: d.AgentToken(t, "head")}
	req := map[string]any{"id": "batch", "source": "python@hostb", "section": map[string]any{"status": "Ready", "needs": []any{map[string]any{"text": "Reviewed", "done": true}}, "preview_dir": "C:/legacy/preview", "updated_at": 1000, "updated_by": "legacy-author"}, "cards": []any{map[string]any{"id": "old-question", "question": "Choose?", "created_at": 900}}}
	path := "/api/projects/" + p.ID + "/import"
	if s := foreign.do("POST", path, req, nil); s != 403 {
		t.Fatalf("foreign import: %d", s)
	}
	var rec model.ProjectImportReceipt
	if s := lead.do("POST", path, req, &rec); s != 200 || len(rec.CardIDs) != 1 {
		t.Fatalf("import: %d %+v", s, rec)
	}
	var sec model.ProjectSection
	if s := owner.do("GET", "/api/projects/"+p.ID+"/section", nil, &sec); s != 200 || !sec.Needs[0].Done || sec.PreviewDir != "C:/legacy/preview" {
		t.Fatalf("page: %d %+v", s, sec)
	}
	answer := map[string]any{"decision": "answer", "answer": "Typed and dictated", "free_text": true, "version": 1, "answer_id": "reviewed"}
	if s := owner.do("POST", "/api/approvals/"+rec.CardIDs[0], answer, nil); s != 200 {
		t.Fatalf("answer: %d", s)
	}
	if s := lead.do("POST", path, req, &rec); s != 200 {
		t.Fatalf("retry: %d", s)
	}
	got, err := d.Fleet.Approvals("")
	if err != nil || len(got) != 1 || got[0].Answer != "Typed and dictated" {
		t.Fatalf("preserved answer: %+v %v", got, err)
	}
}
